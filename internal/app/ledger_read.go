package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) ListLedgerTransactions(ctx context.Context, token, paymentIntentID string) (Result, error) {
	apiKeyID, err := a.lookupAPIKey(ctx, token)
	if err != nil {
		return Result{}, err
	}
	var exists bool
	err = a.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM payment_intents WHERE id = $1::uuid AND api_key_id = $2::uuid)`,
		paymentIntentID, apiKeyID).Scan(&exists)
	if err != nil {
		return Result{}, err
	}
	if !exists {
		return Result{}, notFound("payment intent not found")
	}
	txns, err := a.loadLedger(ctx, paymentIntentID, "")
	if err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(map[string]any{"object": "list", "data": txns})
	if err != nil {
		return Result{}, err
	}
	return Result{Status: 200, Body: body}, nil
}

func (a *App) GetLedgerTransaction(ctx context.Context, token, id string) (Result, error) {
	apiKeyID, err := a.lookupAPIKey(ctx, token)
	if err != nil {
		return Result{}, err
	}
	var piID string
	err = a.Pool.QueryRow(ctx, `
		SELECT lt.payment_intent_id::text
		  FROM ledger_transactions lt
		  JOIN payment_intents pi ON pi.id = lt.payment_intent_id
		 WHERE lt.id = $1::uuid AND pi.api_key_id = $2::uuid`, id, apiKeyID).Scan(&piID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, notFound("ledger transaction not found")
	}
	if err != nil {
		return Result{}, err
	}
	txns, err := a.loadLedger(ctx, piID, id)
	if err != nil {
		return Result{}, err
	}
	if len(txns) != 1 {
		return Result{}, notFound("ledger transaction not found")
	}
	body, err := json.Marshal(txns[0])
	if err != nil {
		return Result{}, err
	}
	return Result{Status: 200, Body: body}, nil
}

func (a *App) loadLedger(ctx context.Context, paymentIntentID, onlyID string) ([]ledgerTxnDTO, error) {
	rows, err := a.Pool.Query(ctx, `
		SELECT id::text, kind, currency, payment_intent_id::text, created_at
		  FROM ledger_transactions
		 WHERE payment_intent_id = $1::uuid
		   AND ($2 = '' OR id::text = $2)
		 ORDER BY created_at, id`, paymentIntentID, onlyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txns []ledgerTxnDTO
	for rows.Next() {
		var dto ledgerTxnDTO
		var created time.Time
		if err := rows.Scan(&dto.ID, &dto.Kind, &dto.Currency, &dto.PaymentIntentID, &created); err != nil {
			return nil, err
		}
		dto.Object = "ledger_transaction"
		dto.CreatedAt = formatTime(created)
		dto.Entries = []ledgerEntryDTO{}
		txns = append(txns, dto)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if txns == nil {
		txns = []ledgerTxnDTO{}
	}
	for i := range txns {
		entries, err := a.loadEntries(ctx, txns[i].ID)
		if err != nil {
			return nil, err
		}
		txns[i].Entries = entries
	}
	return txns, nil
}

func (a *App) loadEntries(ctx context.Context, txnID string) ([]ledgerEntryDTO, error) {
	rows, err := a.Pool.Query(ctx, `
		SELECT id::text, account_id::text, amount_minor, currency, created_at
		  FROM ledger_entries
		 WHERE transaction_id = $1::uuid
		 ORDER BY created_at, id`, txnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledgerEntryDTO
	for rows.Next() {
		var e ledgerEntryDTO
		var created time.Time
		if err := rows.Scan(&e.ID, &e.AccountID, &e.Amount, &e.Currency, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = formatTime(created)
		out = append(out, e)
	}
	if out == nil {
		out = []ledgerEntryDTO{}
	}
	return out, rows.Err()
}

// Reset wipes domain rows and reseeds clearing accounts plus the bootstrap API key.
func (a *App) Reset(ctx context.Context, bootstrapToken, bootstrapName string) error {
	_, err := a.Pool.Exec(ctx, `
		TRUNCATE TABLE
			ledger_entries,
			ledger_transactions,
			refunds,
			events,
			idempotency_keys,
			processor_charges,
			processor_attempts,
			payment_intents,
			accounts,
			api_keys
		RESTART IDENTITY CASCADE`)
	if err != nil {
		return err
	}
	_, err = a.Pool.Exec(ctx, `
		INSERT INTO accounts (id, api_key_id, kind, currency, available_balance_minor, balance_version, created_at)
		VALUES
			('00000000-0000-4000-8000-000000000001', NULL, 'clearing', 'usd', 0, 0, now()),
			('00000000-0000-4000-8000-000000000002', NULL, 'clearing', 'eur', 0, 0, now()),
			('00000000-0000-4000-8000-000000000003', NULL, 'clearing', 'gbp', 0, 0, now())`)
	if err != nil {
		return err
	}
	return a.BootstrapAPIKey(ctx, bootstrapToken, bootstrapName)
}
