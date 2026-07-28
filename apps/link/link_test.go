package link

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBillingModeIsTheSubscriptionDistinction: the ONE decision that separates a
// subscription (bills the user's plan — NO commerce charge) from an api-key
// account (bills via commerce). An unknown kind is never silently "free".
func TestBillingModeIsTheSubscriptionDistinction(t *testing.T) {
	if BillingMode(KindSubscription) != BillingPlan {
		t.Fatalf("a subscription must bill the plan, not commerce")
	}
	if BillingMode(KindAPIKey) != BillingCommerce {
		t.Fatalf("an api-key account must bill via commerce")
	}
	if BillingMode("weird") != BillingCommerce {
		t.Fatalf("an unknown kind must default to a real charge, never free")
	}
}

func TestHeadroom(t *testing.T) {
	if headroomPct(nil) != 100 {
		t.Fatalf("no snapshot = full headroom")
	}
	if got := headroomPct(&Usage{SessionPct: 30, WeeklyPct: 70}); got != 30 {
		t.Fatalf("headroom is the tighter window (100-70=30), got %v", got)
	}
	if got := headroomPct(&Usage{SessionPct: 100}); got != 0 {
		t.Fatalf("an exhausted window = 0 headroom, got %v", got)
	}
	if got := headroomPct(&Usage{SessionPct: 150}); got != 0 {
		t.Fatalf("over-100 used clamps headroom to 0, got %v", got)
	}
	if got := headroomPct(&Usage{SessionPct: -20}); got != 100 {
		t.Fatalf("a nonsense negative used clamps to full headroom, got %v", got)
	}
}

// TestNormalizeUsage: absent → "" (heartbeat), valid → clamped + re-marshalled,
// malformed / oversized → an error (the collector owns a valid projection).
func TestNormalizeUsage(t *testing.T) {
	if out, err := normalizeUsage(nil); err != nil || out != "" {
		t.Fatalf("absent usage → empty, got %q %v", out, err)
	}
	if out, err := normalizeUsage(json.RawMessage("null")); err != nil || out != "" {
		t.Fatalf("null usage → empty, got %q %v", out, err)
	}
	out, err := normalizeUsage(json.RawMessage(`{"sessionPct":150,"weeklyPct":-5,"tokens":42}`))
	if err != nil {
		t.Fatalf("valid usage: %v", err)
	}
	var u Usage
	_ = json.Unmarshal([]byte(out), &u)
	if u.SessionPct != 100 || u.WeeklyPct != 0 || u.Tokens != 42 {
		t.Fatalf("usage must be clamped [0,100] and preserved, got %+v", u)
	}
	if _, err := normalizeUsage(json.RawMessage(`{not json`)); err == nil {
		t.Fatalf("malformed usage must error")
	}
	big := json.RawMessage(`{"resetsAt":"` + strings.Repeat("x", maxUsage) + `"}`)
	if _, err := normalizeUsage(big); err == nil {
		t.Fatalf("oversized usage must error")
	}
}

// TestDevicesOf groups a user's links by machine (device projection), newest
// device first, carrying each machine's accounts.
func TestDevicesOf(t *testing.T) {
	links := []Link{
		{ID: "1", Machine: "m1", Host: "h1", Provider: "claude", Kind: KindSubscription, LastSeen: 200},
		{ID: "2", Machine: "m1", Host: "h1", Provider: "codex", Kind: KindSubscription, LastSeen: 190},
		{ID: "3", Machine: "m2", Host: "h2", Provider: "hanzo", Kind: KindAPIKey, LastSeen: 180},
	}
	ds := devicesOf(links)
	if len(ds) != 2 {
		t.Fatalf("want 2 devices, got %d", len(ds))
	}
	if ds[0].Machine != "m1" || len(ds[0].Accounts) != 2 {
		t.Fatalf("m1 groups its 2 accounts, got %+v", ds[0])
	}
	if ds[1].Machine != "m2" || len(ds[1].Accounts) != 1 {
		t.Fatalf("m2 has 1 account, got %+v", ds[1])
	}
	// The account views carry the derived billing mode.
	if ds[0].Accounts[0].Billing != BillingPlan || ds[1].Accounts[0].Billing != BillingCommerce {
		t.Fatalf("device views must carry each account's billing mode")
	}
}
