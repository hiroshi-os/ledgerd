package money

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseMinor(t *testing.T) {
	n, err := ParseMinor(json.RawMessage("2500"))
	if err != nil || n != 2500 {
		t.Fatalf("got %d %v", n, err)
	}
	for _, raw := range []string{"10.5", "1e2", `"10"`, "0", "-5", "null", "true"} {
		if _, err := ParseMinor(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected reject %s", raw)
		}
	}
	if _, err := ParseMinor(json.RawMessage("1000000000001")); err == nil {
		t.Fatal("expected max reject")
	}
}

func TestAddOverflow(t *testing.T) {
	if _, ok := Add(math.MaxInt64, 1); ok {
		t.Fatal("expected overflow")
	}
	if _, ok := Add(math.MinInt64, -1); ok {
		t.Fatal("expected underflow")
	}
	got, ok := Add(40, -15)
	if !ok || got != 25 {
		t.Fatalf("got %d %v", got, ok)
	}
	total, ok := Sum(10, -4, -6)
	if !ok || total != 0 {
		t.Fatalf("sum %d %v", total, ok)
	}
}

func TestValidCurrency(t *testing.T) {
	if !ValidCurrency("usd") || ValidCurrency("USD") || ValidCurrency("us") || ValidCurrency("jpy") {
		t.Fatal("currency gate")
	}
}
