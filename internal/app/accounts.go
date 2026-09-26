package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hiroshi-os/ledgerd/internal/money"
)

type accountDTO struct {
	ID                    string `json:"id"`
	Object                string `json:"object"`
	Currency              string `json:"currency"`
	Kind                  string `json:"kind"`
	AvailableBalanceMinor int64  `json:"available_balance_minor"`
	DerivedBalanceMinor   int64  `json:"derived_balance_minor"`
	BalancesMatch         bool   `json:"balances_match"`
	BalanceVersion        int64  `json:"balance_version"`
	CreatedAt             string `json:"created_at"`
}

func (a *App) CreateAccount(ctx context.Context, req Request, currency string) (Result, error) {
	if !money.ValidCurrency(currency) {
		return Result{}, invalid("parameter_invalid", "currency must be one of usd, eur, gbp")
	}
	return a.execute(ctx, req, func(ctx context.Context, tx pgx.Tx, apiKeyID string) (Result, error) {
		id := newID()
		var created time.Time
		err := tx.QueryRow(ctx, `
			INSERT INTO accounts (id, api_key_id, kind, currency, available_balance_minor, balance_version, created_at)
			VALUES ($1::uuid, $2::uuid, 'merchant', $3, 0, 0, now())
			RETURNING created_at`, id, apiKeyID, currency).Scan(&created)
		if err != nil {
			return Result{}, err
		}
		dto := accountDTO{
			ID:                    id,
			Object:                "account",
			Currency:              currency,
			Kind:                  "merchant",
			AvailableBalanceMinor: 0,
			DerivedBalanceMinor:   0,
			BalancesMatch:         true,
			BalanceVersion:        0,
			CreatedAt:             formatTime(created),
		}
		if err := writeEvent(ctx, tx, "account.created", id, map[string]any{
			"id": id, "currency": currency,
		}); err != nil {
			return Result{}, err
		}
		body, err := json.Marshal(dto)
		if err != nil {
			return Result{}, err
		}
		return Result{Status: 201, Body: body}, nil
	})
}

func (a *App) GetAccount(ctx context.Context, token, id string) (Result, error) {
	apiKeyID, err := a.lookupAPIKey(ctx, token)
	if err != nil {
		return Result{}, err
	}
	dto, err := loadAccount(ctx, a.Pool, id, apiKeyID)
	if err != nil {
		return Result{}, err
	}
	if !dto.BalancesMatch {
		return Result{}, &APIError{
			Status:  500,
			Type:    "api_error",
			Code:    "balance_mismatch",
			Message: "materialized balance does not match the sum of ledger entries",
		}
	}
	body, err := json.Marshal(dto)
	if err != nil {
		return Result{}, err
	}
	return Result{Status: 200, Body: body}, nil
}
