package app

import "fmt"

// APIError is a client-visible failure. The idempotency wrapper does not store it:
// returning an APIError rolls the transaction back, so the key can be reused.
type APIError struct {
	Status  int
	Type    string
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func invalid(code, message string) *APIError {
	return &APIError{Status: 400, Type: "invalid_request_error", Code: code, Message: message}
}

func notFound(message string) *APIError {
	return &APIError{Status: 404, Type: "invalid_request_error", Code: "not_found", Message: message}
}

func unauthorized() *APIError {
	return &APIError{Status: 401, Type: "authentication_error", Code: "unauthorized", Message: "invalid API key"}
}
