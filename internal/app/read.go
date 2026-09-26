package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type rower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadAccount(ctx context.Context, q rower, id, apiKeyID string) (accountDTO, error) {
	var dto accountDTO
	var created time.Time
	err := q.QueryRow(ctx, `
		SELECT a.id::text, a.currency, a.kind, a.available_balance_minor, a.balance_version, a.created_at,
		       COALESCE(SUM(e.amount_minor), 0)
		  FROM accounts a
		  LEFT JOIN ledger_entries e ON e.account_id = a.id
		 WHERE a.id = $1::uuid
		   AND a.api_key_id = $2::uuid
		 GROUP BY a.id`, id, apiKeyID).Scan(
		&dto.ID, &dto.Currency, &dto.Kind, &dto.AvailableBalanceMinor, &dto.BalanceVersion, &created, &dto.DerivedBalanceMinor,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return accountDTO{}, notFound("account not found")
	}
	if err != nil {
		return accountDTO{}, err
	}
	dto.Object = "account"
	dto.CreatedAt = formatTime(created)
	dto.BalancesMatch = dto.AvailableBalanceMinor == dto.DerivedBalanceMinor
	return dto, nil
}

type paymentIntentDTO struct {
	ID             string  `json:"id"`
	Object         string  `json:"object"`
	AccountID      string  `json:"account_id"`
	Amount         int64   `json:"amount"`
	Currency       string  `json:"currency"`
	Status         string  `json:"status"`
	AmountCaptured int64   `json:"amount_captured"`
	AmountRefunded int64   `json:"amount_refunded"`
	ProcessorRef   *string `json:"processor_ref"`
	FailureReason  *string `json:"failure_reason"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type paymentIntentRow struct {
	dto    paymentIntentDTO
	script []string
}

func loadPaymentIntent(ctx context.Context, q rower, id, apiKeyID string) (paymentIntentRow, error) {
	var row paymentIntentRow
	var script []byte
	var created, updated time.Time
	err := q.QueryRow(ctx, `
		SELECT id::text, account_id::text, amount_minor, currency, status,
		       amount_captured_minor, amount_refunded_minor, processor_script,
		       processor_ref, failure_reason, created_at, updated_at
		  FROM payment_intents
		 WHERE id = $1::uuid AND api_key_id = $2::uuid`, id, apiKeyID).Scan(
		&row.dto.ID, &row.dto.AccountID, &row.dto.Amount, &row.dto.Currency, &row.dto.Status,
		&row.dto.AmountCaptured, &row.dto.AmountRefunded, &script,
		&row.dto.ProcessorRef, &row.dto.FailureReason, &created, &updated,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return paymentIntentRow{}, notFound("payment intent not found")
	}
	if err != nil {
		return paymentIntentRow{}, err
	}
	if len(script) > 0 {
		if err := json.Unmarshal(script, &row.script); err != nil {
			return paymentIntentRow{}, err
		}
	}
	row.dto.Object = "payment_intent"
	row.dto.CreatedAt = formatTime(created)
	row.dto.UpdatedAt = formatTime(updated)
	return row, nil
}

func marshalPI(row paymentIntentRow) ([]byte, error) {
	return json.Marshal(row.dto)
}

type refundDTO struct {
	ID                  string `json:"id"`
	Object              string `json:"object"`
	PaymentIntentID     string `json:"payment_intent_id"`
	Amount              int64  `json:"amount"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	LedgerTransactionID string `json:"ledger_transaction_id"`
	CreatedAt           string `json:"created_at"`
}

type ledgerEntryDTO struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	CreatedAt string `json:"created_at"`
}

type ledgerTxnDTO struct {
	ID              string           `json:"id"`
	Object          string           `json:"object"`
	Kind            string           `json:"kind"`
	Currency        string           `json:"currency"`
	PaymentIntentID string           `json:"payment_intent_id"`
	Entries         []ledgerEntryDTO `json:"entries"`
	CreatedAt       string           `json:"created_at"`
}
