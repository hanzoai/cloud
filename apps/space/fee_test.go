package space

import "testing"

// An operation is a request, so it is free until an operator prices it — never the
// $1.00 a resource costs to create.
func TestAnOperationIsFreeByDefault(t *testing.T) {
	t.Setenv(feeEnv, "")
	if kind, cents := (state{}).Fee(); kind != "op" || cents != 0 {
		t.Fatalf("Fee() = %s %d, want op 0", kind, cents)
	}
	t.Setenv(feeEnv, "3")
	if _, cents := (state{}).Fee(); cents != 3 {
		t.Fatalf("priced Fee() = %d, want 3", cents)
	}
}
