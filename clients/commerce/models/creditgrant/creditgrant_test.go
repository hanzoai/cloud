package creditgrant

import (
	"testing"
	"time"
)

func TestIsActive_ActiveGrant(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: 500,
		EffectiveAt:    time.Now().Add(-1 * time.Hour),
		ExpiresAt:      time.Now().Add(24 * time.Hour),
		Voided:         false,
	}
	if !g.IsActive() {
		t.Error("expected active grant to return true")
	}
}

func TestIsActive_VoidedGrant(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: 500,
		EffectiveAt:    time.Now().Add(-1 * time.Hour),
		Voided:         true,
	}
	if g.IsActive() {
		t.Error("expected voided grant to be inactive")
	}
}

func TestIsActive_ExhaustedGrant(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: 0,
		EffectiveAt:    time.Now().Add(-1 * time.Hour),
		Voided:         false,
	}
	if g.IsActive() {
		t.Error("expected exhausted grant (0 remaining) to be inactive")
	}
}

func TestIsActive_NegativeRemaining(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: -10,
		EffectiveAt:    time.Now().Add(-1 * time.Hour),
		Voided:         false,
	}
	if g.IsActive() {
		t.Error("expected negative remaining grant to be inactive")
	}
}

func TestIsActive_FutureEffective(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: 500,
		EffectiveAt:    time.Now().Add(24 * time.Hour),
		Voided:         false,
	}
	if g.IsActive() {
		t.Error("expected future effective grant to be inactive")
	}
}

func TestIsActive_ExpiredGrant(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: 500,
		EffectiveAt:    time.Now().Add(-48 * time.Hour),
		ExpiresAt:      time.Now().Add(-1 * time.Hour),
		Voided:         false,
	}
	if g.IsActive() {
		t.Error("expected expired grant to be inactive")
	}
}

func TestIsActive_NoExpiryDate(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: 500,
		EffectiveAt:    time.Now().Add(-1 * time.Hour),
		ExpiresAt:      time.Time{}, // Zero value = no expiry
		Voided:         false,
	}
	if !g.IsActive() {
		t.Error("expected grant with no expiry to be active")
	}
}

func TestIsActive_ExactlyAtEffective(t *testing.T) {
	now := time.Now()
	g := &CreditGrant{
		RemainingCents: 500,
		EffectiveAt:    now.Add(-1 * time.Millisecond),
		Voided:         false,
	}
	if !g.IsActive() {
		t.Error("expected grant effective at current time to be active")
	}
}

func TestIsEligibleForMeter_EmptyList(t *testing.T) {
	g := &CreditGrant{
		Eligibility: nil,
	}
	if !g.IsEligibleForMeter("any-meter") {
		t.Error("expected empty eligibility to match all meters")
	}
}

func TestIsEligibleForMeter_EmptySlice(t *testing.T) {
	g := &CreditGrant{
		Eligibility: []string{},
	}
	if !g.IsEligibleForMeter("any-meter") {
		t.Error("expected empty slice eligibility to match all meters")
	}
}

func TestIsEligibleForMeter_Matching(t *testing.T) {
	g := &CreditGrant{
		Eligibility: []string{"meter-a", "meter-b", "meter-c"},
	}
	if !g.IsEligibleForMeter("meter-b") {
		t.Error("expected matching meter to be eligible")
	}
}

func TestIsEligibleForMeter_NotMatching(t *testing.T) {
	g := &CreditGrant{
		Eligibility: []string{"meter-a", "meter-b"},
	}
	if g.IsEligibleForMeter("meter-c") {
		t.Error("expected non-matching meter to be ineligible")
	}
}

func TestIsEligibleForMeter_SingleItem(t *testing.T) {
	g := &CreditGrant{
		Eligibility: []string{"meter-x"},
	}

	if !g.IsEligibleForMeter("meter-x") {
		t.Error("expected single matching meter to be eligible")
	}
	if g.IsEligibleForMeter("meter-y") {
		t.Error("expected non-matching meter to be ineligible")
	}
}

func TestIsEligibleForMeter_EmptyMeterId(t *testing.T) {
	g := &CreditGrant{
		Eligibility: []string{"meter-a"},
	}
	if g.IsEligibleForMeter("") {
		t.Error("expected empty meter ID to not match specific eligibility")
	}
}

func TestIsActive_AllConditionsInvalid(t *testing.T) {
	g := &CreditGrant{
		RemainingCents: 0,
		EffectiveAt:    time.Now().Add(24 * time.Hour),
		ExpiresAt:      time.Now().Add(-1 * time.Hour),
		Voided:         true,
	}
	if g.IsActive() {
		t.Error("expected grant with all invalid conditions to be inactive")
	}
}
