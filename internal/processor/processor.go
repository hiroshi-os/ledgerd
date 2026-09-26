// Package processor is a deterministic, seedable stand-in for a card processor.
package processor

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

type Outcome string

const (
	OutcomeSucceed Outcome = "succeed"
	OutcomeDecline Outcome = "decline"
	OutcomeTimeout Outcome = "timeout"
	OutcomeDrop    Outcome = "drop"
)

// Decide picks an outcome. A non-empty script wins: attempt 1 uses script[0],
// and attempts past the end repeat the last entry. Otherwise the choice is a
// pure function of seed, payment intent id, and attempt number.
func Decide(seed int64, paymentIntentID uuid.UUID, attempt int, script []string) (Outcome, error) {
	if attempt < 1 {
		return "", fmt.Errorf("attempt must be >= 1")
	}
	if len(script) > 0 {
		idx := attempt - 1
		if idx >= len(script) {
			idx = len(script) - 1
		}
		o := Outcome(script[idx])
		if !known(o) {
			return "", fmt.Errorf("unknown processor outcome %q", script[idx])
		}
		return o, nil
	}
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(seed))
	h.Write(buf[:])
	h.Write(paymentIntentID[:])
	binary.BigEndian.PutUint64(buf[:], uint64(attempt))
	h.Write(buf[:])
	sum := h.Sum(nil)
	n := binary.BigEndian.Uint64(sum[:8]) % 100
	switch {
	case n < 5:
		return OutcomeDecline, nil
	case n < 8:
		return OutcomeTimeout, nil
	case n < 9:
		return OutcomeDrop, nil
	default:
		return OutcomeSucceed, nil
	}
}

func known(o Outcome) bool {
	switch o {
	case OutcomeSucceed, OutcomeDecline, OutcomeTimeout, OutcomeDrop:
		return true
	default:
		return false
	}
}

// ValidScript reports whether every entry is a known outcome.
func ValidScript(script []string) bool {
	if len(script) > 8 {
		return false
	}
	for _, s := range script {
		if !known(Outcome(s)) {
			return false
		}
	}
	return true
}

// Ref is stable for a payment intent so retries observe the same processor reference.
func Ref(paymentIntentID uuid.UUID) string {
	sum := sha256.Sum256(paymentIntentID[:])
	return "proc_" + hex.EncodeToString(sum[:16])
}
