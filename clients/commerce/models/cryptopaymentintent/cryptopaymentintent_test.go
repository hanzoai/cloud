package cryptopaymentintent

import (
	"testing"
	"time"
)

func TestMarkConfirming(t *testing.T) {
	cpi := &CryptoPaymentIntent{Status: Pending}

	if err := cpi.MarkConfirming("0xabc", 12345); err != nil {
		t.Fatalf("MarkConfirming failed: %v", err)
	}
	if cpi.Status != Confirming {
		t.Errorf("expected confirming, got %s", cpi.Status)
	}
	if cpi.TxHash != "0xabc" {
		t.Errorf("expected txHash '0xabc', got %s", cpi.TxHash)
	}
	if cpi.BlockNumber != 12345 {
		t.Errorf("expected blockNumber 12345, got %d", cpi.BlockNumber)
	}
}

func TestMarkConfirmingInvalid(t *testing.T) {
	cpi := &CryptoPaymentIntent{Status: Succeeded}
	if err := cpi.MarkConfirming("0x", 1); err == nil {
		t.Error("expected error marking confirmed intent as confirming")
	}
}

func TestAddConfirmation(t *testing.T) {
	cpi := &CryptoPaymentIntent{RequiredConfirmations: 12}

	for i := 0; i < 12; i++ {
		cpi.AddConfirmation()
	}

	if cpi.Confirmations != 12 {
		t.Errorf("expected 12 confirmations, got %d", cpi.Confirmations)
	}
	if !cpi.IsFullyConfirmed() {
		t.Error("expected fully confirmed")
	}
}

func TestIsFullyConfirmed(t *testing.T) {
	cpi := &CryptoPaymentIntent{
		Confirmations:         5,
		RequiredConfirmations: 12,
	}
	if cpi.IsFullyConfirmed() {
		t.Error("expected not fully confirmed")
	}

	cpi.Confirmations = 12
	if !cpi.IsFullyConfirmed() {
		t.Error("expected fully confirmed")
	}
}

func TestMarkSucceeded(t *testing.T) {
	cpi := &CryptoPaymentIntent{
		Status:                Confirming,
		Confirmations:         12,
		RequiredConfirmations: 12,
	}

	if err := cpi.MarkSucceeded(10000, "1.0001"); err != nil {
		t.Fatalf("MarkSucceeded failed: %v", err)
	}
	if cpi.Status != Succeeded {
		t.Errorf("expected succeeded, got %s", cpi.Status)
	}
	if cpi.SettlementAmount != 10000 {
		t.Errorf("expected settlement 10000, got %d", cpi.SettlementAmount)
	}
}

func TestMarkSucceededInsufficientConfirmations(t *testing.T) {
	cpi := &CryptoPaymentIntent{
		Status:                Confirming,
		Confirmations:         5,
		RequiredConfirmations: 12,
	}

	if err := cpi.MarkSucceeded(10000, "1.0"); err == nil {
		t.Error("expected error for insufficient confirmations")
	}
}

func TestMarkSucceededInvalidStatus(t *testing.T) {
	cpi := &CryptoPaymentIntent{Status: Pending}
	if err := cpi.MarkSucceeded(10000, "1.0"); err == nil {
		t.Error("expected error for pending status")
	}
}

func TestMarkExpired(t *testing.T) {
	cpi := &CryptoPaymentIntent{Status: Pending}
	if err := cpi.MarkExpired(); err != nil {
		t.Fatalf("MarkExpired failed: %v", err)
	}
	if cpi.Status != Expired {
		t.Errorf("expected expired, got %s", cpi.Status)
	}
}

func TestMarkExpiredInvalid(t *testing.T) {
	cpi := &CryptoPaymentIntent{Status: Confirming}
	if err := cpi.MarkExpired(); err == nil {
		t.Error("expected error expiring confirming intent")
	}
}

func TestMarkFailed(t *testing.T) {
	cpi := &CryptoPaymentIntent{Status: Confirming}
	if err := cpi.MarkFailed("reorg"); err != nil {
		t.Fatalf("MarkFailed failed: %v", err)
	}
	if cpi.Status != Failed {
		t.Errorf("expected failed, got %s", cpi.Status)
	}
}

func TestMarkFailedInvalid(t *testing.T) {
	cpi := &CryptoPaymentIntent{Status: Succeeded}
	if err := cpi.MarkFailed("test"); err == nil {
		t.Error("expected error failing succeeded intent")
	}

	cpi.Status = Refunded
	if err := cpi.MarkFailed("test"); err == nil {
		t.Error("expected error failing refunded intent")
	}
}

func TestIsExpired(t *testing.T) {
	cpi := &CryptoPaymentIntent{
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	if !cpi.IsExpired() {
		t.Error("expected expired (past time)")
	}

	cpi.ExpiresAt = time.Now().Add(time.Hour)
	if cpi.IsExpired() {
		t.Error("expected not expired (future time)")
	}

	cpi.ExpiresAt = time.Time{}
	if cpi.IsExpired() {
		t.Error("expected not expired (zero time)")
	}
}

func TestRequiredConfirmationsForChain(t *testing.T) {
	tests := []struct {
		chain  Chain
		expect int
	}{
		{Ethereum, 12},
		{Base, 20},
		{Polygon, 20},
		{Arbitrum, 20},
		{Solana, 32},
		{Chain("unknown"), 12},
	}

	for _, tt := range tests {
		if got := RequiredConfirmationsForChain(tt.chain); got != tt.expect {
			t.Errorf("RequiredConfirmationsForChain(%s) = %d, want %d", tt.chain, got, tt.expect)
		}
	}
}
