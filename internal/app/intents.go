package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hiroshi-os/ledgerd/internal/money"
	"github.com/hiroshi-os/ledgerd/internal/processor"
)

func (a *App) CreatePaymentIntent(ctx context.Context, req Request, accountID string, amount int64, currency string, script []string) (Result, error) {
	if _, err := uuid.Parse(accountID); err != nil {
		return Result{}, invalid("parameter_invalid", "account_id must be a UUID")
	}
	if amount <= 0 || amount > money.MaxMinor {
		return Result{}, invalid("parameter_invalid", "amount must be a positive integer minor unit")
	}
	if !money.ValidCurrency(currency) {
		return Result{}, invalid("parameter_invalid", "currency must be one of usd, eur, gbp")
	}
	if !processor.ValidScript(script) {
		return Result{}, invalid("parameter_invalid", "processor_script entries must be succeed, decline, timeout, or drop")
	}
	scriptJSON, err := json.Marshal(script)
	if err != nil {
		return Result{}, err
	}

	return a.execute(ctx, req, func(ctx context.Context, tx pgx.Tx, apiKeyID string) (Result, error) {
		var acctCurrency, kind string
		err := tx.QueryRow(ctx, `
			SELECT currency, kind FROM accounts
			 WHERE id = $1::uuid AND api_key_id = $2::uuid`, accountID, apiKeyID).Scan(&acctCurrency, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, notFound("account not found")
		}
		if err != nil {
			return Result{}, err
		}
		if kind != "merchant" {
			return Result{}, invalid("parameter_invalid", "account cannot accept payment intents")
		}
		if acctCurrency != currency {
			return Result{}, invalid("parameter_invalid", "currency does not match the account")
		}

		id := newID()
		var created time.Time
		err = tx.QueryRow(ctx, `
			INSERT INTO payment_intents (
				id, api_key_id, account_id, amount_minor, currency, status,
				amount_captured_minor, amount_refunded_minor, processor_script,
				version, created_at, updated_at
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4, $5, 'requires_confirmation',
				0, 0, $6,
				0, now(), now()
			)
			RETURNING created_at`, id, apiKeyID, accountID, amount, currency, scriptJSON).Scan(&created)
		if err != nil {
			return Result{}, err
		}
		if err := writeEvent(ctx, tx, "payment_intent.created", id, map[string]any{
			"id": id, "account_id": accountID, "amount": amount, "currency": currency,
		}); err != nil {
			return Result{}, err
		}
		row := paymentIntentRow{dto: paymentIntentDTO{
			ID:             id,
			Object:         "payment_intent",
			AccountID:      accountID,
			Amount:         amount,
			Currency:       currency,
			Status:         "requires_confirmation",
			AmountCaptured: 0,
			AmountRefunded: 0,
			CreatedAt:      formatTime(created),
			UpdatedAt:      formatTime(created),
		}}
		body, err := marshalPI(row)
		if err != nil {
			return Result{}, err
		}
		return Result{Status: 201, Body: body}, nil
	})
}

func (a *App) GetPaymentIntent(ctx context.Context, token, id string) (Result, error) {
	apiKeyID, err := a.lookupAPIKey(ctx, token)
	if err != nil {
		return Result{}, err
	}
	row, err := loadPaymentIntent(ctx, a.Pool, id, apiKeyID)
	if err != nil {
		return Result{}, err
	}
	body, err := marshalPI(row)
	if err != nil {
		return Result{}, err
	}
	return Result{Status: 200, Body: body}, nil
}

func (a *App) ConfirmPaymentIntent(ctx context.Context, req Request, id string) (Result, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Result{}, invalid("parameter_invalid", "payment intent id must be a UUID")
	}
	piUUID, _ := uuid.Parse(id)
	return a.execute(ctx, req, func(ctx context.Context, tx pgx.Tx, apiKeyID string) (Result, error) {
		row, err := loadPaymentIntent(ctx, tx, id, apiKeyID)
		if err != nil {
			return Result{}, err
		}
		var status string
		err = tx.QueryRow(ctx, `
			SELECT status FROM payment_intents
			 WHERE id = $1::uuid AND api_key_id = $2::uuid
			 FOR UPDATE`, id, apiKeyID).Scan(&status)
		if err != nil {
			return Result{}, err
		}
		if status != "requires_confirmation" {
			return Result{}, invalid("invalid_state", "payment intent cannot be confirmed from status "+status)
		}

		charge, err := processor.Authorize(ctx, a.Pool, a.ProcessorSeed, piUUID, row.script)
		if err != nil {
			return Result{}, err
		}
		if charge.Outcome == processor.OutcomeTimeout {
			return Result{}, &APIError{
				Status:  503,
				Type:    "processor_error",
				Code:    "processor_timeout",
				Message: "card processor timed out; retry the same request",
			}
		}

		switch charge.Outcome {
		case processor.OutcomeDecline:
			var updated time.Time
			err = tx.QueryRow(ctx, `
				UPDATE payment_intents
				   SET status = 'failed',
				       processor_ref = $2,
				       failure_reason = 'card_declined',
				       version = version + 1,
				       updated_at = now()
				 WHERE id = $1::uuid
				 RETURNING updated_at`, id, charge.Ref).Scan(&updated)
			if err != nil {
				return Result{}, err
			}
			row.dto.Status = "failed"
			row.dto.ProcessorRef = &charge.Ref
			reason := "card_declined"
			row.dto.FailureReason = &reason
			row.dto.UpdatedAt = formatTime(updated)
			if err := writeEvent(ctx, tx, "payment_intent.payment_failed", id, map[string]any{
				"id": id, "failure_reason": "card_declined",
			}); err != nil {
				return Result{}, err
			}
			body, err := marshalPI(row)
			if err != nil {
				return Result{}, err
			}
			return Result{Status: 200, Body: body}, nil

		case processor.OutcomeSucceed, processor.OutcomeDrop:
			var updated time.Time
			err = tx.QueryRow(ctx, `
				UPDATE payment_intents
				   SET status = 'processing',
				       processor_ref = $2,
				       version = version + 1,
				       updated_at = now()
				 WHERE id = $1::uuid
				 RETURNING updated_at`, id, charge.Ref).Scan(&updated)
			if err != nil {
				return Result{}, err
			}
			row.dto.Status = "processing"
			row.dto.ProcessorRef = &charge.Ref
			row.dto.UpdatedAt = formatTime(updated)
			if err := writeEvent(ctx, tx, "payment_intent.processing", id, map[string]any{
				"id": id, "processor_ref": charge.Ref,
			}); err != nil {
				return Result{}, err
			}
			body, err := marshalPI(row)
			if err != nil {
				return Result{}, err
			}
			return Result{Status: 200, Body: body, DropResponse: charge.Outcome == processor.OutcomeDrop}, nil
		default:
			return Result{}, &APIError{Status: 500, Type: "api_error", Code: "processor", Message: "unexpected processor outcome"}
		}
	})
}

func (a *App) CapturePaymentIntent(ctx context.Context, req Request, id string) (Result, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Result{}, invalid("parameter_invalid", "payment intent id must be a UUID")
	}
	return a.execute(ctx, req, func(ctx context.Context, tx pgx.Tx, apiKeyID string) (Result, error) {
		var status, accountID, currency string
		var amount int64
		var created time.Time
		err := tx.QueryRow(ctx, `
			SELECT status, account_id::text, amount_minor, currency, created_at
			  FROM payment_intents
			 WHERE id = $1::uuid AND api_key_id = $2::uuid
			 FOR UPDATE`, id, apiKeyID).Scan(&status, &accountID, &amount, &currency, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, notFound("payment intent not found")
		}
		if err != nil {
			return Result{}, err
		}
		if status != "processing" {
			return Result{}, invalid("invalid_state", "payment intent cannot be captured from status "+status)
		}
		clearingID, ok := clearingAccount[currency]
		if !ok {
			return Result{}, invalid("parameter_invalid", "no clearing account for currency")
		}

		if _, err := postMovement(ctx, tx, "capture", currency, id, []movement{
			{AccountID: accountID, Delta: amount},
			{AccountID: clearingID, Delta: -amount},
		}); err != nil {
			return Result{}, err
		}

		var updated time.Time
		var processorRef *string
		err = tx.QueryRow(ctx, `
			UPDATE payment_intents
			   SET status = 'succeeded',
			       amount_captured_minor = amount_minor,
			       version = version + 1,
			       updated_at = now()
			 WHERE id = $1::uuid
			 RETURNING updated_at, processor_ref`, id).Scan(&updated, &processorRef)
		if err != nil {
			return Result{}, err
		}
		if err := writeEvent(ctx, tx, "payment_intent.succeeded", id, map[string]any{
			"id": id, "amount": amount, "currency": currency,
		}); err != nil {
			return Result{}, err
		}
		row := paymentIntentRow{dto: paymentIntentDTO{
			ID:             id,
			Object:         "payment_intent",
			AccountID:      accountID,
			Amount:         amount,
			Currency:       currency,
			Status:         "succeeded",
			AmountCaptured: amount,
			AmountRefunded: 0,
			ProcessorRef:   processorRef,
			CreatedAt:      formatTime(created),
			UpdatedAt:      formatTime(updated),
		}}
		body, err := marshalPI(row)
		if err != nil {
			return Result{}, err
		}
		return Result{Status: 200, Body: body}, nil
	})
}
