package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type effectFunc func(ctx context.Context, tx pgx.Tx, apiKeyID string) (Result, error)

type storedResponse struct {
	Method   string
	Path     string
	BodyHash []byte
	Status   int
	Body     []byte
}

// execute runs effect and the idempotency insert in one database transaction.
//
// In-flight detection uses pg_try_advisory_xact_lock rather than waiting on the
// row. A concurrent duplicate therefore gets 409 immediately instead of blocking
// until the first request commits.
func (a *App) execute(ctx context.Context, req Request, effect effectFunc) (Result, error) {
	if err := validateIdempotencyKey(req.IdempotencyKey); err != nil {
		return Result{}, err
	}
	apiKeyID, err := a.lookupAPIKey(ctx, req.Token)
	if err != nil {
		return Result{}, err
	}

	bodyHash := sha256.Sum256(req.Body)
	ttlSeconds := int32(a.TTL.Seconds())

	if rec, ok, err := a.findFresh(ctx, a.Pool, apiKeyID, req.IdempotencyKey, ttlSeconds); err != nil {
		return Result{}, err
	} else if ok {
		return replayOrMismatch(rec, req, bodyHash[:])
	}

	tx, err := a.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)

	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, advisoryKey(apiKeyID, req.IdempotencyKey)).Scan(&locked); err != nil {
		return Result{}, err
	}
	if !locked {
		return Result{}, &APIError{
			Status:  409,
			Type:    "idempotency_error",
			Code:    "idempotency_key_in_use",
			Message: "a request with this Idempotency-Key is already in progress",
		}
	}

	if rec, ok, err := a.findFresh(ctx, tx, apiKeyID, req.IdempotencyKey, ttlSeconds); err != nil {
		return Result{}, err
	} else if ok {
		return replayOrMismatch(rec, req, bodyHash[:])
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM idempotency_keys
		 WHERE api_key_id = $1::uuid
		   AND idempotency_key = $2`, apiKeyID, req.IdempotencyKey); err != nil {
		return Result{}, err
	}

	res, err := effect(ctx, tx, apiKeyID)
	if err != nil {
		return Result{}, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (
			id, api_key_id, idempotency_key, method, path, body_sha256,
			response_status, response_body, created_at
		) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, now())`,
		newID(), apiKeyID, req.IdempotencyKey, req.Method, req.Path, bodyHash[:], res.Status, res.Body,
	); err != nil {
		return Result{}, fmt.Errorf("insert idempotency key: %w", err)
	}

	a.runBeforeCommit()
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return res, nil
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (a *App) findFresh(ctx context.Context, q queryRower, apiKeyID, key string, ttlSeconds int32) (storedResponse, bool, error) {
	var rec storedResponse
	err := q.QueryRow(ctx, `
		SELECT method, path, body_sha256, response_status, response_body
		  FROM idempotency_keys
		 WHERE api_key_id = $1::uuid
		   AND idempotency_key = $2
		   AND created_at > now() - make_interval(secs => $3::int)`,
		apiKeyID, key, ttlSeconds).Scan(&rec.Method, &rec.Path, &rec.BodyHash, &rec.Status, &rec.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedResponse{}, false, nil
	}
	if err != nil {
		return storedResponse{}, false, err
	}
	return rec, true, nil
}

func replayOrMismatch(rec storedResponse, req Request, bodyHash []byte) (Result, error) {
	if rec.Method != req.Method || rec.Path != req.Path || !bytesEqual(rec.BodyHash, bodyHash) {
		return Result{}, &APIError{
			Status:  422,
			Type:    "idempotency_error",
			Code:    "idempotency_key_mismatch",
			Message: "this Idempotency-Key was already used with a different request",
		}
	}
	return Result{Status: rec.Status, Body: rec.Body, Replayed: true}, nil
}

func (a *App) lookupAPIKey(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", unauthorized()
	}
	sum := sha256.Sum256([]byte(token))
	var id string
	err := a.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys WHERE token_sha256 = $1`, sum[:]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", unauthorized()
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func validateIdempotencyKey(k string) error {
	if k == "" {
		return invalid("idempotency_key_required", "Idempotency-Key header is required on POST")
	}
	if len(k) > 255 {
		return invalid("idempotency_key_too_long", "Idempotency-Key must be at most 255 characters")
	}
	for _, r := range k {
		if r < 0x21 || r > 0x7e {
			return invalid("idempotency_key_invalid", "Idempotency-Key must be printable ASCII")
		}
	}
	return nil
}

func advisoryKey(apiKeyID, idempotencyKey string) int64 {
	h := sha256.New()
	h.Write([]byte(apiKeyID))
	h.Write([]byte{0})
	h.Write([]byte(idempotencyKey))
	sum := h.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
