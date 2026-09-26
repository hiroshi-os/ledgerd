package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hiroshi-os/ledgerd/internal/app"
	"github.com/hiroshi-os/ledgerd/internal/testutil"
)

func TestHappyPathCaptureAndRefund(t *testing.T) {
	a := testutil.NewApp(t)
	ctx := context.Background()
	acct := mustCreateAccount(t, a, "usd")
	pi := mustCreatePI(t, a, acct, 2500, "usd", []string{"succeed"})
	mustConfirm(t, a, pi)
	mustCapture(t, a, pi)
	ref := mustRefund(t, a, pi, 1000)
	if ref["amount"].(float64) != 1000 {
		t.Fatalf("refund amount %v", ref["amount"])
	}
	row := mustGetPI(t, a, pi)
	if row["status"] != "partially_refunded" {
		t.Fatalf("status %v", row["status"])
	}
	mustRefund(t, a, pi, 1500)
	row = mustGetPI(t, a, pi)
	if row["status"] != "refunded" {
		t.Fatalf("status %v", row["status"])
	}

	bal := mustGetAccount(t, a, acct)
	if bal["available_balance_minor"].(float64) != 0 {
		t.Fatalf("balance %v", bal["available_balance_minor"])
	}
	if bal["balances_match"] != true {
		t.Fatal("balances should match")
	}

	var sum int64
	if err := a.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM ledger_entries WHERE currency='usd'`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != 0 {
		t.Fatalf("global sum %d", sum)
	}
}

func TestIdempotentReplayAndMismatch(t *testing.T) {
	a := testutil.NewApp(t)
	ctx := context.Background()
	key := "idem-acct-1"
	req := app.Request{Token: testutil.BootstrapKey, IdempotencyKey: key, Method: "POST", Path: "/v1/accounts", Body: []byte(`{"currency":"usd"}`)}
	first, err := a.CreateAccount(ctx, req, "usd")
	if err != nil || first.Status != 201 {
		t.Fatalf("first %v %v", first, err)
	}
	second, err := a.CreateAccount(ctx, req, "usd")
	if err != nil || !second.Replayed || string(second.Body) != string(first.Body) {
		t.Fatalf("replay %v %v", second, err)
	}
	req2 := req
	req2.Body = []byte(`{"currency":"eur"}`)
	_, err = a.CreateAccount(ctx, req2, "eur")
	api, ok := err.(*app.APIError)
	if !ok || api.Status != 422 {
		t.Fatalf("expected 422, got %v", err)
	}
}

func TestValidationFailureNotStored(t *testing.T) {
	a := testutil.NewApp(t)
	ctx := context.Background()
	key := "idem-bad-1"
	req := app.Request{Token: testutil.BootstrapKey, IdempotencyKey: key, Method: "POST", Path: "/v1/accounts", Body: []byte(`{"currency":"jpy"}`)}
	_, err := a.CreateAccount(ctx, req, "jpy")
	if err == nil {
		t.Fatal("expected error")
	}
	reqOK := app.Request{Token: testutil.BootstrapKey, IdempotencyKey: key, Method: "POST", Path: "/v1/accounts", Body: []byte(`{"currency":"usd"}`)}
	res, err := a.CreateAccount(ctx, reqOK, "usd")
	if err != nil || res.Status != 201 {
		t.Fatalf("reusable key after validation failure: %v %v", res, err)
	}
}

func TestConcurrentIdempotency409(t *testing.T) {
	a := testutil.NewApp(t)
	ctx := context.Background()
	gate := make(chan struct{})
	a.SetBeforeCommit(func() { <-gate })
	defer a.SetBeforeCommit(nil)

	key := "idem-race-1"
	req := app.Request{Token: testutil.BootstrapKey, IdempotencyKey: key, Method: "POST", Path: "/v1/accounts", Body: []byte(`{"currency":"usd"}`)}

	var wg sync.WaitGroup
	wg.Add(2)
	var firstRes, secondRes app.Result
	var firstErr, secondErr error
	go func() {
		defer wg.Done()
		firstRes, firstErr = a.CreateAccount(ctx, req, "usd")
	}()
	time.Sleep(50 * time.Millisecond)
	go func() {
		defer wg.Done()
		secondRes, secondErr = a.CreateAccount(ctx, req, "usd")
	}()
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	got409 := false
	if api, ok := secondErr.(*app.APIError); ok && api.Status == 409 {
		got409 = true
	}
	if api, ok := firstErr.(*app.APIError); ok && api.Status == 409 {
		got409 = true
	}
	if !got409 {
		t.Fatalf("expected one 409; first=%v/%v second=%v/%v", firstRes, firstErr, secondRes, secondErr)
	}
}

func TestProcessorTimeoutThenSucceed(t *testing.T) {
	a := testutil.NewApp(t)
	acct := mustCreateAccount(t, a, "usd")
	pi := mustCreatePI(t, a, acct, 500, "usd", []string{"timeout", "succeed"})
	_, err := a.ConfirmPaymentIntent(context.Background(), app.Request{
		Token: testutil.BootstrapKey, IdempotencyKey: "c1", Method: "POST",
		Path: "/v1/payment_intents/" + pi + "/confirm", Body: []byte(`{}`),
	}, pi)
	api, ok := err.(*app.APIError)
	if !ok || api.Status != 503 {
		t.Fatalf("expected timeout 503, got %v", err)
	}
	mustConfirm(t, a, pi)
	mustCapture(t, a, pi)
}

func TestAppendOnlyLedger(t *testing.T) {
	a := testutil.NewApp(t)
	ctx := context.Background()
	acct := mustCreateAccount(t, a, "usd")
	pi := mustCreatePI(t, a, acct, 100, "usd", []string{"succeed"})
	mustConfirm(t, a, pi)
	mustCapture(t, a, pi)
	_, err := a.Pool.Exec(ctx, `UPDATE ledger_entries SET amount_minor = 1 WHERE true`)
	if err == nil {
		t.Fatal("expected append-only reject")
	}
	_, err = a.Pool.Exec(ctx, `DELETE FROM ledger_entries`)
	if err == nil {
		t.Fatal("expected append-only reject on delete")
	}
}

func mustCreateAccount(t *testing.T, a *app.App, currency string) string {
	t.Helper()
	res, err := a.CreateAccount(context.Background(), app.Request{
		Token: testutil.BootstrapKey, IdempotencyKey: "acct-" + currency + "-" + fmt.Sprint(time.Now().UnixNano()),
		Method: "POST", Path: "/v1/accounts", Body: []byte(`{"currency":"` + currency + `"}`),
	}, currency)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)
	return m["id"].(string)
}

func mustCreatePI(t *testing.T, a *app.App, accountID string, amount int64, currency string, script []string) string {
	t.Helper()
	scriptJSON, _ := json.Marshal(script)
	body := fmt.Sprintf(`{"account_id":%q,"amount":%d,"currency":%q,"processor_script":%s}`, accountID, amount, currency, scriptJSON)
	res, err := a.CreatePaymentIntent(context.Background(), app.Request{
		Token: testutil.BootstrapKey, IdempotencyKey: "pi-" + fmt.Sprint(time.Now().UnixNano()),
		Method: "POST", Path: "/v1/payment_intents", Body: []byte(body),
	}, accountID, amount, currency, script)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)
	return m["id"].(string)
}

func mustConfirm(t *testing.T, a *app.App, id string) {
	t.Helper()
	_, err := a.ConfirmPaymentIntent(context.Background(), app.Request{
		Token: testutil.BootstrapKey, IdempotencyKey: "confirm-" + id,
		Method: "POST", Path: "/v1/payment_intents/" + id + "/confirm", Body: []byte(`{}`),
	}, id)
	if err != nil {
		t.Fatal(err)
	}
}

func mustCapture(t *testing.T, a *app.App, id string) {
	t.Helper()
	_, err := a.CapturePaymentIntent(context.Background(), app.Request{
		Token: testutil.BootstrapKey, IdempotencyKey: "capture-" + id,
		Method: "POST", Path: "/v1/payment_intents/" + id + "/capture", Body: []byte(`{}`),
	}, id)
	if err != nil {
		t.Fatal(err)
	}
}

func mustRefund(t *testing.T, a *app.App, pi string, amount int64) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"payment_intent_id":%q,"amount":%d}`, pi, amount)
	res, err := a.CreateRefund(context.Background(), app.Request{
		Token: testutil.BootstrapKey, IdempotencyKey: fmt.Sprintf("ref-%s-%d-%d", pi, amount, time.Now().UnixNano()),
		Method: "POST", Path: "/v1/refunds", Body: []byte(body),
	}, pi, amount)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)
	return m
}

func mustGetPI(t *testing.T, a *app.App, id string) map[string]any {
	t.Helper()
	res, err := a.GetPaymentIntent(context.Background(), testutil.BootstrapKey, id)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)
	return m
}

func mustGetAccount(t *testing.T, a *app.App, id string) map[string]any {
	t.Helper()
	res, err := a.GetAccount(context.Background(), testutil.BootstrapKey, id)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)
	return m
}
