package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/hiroshi-os/ledgerd/internal/app"
	"github.com/hiroshi-os/ledgerd/internal/testutil"
)

// SequenceCount is the number of random operation sequences checked.
const SequenceCount = 200

type propIntent struct {
	id, account, currency, status string
	amount, captured, refunded    int64
}

func TestPropertyPaymentSequences(t *testing.T) {
	a := testutil.NewApp(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(99))

	for seq := 0; seq < SequenceCount; seq++ {
		if err := a.Reset(ctx, testutil.BootstrapKey, "test"); err != nil {
			t.Fatalf("seq %d reset: %v", seq, err)
		}

		nAccounts := 1 + rng.Intn(4)
		type acct struct{ id, currency string }
		accounts := make([]acct, 0, nAccounts)
		currencies := []string{"usd", "eur"}
		for i := 0; i < nAccounts; i++ {
			currency := currencies[rng.Intn(len(currencies))]
			res, err := a.CreateAccount(ctx, app.Request{
				Token: testutil.BootstrapKey, IdempotencyKey: fmt.Sprintf("p-acct-%d-%d", seq, i),
				Method: "POST", Path: "/v1/accounts", Body: []byte(`{"currency":"` + currency + `"}`),
			}, currency)
			if err != nil {
				t.Fatalf("seq %d create account: %v", seq, err)
			}
			var m map[string]any
			_ = json.Unmarshal(res.Body, &m)
			accounts = append(accounts, acct{id: m["id"].(string), currency: currency})
		}

		var intents []propIntent
		ops := 5 + rng.Intn(21)
		for i := 0; i < ops; i++ {
			switch rng.Intn(5) {
			case 0:
				pick := accounts[rng.Intn(len(accounts))]
				amount := int64(1 + rng.Intn(5000))
				script := []string{"succeed"}
				body := fmt.Sprintf(`{"account_id":%q,"amount":%d,"currency":%q,"processor_script":["succeed"]}`, pick.id, amount, pick.currency)
				res, err := a.CreatePaymentIntent(ctx, app.Request{
					Token: testutil.BootstrapKey, IdempotencyKey: fmt.Sprintf("p-pi-%d-%d", seq, i),
					Method: "POST", Path: "/v1/payment_intents", Body: []byte(body),
				}, pick.id, amount, pick.currency, script)
				if err != nil {
					t.Fatalf("seq %d create pi: %v", seq, err)
				}
				var m map[string]any
				_ = json.Unmarshal(res.Body, &m)
				intents = append(intents, propIntent{
					id: m["id"].(string), account: pick.id, currency: pick.currency,
					status: "requires_confirmation", amount: amount,
				})
			case 1:
				if len(intents) == 0 {
					continue
				}
				it := &intents[rng.Intn(len(intents))]
				if it.status != "requires_confirmation" {
					continue
				}
				_, err := a.ConfirmPaymentIntent(ctx, app.Request{
					Token: testutil.BootstrapKey, IdempotencyKey: "p-conf-" + it.id,
					Method: "POST", Path: "/v1/payment_intents/" + it.id + "/confirm", Body: []byte(`{}`),
				}, it.id)
				if err != nil {
					continue
				}
				it.status = "processing"
			case 2:
				if len(intents) == 0 {
					continue
				}
				it := &intents[rng.Intn(len(intents))]
				if it.status != "processing" {
					continue
				}
				_, err := a.CapturePaymentIntent(ctx, app.Request{
					Token: testutil.BootstrapKey, IdempotencyKey: "p-cap-" + it.id,
					Method: "POST", Path: "/v1/payment_intents/" + it.id + "/capture", Body: []byte(`{}`),
				}, it.id)
				if err != nil {
					t.Fatalf("seq %d capture: %v", seq, err)
				}
				it.status = "succeeded"
				it.captured = it.amount
			default:
				if len(intents) == 0 {
					continue
				}
				it := &intents[rng.Intn(len(intents))]
				if it.status != "succeeded" && it.status != "partially_refunded" {
					continue
				}
				remaining := it.captured - it.refunded
				if remaining <= 0 {
					continue
				}
				amt := int64(1 + rng.Intn(int(remaining)))
				body := fmt.Sprintf(`{"payment_intent_id":%q,"amount":%d}`, it.id, amt)
				_, err := a.CreateRefund(ctx, app.Request{
					Token: testutil.BootstrapKey, IdempotencyKey: fmt.Sprintf("p-ref-%s-%d-%d", it.id, amt, i),
					Method: "POST", Path: "/v1/refunds", Body: []byte(body),
				}, it.id, amt)
				if err != nil {
					t.Fatalf("seq %d refund: %v", seq, err)
				}
				it.refunded += amt
				if it.refunded == it.captured {
					it.status = "refunded"
				} else {
					it.status = "partially_refunded"
				}
			}
		}

		for _, currency := range []string{"usd", "eur", "gbp"} {
			var sum int64
			if err := a.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM ledger_entries WHERE currency=$1`, currency).Scan(&sum); err != nil {
				t.Fatalf("seq %d sum: %v", seq, err)
			}
			if sum != 0 {
				t.Fatalf("seq %d global sum for %s = %d", seq, currency, sum)
			}
		}

		rows, err := a.Pool.Query(ctx, `SELECT available_balance_minor FROM accounts WHERE kind = 'merchant'`)
		if err != nil {
			t.Fatalf("seq %d accounts: %v", seq, err)
		}
		for rows.Next() {
			var bal int64
			if err := rows.Scan(&bal); err != nil {
				rows.Close()
				t.Fatalf("seq %d scan: %v", seq, err)
			}
			if bal < 0 {
				rows.Close()
				t.Fatalf("seq %d merchant balance negative: %d", seq, bal)
			}
		}
		rows.Close()

		for _, it := range intents {
			if it.refunded > it.captured {
				t.Fatalf("seq %d refunded %d > captured %d", seq, it.refunded, it.captured)
			}
			if it.status == "succeeded" || it.status == "refunded" || it.status == "partially_refunded" {
				var n int
				if err := a.Pool.QueryRow(ctx, `
					SELECT COUNT(*) FROM ledger_transactions
					 WHERE payment_intent_id = $1::uuid AND kind = 'capture'`, it.id).Scan(&n); err != nil {
					t.Fatalf("seq %d capture count: %v", seq, err)
				}
				if n != 1 {
					t.Fatalf("seq %d succeeded intent %s has %d captures", seq, it.id, n)
				}
			}
		}
	}
	t.Logf("property sequences passed: %d (seed=99, at %s)", SequenceCount, time.Now().UTC().Format(time.RFC3339))
}

func TestPropertySequenceCountDocumented(t *testing.T) {
	if SequenceCount < 100 {
		t.Fatalf("SequenceCount too low: %d", SequenceCount)
	}
}
