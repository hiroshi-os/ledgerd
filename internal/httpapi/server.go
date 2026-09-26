package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/hiroshi-os/ledgerd/internal/app"
	"github.com/hiroshi-os/ledgerd/internal/money"
)

const maxBody = 1 << 20

type Server struct {
	App *app.App
}

func New(a *app.App) http.Handler {
	s := &Server{App: a}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/accounts", s.createAccount)
	mux.HandleFunc("GET /v1/accounts/{id}", s.getAccount)
	mux.HandleFunc("POST /v1/payment_intents", s.createPaymentIntent)
	mux.HandleFunc("GET /v1/payment_intents/{id}", s.getPaymentIntent)
	mux.HandleFunc("POST /v1/payment_intents/{id}/confirm", s.confirmPaymentIntent)
	mux.HandleFunc("POST /v1/payment_intents/{id}/capture", s.capturePaymentIntent)
	mux.HandleFunc("POST /v1/refunds", s.createRefund)
	mux.HandleFunc("GET /v1/refunds/{id}", s.getRefund)
	mux.HandleFunc("GET /v1/payment_intents/{id}/ledger_transactions", s.listLedger)
	mux.HandleFunc("GET /v1/ledger_transactions/{id}", s.getLedger)
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	body, req, err := readPOST(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Currency string `json:"currency"`
	}
	if err := decodeStrict(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.App.CreateAccount(r.Context(), req, strings.ToLower(in.Currency))
	writeResult(w, res, err)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.GetAccount(r.Context(), bearer(r), r.PathValue("id"))
	writeResult(w, res, err)
}

func (s *Server) createPaymentIntent(w http.ResponseWriter, r *http.Request) {
	body, req, err := readPOST(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeErr(w, badJSON())
		return
	}
	var in struct {
		AccountID       string   `json:"account_id"`
		Currency        string   `json:"currency"`
		ProcessorScript []string `json:"processor_script"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, badJSON())
		return
	}
	amountRaw, ok := raw["amount"]
	if !ok {
		writeErr(w, &app.APIError{Status: 400, Type: "invalid_request_error", Code: "parameter_missing", Message: "amount is required"})
		return
	}
	amount, err := money.ParseMinor(amountRaw)
	if err != nil {
		writeErr(w, &app.APIError{Status: 400, Type: "invalid_request_error", Code: "parameter_invalid", Message: err.Error()})
		return
	}
	if in.ProcessorScript == nil {
		in.ProcessorScript = []string{}
	}
	res, err := s.App.CreatePaymentIntent(r.Context(), req, in.AccountID, amount, strings.ToLower(in.Currency), in.ProcessorScript)
	writeResult(w, res, err)
}

func (s *Server) getPaymentIntent(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.GetPaymentIntent(r.Context(), bearer(r), r.PathValue("id"))
	writeResult(w, res, err)
}

func (s *Server) confirmPaymentIntent(w http.ResponseWriter, r *http.Request) {
	body, req, err := readPOST(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
		req.Body = body
	}
	res, err := s.App.ConfirmPaymentIntent(r.Context(), req, r.PathValue("id"))
	writeResult(w, res, err)
}

func (s *Server) capturePaymentIntent(w http.ResponseWriter, r *http.Request) {
	body, req, err := readPOST(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
		req.Body = body
	}
	res, err := s.App.CapturePaymentIntent(r.Context(), req, r.PathValue("id"))
	writeResult(w, res, err)
}

func (s *Server) createRefund(w http.ResponseWriter, r *http.Request) {
	body, req, err := readPOST(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeErr(w, badJSON())
		return
	}
	var in struct {
		PaymentIntentID string `json:"payment_intent_id"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, badJSON())
		return
	}
	amountRaw, ok := raw["amount"]
	if !ok {
		writeErr(w, &app.APIError{Status: 400, Type: "invalid_request_error", Code: "parameter_missing", Message: "amount is required"})
		return
	}
	amount, err := money.ParseMinor(amountRaw)
	if err != nil {
		writeErr(w, &app.APIError{Status: 400, Type: "invalid_request_error", Code: "parameter_invalid", Message: err.Error()})
		return
	}
	res, err := s.App.CreateRefund(r.Context(), req, in.PaymentIntentID, amount)
	writeResult(w, res, err)
}

func (s *Server) getRefund(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.GetRefund(r.Context(), bearer(r), r.PathValue("id"))
	writeResult(w, res, err)
}

func (s *Server) listLedger(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.ListLedgerTransactions(r.Context(), bearer(r), r.PathValue("id"))
	writeResult(w, res, err)
}

func (s *Server) getLedger(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.GetLedgerTransaction(r.Context(), bearer(r), r.PathValue("id"))
	writeResult(w, res, err)
}

func readPOST(r *http.Request) ([]byte, app.Request, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, app.Request{}, err
	}
	if len(body) > maxBody {
		return nil, app.Request{}, &app.APIError{Status: 413, Type: "invalid_request_error", Code: "body_too_large", Message: "request body too large"}
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	req := app.Request{
		Token:          bearer(r),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Method:         r.Method,
		Path:           r.URL.Path,
		Body:           body,
	}
	return body, req, nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func writeResult(w http.ResponseWriter, res app.Result, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.DropResponse {
		// Simulate succeed-but-drop-the-response: effect is committed, client sees a network failure.
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, herr := hj.Hijack()
			if herr == nil {
				_ = conn.Close()
				return
			}
		}
		// Fallback when hijack is unavailable (some test ResponseWriters).
		panic("ledgerd: drop_response")
	}
	w.Header().Set("Content-Type", "application/json")
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func writeErr(w http.ResponseWriter, err error) {
	var api *app.APIError
	if errors.As(err, &api) {
		writeJSON(w, api.Status, map[string]any{
			"error": map[string]string{
				"type":    api.Type,
				"code":    api.Code,
				"message": api.Message,
			},
		})
		return
	}
	writeJSON(w, 500, map[string]any{
		"error": map[string]string{
			"type":    "api_error",
			"code":    "internal",
			"message": "internal error",
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeStrict(body []byte, dest any) error {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return badJSON()
	}
	return nil
}

func badJSON() *app.APIError {
	return &app.APIError{Status: 400, Type: "invalid_request_error", Code: "invalid_json", Message: "request body must be valid JSON"}
}
