// Package money handles currency minor units. Amounts are int64. Floats are rejected.
package money

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const MaxMinor int64 = 1_000_000_000_000

var currencies = map[string]struct{}{
	"usd": {},
	"eur": {},
	"gbp": {},
}

// ParseMinor accepts a JSON integer and rejects floats, strings, and exponents.
func ParseMinor(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, fmt.Errorf("amount is required")
	}
	if raw[0] == '"' {
		return 0, fmt.Errorf("amount must be a JSON integer minor unit")
	}
	for _, c := range raw {
		if c == '.' || c == 'e' || c == 'E' {
			return 0, fmt.Errorf("amount must be an integer minor unit, not a float")
		}
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("amount must be a JSON integer minor unit")
	}
	if n <= 0 {
		return 0, fmt.Errorf("amount must be a positive integer minor unit")
	}
	if n > MaxMinor {
		return 0, fmt.Errorf("amount exceeds maximum of %d", MaxMinor)
	}
	return n, nil
}

// Add returns a+b, or false if the sum would overflow int64.
func Add(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// Sum returns the sum of values, or false on overflow.
func Sum(vals ...int64) (int64, bool) {
	var total int64
	for _, v := range vals {
		next, ok := Add(total, v)
		if !ok {
			return 0, false
		}
		total = next
	}
	return total, true
}

// ValidCurrency reports whether currency is an allowed lowercase ISO code.
func ValidCurrency(currency string) bool {
	if utf8.RuneCountInString(currency) != 3 {
		return false
	}
	_, ok := currencies[currency]
	return ok
}
