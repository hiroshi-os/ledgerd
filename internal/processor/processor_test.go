package processor

import (
	"testing"

	"github.com/google/uuid"
)

func TestDecideStable(t *testing.T) {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	a, err := Decide(7, id, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Decide(7, id, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("not deterministic: %s vs %s", a, b)
	}
}

func TestDecideScript(t *testing.T) {
	id := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	script := []string{"timeout", "succeed"}
	first, err := Decide(1, id, 1, script)
	if err != nil || first != OutcomeTimeout {
		t.Fatalf("first %s %v", first, err)
	}
	second, err := Decide(1, id, 2, script)
	if err != nil || second != OutcomeSucceed {
		t.Fatalf("second %s %v", second, err)
	}
	third, err := Decide(1, id, 3, script)
	if err != nil || third != OutcomeSucceed {
		t.Fatalf("repeat last %s %v", third, err)
	}
}

func TestRefStable(t *testing.T) {
	id := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	if Ref(id) != Ref(id) {
		t.Fatal("ref unstable")
	}
}
