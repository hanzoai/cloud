package cryptobalance

import "testing"

func newBalance(bal, reserved string) *CryptoBalance {
	return &CryptoBalance{
		Balance:  bal,
		Reserved: reserved,
	}
}

func TestAvailable(t *testing.T) {
	cb := newBalance("1000000", "200000")
	avail, err := cb.Available()
	if err != nil {
		t.Fatal(err)
	}
	if avail.String() != "800000" {
		t.Errorf("expected 800000, got %s", avail.String())
	}
}

func TestAvailableNoReserved(t *testing.T) {
	cb := newBalance("5000", "")
	avail, err := cb.Available()
	if err != nil {
		t.Fatal(err)
	}
	if avail.String() != "5000" {
		t.Errorf("expected 5000, got %s", avail.String())
	}
}

func TestCredit(t *testing.T) {
	cb := newBalance("1000", "0")
	if err := cb.Credit("500"); err != nil {
		t.Fatal(err)
	}
	if cb.Balance != "1500" {
		t.Errorf("expected 1500, got %s", cb.Balance)
	}
}

func TestCreditInvalid(t *testing.T) {
	cb := newBalance("1000", "0")

	if err := cb.Credit("not-a-number"); err == nil {
		t.Error("expected error for invalid amount")
	}

	if err := cb.Credit("-100"); err == nil {
		t.Error("expected error for negative amount")
	}

	if err := cb.Credit("0"); err == nil {
		t.Error("expected error for zero amount")
	}
}

func TestDebit(t *testing.T) {
	cb := newBalance("1000", "0")
	if err := cb.Debit("300"); err != nil {
		t.Fatal(err)
	}
	if cb.Balance != "700" {
		t.Errorf("expected 700, got %s", cb.Balance)
	}
}

func TestDebitInsufficient(t *testing.T) {
	cb := newBalance("500", "200")
	if err := cb.Debit("400"); err == nil {
		t.Error("expected error for insufficient balance (300 available, 400 debit)")
	}
}

func TestDebitInvalid(t *testing.T) {
	cb := newBalance("1000", "0")

	if err := cb.Debit("not-a-number"); err == nil {
		t.Error("expected error for invalid amount")
	}

	if err := cb.Debit("-100"); err == nil {
		t.Error("expected error for negative amount")
	}
}

func TestReserve(t *testing.T) {
	cb := newBalance("1000", "0")
	if err := cb.Reserve("400"); err != nil {
		t.Fatal(err)
	}
	if cb.Reserved != "400" {
		t.Errorf("expected reserved 400, got %s", cb.Reserved)
	}

	// Reserve more
	if err := cb.Reserve("300"); err != nil {
		t.Fatal(err)
	}
	if cb.Reserved != "700" {
		t.Errorf("expected reserved 700, got %s", cb.Reserved)
	}
}

func TestReserveInsufficient(t *testing.T) {
	cb := newBalance("1000", "800")
	if err := cb.Reserve("300"); err == nil {
		t.Error("expected error for insufficient available balance")
	}
}

func TestReserveInvalid(t *testing.T) {
	cb := newBalance("1000", "0")
	if err := cb.Reserve("not-a-number"); err == nil {
		t.Error("expected error for invalid amount")
	}
}

func TestRelease(t *testing.T) {
	cb := newBalance("1000", "500")
	if err := cb.Release("200"); err != nil {
		t.Fatal(err)
	}
	if cb.Reserved != "300" {
		t.Errorf("expected reserved 300, got %s", cb.Reserved)
	}
}

func TestReleaseExceeds(t *testing.T) {
	cb := newBalance("1000", "200")
	if err := cb.Release("300"); err == nil {
		t.Error("expected error for release exceeding reserved")
	}
}

func TestReleaseInvalid(t *testing.T) {
	cb := newBalance("1000", "500")
	if err := cb.Release("not-a-number"); err == nil {
		t.Error("expected error for invalid amount")
	}
}

func TestIsZero(t *testing.T) {
	tests := []struct {
		balance string
		zero    bool
	}{
		{"0", true},
		{"", true},
		{"1", false},
		{"1000000000000000000", false},
	}

	for _, tt := range tests {
		cb := &CryptoBalance{Balance: tt.balance}
		if got := cb.IsZero(); got != tt.zero {
			t.Errorf("IsZero(%q) = %v, want %v", tt.balance, got, tt.zero)
		}
	}
}

func TestCreditFromZero(t *testing.T) {
	cb := &CryptoBalance{Balance: ""}
	if err := cb.Credit("100"); err != nil {
		t.Fatal(err)
	}
	if cb.Balance != "100" {
		t.Errorf("expected 100, got %s", cb.Balance)
	}
}
