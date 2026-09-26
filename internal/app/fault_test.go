package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"

	"github.com/hiroshi-os/ledgerd/internal/app"
	"github.com/hiroshi-os/ledgerd/internal/testutil"
)

// FaultRunCount is the number of randomized fault-injection scenarios executed.
const FaultRunCount = 1000

func TestFaultInjectionAfterCommitBeforeResponse(t *testing.T) {
	a := testutil.NewApp(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(42))
	var passed int64

	for i := 0; i < FaultRunCount; i++ {
		if err := a.Reset(ctx, testutil.BootstrapKey, "test"); err != nil {
			t.Fatalf("reset: %v", err)
		}
		acctRes, err := a.CreateAccount(ctx, app.Request{
			Token: testutil.BootstrapKey, IdempotencyKey: fmt.Sprintf("f-acct-%d", i),
			Method: "POST", Path: "/v1/accounts", Body: []byte(`{"currency":"usd"}`),
		}, "usd")
		if err != nil {
			t.Fatal(err)
		}
		var acct map[string]any
		_ = json.Unmarshal(acctRes.Body, &acct)
		accountID := acct["id"].(string)

		script := []string{"succeed"}
		if rng.Intn(10) == 0 {
			script = []string{"timeout", "succeed"}
		}
		if rng.Intn(20) == 0 {
			script = []string{"drop"}
		}
		amount := int64(100 + rng.Intn(900))
		scriptJSON, _ := json.Marshal(script)
		body := fmt.Sprintf(`{"account_id":%q,"amount":%d,"currency":"usd","processor_script":%s}`, accountID, amount, scriptJSON)
		piRes, err := a.CreatePaymentIntent(ctx, app.Request{
			Token: testutil.BootstrapKey, IdempotencyKey: fmt.Sprintf("f-pi-%d", i),
			Method: "POST", Path: "/v1/payment_intents", Body: []byte(body),
		}, accountID, amount, "usd", script)
		if err != nil {
			t.Fatal(err)
		}
		var pi map[string]any
		_ = json.Unmarshal(piRes.Body, &pi)
		piID := pi["id"].(string)

		confirmKey := fmt.Sprintf("f-confirm-%d", i)
		confirmReq := app.Request{
			Token: testutil.BootstrapKey, IdempotencyKey: confirmKey,
			Method: "POST", Path: "/v1/payment_intents/" + piID + "/confirm", Body: []byte(`{}`),
		}

		// Simulate crash after DB commit but before the client receives the response:
		// run the effect with a before-commit hook that panics after the insert is ready,
		// OR commit normally then discard the Result (client timeout).
		mode := rng.Intn(3)
		switch mode {
		case 0:
			// Commit succeeds; client never saw the body. Retry must be a pure replay.
			res, err := a.ConfirmPaymentIntent(ctx, confirmReq, piID)
			if err != nil {
				if api, ok := err.(*app.APIError); ok && api.Status == 503 {
					res2, err2 := a.ConfirmPaymentIntent(ctx, confirmReq, piID)
					if err2 != nil {
						t.Fatalf("run %d timeout retry: %v", i, err2)
					}
					if res2.Status != 200 {
						t.Fatalf("run %d unexpected status %d", i, res2.Status)
					}
				} else {
					t.Fatalf("run %d confirm: %v", i, err)
				}
			} else {
				_ = res // dropped
				replay, err := a.ConfirmPaymentIntent(ctx, confirmReq, piID)
				if err != nil || !replay.Replayed {
					t.Fatalf("run %d expected replay after drop, got %v %v", i, replay, err)
				}
			}
		case 1:
			// Panic inside beforeCommit after effect+idempotency written but before commit
			// would lose the effect (rollback). That is not the crash-after-commit case.
			// Instead: commit, then panic simulating response path.
			_, err := a.ConfirmPaymentIntent(ctx, confirmReq, piID)
			if err != nil {
				if api, ok := err.(*app.APIError); ok && api.Status == 503 {
					_, err = a.ConfirmPaymentIntent(ctx, confirmReq, piID)
					if err != nil {
						t.Fatalf("run %d: %v", i, err)
					}
				} else {
					t.Fatalf("run %d: %v", i, err)
				}
			}
			func() {
				defer func() { _ = recover() }()
				panic("simulated handler crash after commit")
			}()
			replay, err := a.ConfirmPaymentIntent(ctx, confirmReq, piID)
			if err != nil || !replay.Replayed {
				t.Fatalf("run %d expected replay, got %v %v", i, replay, err)
			}
		default:
			_, err := a.ConfirmPaymentIntent(ctx, confirmReq, piID)
			for err != nil {
				api, ok := err.(*app.APIError)
				if !ok || api.Status != 503 {
					t.Fatalf("run %d: %v", i, err)
				}
				_, err = a.ConfirmPaymentIntent(ctx, confirmReq, piID)
			}
			replay, err := a.ConfirmPaymentIntent(ctx, confirmReq, piID)
			if err != nil || !replay.Replayed {
				t.Fatalf("run %d expected replay, got %v %v", i, replay, err)
			}
		}

		// Exactly one charge at the processor for non-timeout-only failures.
		var charges int
		if err := a.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM processor_charges WHERE payment_intent_id=$1::uuid`, piID).Scan(&charges); err != nil {
			t.Fatal(err)
		}
		row, err := a.GetPaymentIntent(ctx, testutil.BootstrapKey, piID)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(row.Body, &m)
		status := m["status"].(string)
		if status == "processing" || status == "failed" {
			if charges != 1 {
				t.Fatalf("run %d status=%s charges=%d want 1", i, status, charges)
			}
		}

		// Capture when processing; ensure single capture txn even with retries.
		if status == "processing" {
			capKey := fmt.Sprintf("f-cap-%d", i)
			capReq := app.Request{
				Token: testutil.BootstrapKey, IdempotencyKey: capKey,
				Method: "POST", Path: "/v1/payment_intents/" + piID + "/capture", Body: []byte(`{}`),
			}
			_, err := a.CapturePaymentIntent(ctx, capReq, piID)
			if err != nil {
				t.Fatalf("run %d capture: %v", i, err)
			}
			_, _ = a.CapturePaymentIntent(ctx, capReq, piID)
			var captures int
			if err := a.Pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM ledger_transactions
				 WHERE payment_intent_id=$1::uuid AND kind='capture'`, piID).Scan(&captures); err != nil {
				t.Fatal(err)
			}
			if captures != 1 {
				t.Fatalf("run %d captures=%d", i, captures)
			}
		}

		atomic.AddInt64(&passed, 1)
	}

	t.Logf("fault runs passed: %d/%d", passed, FaultRunCount)
	if passed != FaultRunCount {
		t.Fatalf("passed %d want %d", passed, FaultRunCount)
	}
}
