package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hiroshi-os/ledgerd/internal/money"
)

func (a *App) CreateRefund(ctx context.Context, req Request, paymentIntentID string, amount int64) (Result, error) {
	if _, err := uuid.Parse(paymentIntentID); err != nil {
		return Result{}, invalid("parameter_invalid", "payment_intent_id must be a UUID")
	}
	if amount <= 0 || amount > money.MaxMinor {
		return Result{}, invalid("parameter_invalid", "amount must be a positive integer minor unit")
	}
	return a.execute(ctx, req, func(ctx context.Context, tx pgx.Tx, apiKeyID string) (Result, error) {
		var status, accountID, currency string
		var captured, refunded int64
		err := tx.QueryRow(ctx, `
			SELECT status, account_id::text, currency, amount_captured_minor, amount_refunded_minor
			  FROM payment_intents
			 WHERE id = $1::uuid AND api_key_id = $2::uuid
			 FOR UPDATE`, paymentIntentID, apiKeyID).Scan(&status, &accountID, &currency, &captured, &refunded)
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, notFound("payment intent not found")
		}
		if err != nil {
			return Result{}, err
		}
		if status != "succeeded" && status != "partially_refunded" {
			return Result{}, invalid("invalid_state", "payment intent cannot be refunded from status "+status)
		}
		remaining := captured - refunded
		if amount > remaining {
			return Result{}, invalid("amount_exceeds_refundable", "refund amount exceeds the unrefunded captured amount")
		}
		clearingID, ok := clearingAccount[currency]
		if !ok {
			return Result{}, invalid("parameter_invalid", "no clearing account for currency")
		}

		txnID, err := postMovement(ctx, tx, "refund", currency, paymentIntentID, []movement{
			{AccountID: accountID, Delta: -amount},
			{AccountID: clearingID, Delta: amount},
		})
		if err != nil {
			return Result{}, err
		}

		newRefunded := refunded + amount
		newStatus := "partially_refunded"
		if newRefunded == captured {
			newStatus = "refunded"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE payment_intents
			   SET status = $2,
			       amount_refunded_minor = $3,
			       version = version + 1,
			       updated_at = now()
			 WHERE id = $1::uuid`, paymentIntentID, newStatus, newRefunded); err != nil {
			return Result{}, err
		}

		refundID := newID()
		var created time.Time
		err = tx.QueryRow(ctx, `
			INSERT INTO refunds (
				id, api_key_id, payment_intent_id, amount_minor, currency, status, ledger_transaction_id, created_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, 'succeeded', $6::uuid, now())
			RETURNING created_at`, refundID, apiKeyID, paymentIntentID, amount, currency, txnID).Scan(&created)
		if err != nil {
			return Result{}, err
		}
		if err := writeEvent(ctx, tx, "refund.created", refundID, map[string]any{
			"id": refundID, "payment_intent_id": paymentIntentID, "amount": amount, "currency": currency,
		}); err != nil {
			return Result{}, err
		}
		dto := refundDTO{
			ID:                  refundID,
			Object:              "refund",
			PaymentIntentID:     paymentIntentID,
			Amount:              amount,
			Currency:            currency,
			Status:              "succeeded",
			LedgerTransactionID: txnID,
			CreatedAt:           formatTime(created),
		}
		body, err := json.Marshal(dto)
		if err != nil {
			return Result{}, err
		}
		return Result{Status: 201, Body: body}, nil
	})
}

func (a *App) GetRefund(ctx context.Context, token, id string) (Result, error) {
	apiKeyID, err := a.lookupAPIKey(ctx, token)
	if err != nil {
		return Result{}, err
	}
	var dto refundDTO
	var created time.Time
	err = a.Pool.QueryRow(ctx, `
		SELECT id::text, payment_intent_id::text, amount_minor, currency, status, ledger_transaction_id::text, created_at
		  FROM refunds
		 WHERE id = $1::uuid AND api_key_id = $2::uuid`, id, apiKeyID).Scan(
		&dto.ID, &dto.PaymentIntentID, &dto.Amount, &dto.Currency, &dto.Status, &dto.LedgerTransactionID, &created,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, notFound("refund not found")
	}
	if err != nil {
		return Result{}, err
	}
	dto.Object = "refund"
	dto.CreatedAt = formatTime(created)
	body, err := json.Marshal(dto)
	if err != nil {
		return Result{}, err
	}
	return Result{Status: 200, Body: body}, nil
}
