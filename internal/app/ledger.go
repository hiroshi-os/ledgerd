package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hiroshi-os/ledgerd/internal/money"
)

var clearingAccount = map[string]string{
	"usd": "00000000-0000-4000-8000-000000000001",
	"eur": "00000000-0000-4000-8000-000000000002",
	"gbp": "00000000-0000-4000-8000-000000000003",
}

type movement struct {
	AccountID string
	Delta     int64
}

// postMovement writes one balanced ledger transaction and updates materialized
// balances. Accounts are locked with SELECT ... FOR UPDATE in sorted id order
// so concurrent refunds cannot both observe the same balance.
//
// Blocking row locks (not NOWAIT, not optimistic version checks) are used because
// a refund that arrives while another refund holds the account should wait and
// then see the reduced balance. Idempotency uses the opposite policy; see execute.
func postMovement(ctx context.Context, tx pgx.Tx, kind, currency, paymentIntentID string, moves []movement) (string, error) {
	if len(moves) < 2 {
		return "", fmt.Errorf("ledger transaction %s needs at least two entries", kind)
	}
	var parts []int64
	for _, m := range moves {
		parts = append(parts, m.Delta)
	}
	sum, ok := money.Sum(parts...)
	if !ok || sum != 0 {
		return "", fmt.Errorf("unbalanced %s movement", kind)
	}

	ids := make([]string, 0, len(moves))
	seen := map[string]struct{}{}
	for _, m := range moves {
		if _, ok := seen[m.AccountID]; ok {
			return "", fmt.Errorf("duplicate account in movement")
		}
		seen[m.AccountID] = struct{}{}
		ids = append(ids, m.AccountID)
	}
	sort.Strings(ids)

	type acct struct {
		kind     string
		currency string
		balance  int64
	}
	locked := make(map[string]acct, len(ids))
	for _, id := range ids {
		var row acct
		err := tx.QueryRow(ctx, `
			SELECT kind, currency, available_balance_minor
			  FROM accounts
			 WHERE id = $1::uuid
			 FOR UPDATE`, id).Scan(&row.kind, &row.currency, &row.balance)
		if err != nil {
			return "", fmt.Errorf("lock account %s: %w", id, err)
		}
		if row.currency != currency {
			return "", fmt.Errorf("account %s currency %s != %s", id, row.currency, currency)
		}
		locked[id] = row
	}

	newBal := make(map[string]int64, len(moves))
	for _, m := range moves {
		next, ok := money.Add(locked[m.AccountID].balance, m.Delta)
		if !ok {
			return "", &APIError{Status: 500, Type: "api_error", Code: "balance_overflow", Message: "balance overflow"}
		}
		if locked[m.AccountID].kind == "merchant" && next < 0 {
			return "", invalid("insufficient_balance", "account available balance is too low for this refund")
		}
		newBal[m.AccountID] = next
	}

	txnID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_transactions (id, kind, currency, payment_intent_id, created_at)
		VALUES ($1, $2, $3, $4::uuid, now())`, txnID, kind, currency, paymentIntentID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "one_capture_per_intent" {
			return "", invalid("invalid_state", "payment intent already has a capture")
		}
		return "", err
	}

	for _, m := range moves {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ledger_entries (id, transaction_id, account_id, amount_minor, currency, created_at)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, now())`,
			uuid.New(), txnID, m.AccountID, m.Delta, currency); err != nil {
			return "", err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE accounts
			   SET available_balance_minor = $2,
			       balance_version = balance_version + 1
			 WHERE id = $1::uuid`, m.AccountID, newBal[m.AccountID])
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "merchant_nonnegative" {
				return "", invalid("insufficient_balance", "account available balance is too low for this refund")
			}
			return "", err
		}
		if tag.RowsAffected() != 1 {
			return "", fmt.Errorf("balance update affected %d rows", tag.RowsAffected())
		}
	}

	for _, id := range ids {
		var derived int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount_minor), 0)
			  FROM ledger_entries
			 WHERE account_id = $1::uuid`, id).Scan(&derived); err != nil {
			return "", err
		}
		if derived != newBal[id] {
			return "", &APIError{
				Status:  500,
				Type:    "api_error",
				Code:    "balance_mismatch",
				Message: "materialized balance does not match the sum of ledger entries",
			}
		}
	}
	return txnID, nil
}
