package processor

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Charge is a processor-side authorization record.
type Charge struct {
	Outcome Outcome
	Ref     string
}

// Authorize commits on pool, independent of the caller's ledger transaction.
// A timeout persists the attempt counter and no charge, so a later attempt can succeed.
// A recorded charge is returned unchanged on retry (processor-side idempotency).
func Authorize(ctx context.Context, pool *pgxpool.Pool, seed int64, paymentIntentID uuid.UUID, script []string) (Charge, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Charge{}, err
	}
	defer tx.Rollback(ctx)

	var outcome, ref string
	err = tx.QueryRow(ctx, `
		SELECT outcome, processor_ref
		  FROM processor_charges
		 WHERE payment_intent_id = $1
		 FOR UPDATE`, paymentIntentID).Scan(&outcome, &ref)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Charge{}, err
		}
		return Charge{Outcome: Outcome(outcome), Ref: ref}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Charge{}, err
	}

	var attempt int
	err = tx.QueryRow(ctx, `
		INSERT INTO processor_attempts (payment_intent_id, attempt_count, updated_at)
		VALUES ($1, 1, now())
		ON CONFLICT (payment_intent_id) DO UPDATE
		    SET attempt_count = processor_attempts.attempt_count + 1,
		        updated_at = now()
		RETURNING attempt_count`, paymentIntentID).Scan(&attempt)
	if err != nil {
		return Charge{}, err
	}

	decided, err := Decide(seed, paymentIntentID, attempt, script)
	if err != nil {
		return Charge{}, err
	}
	if decided == OutcomeTimeout {
		if err := tx.Commit(ctx); err != nil {
			return Charge{}, err
		}
		return Charge{Outcome: OutcomeTimeout}, nil
	}

	ref = Ref(paymentIntentID)
	_, err = tx.Exec(ctx, `
		INSERT INTO processor_charges (payment_intent_id, outcome, processor_ref, created_at)
		VALUES ($1, $2, $3, now())`, paymentIntentID, string(decided), ref)
	if err != nil {
		return Charge{}, fmt.Errorf("record processor charge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Charge{}, err
	}
	return Charge{Outcome: decided, Ref: ref}, nil
}
