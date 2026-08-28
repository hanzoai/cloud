package entitlement

// The read the refusal makes, against the real store — the end a fake peer stands
// in for, so the chain store → holds → plane has no untested link.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/plane"
)

// Keyed by both columns, so one org's purchase cannot answer for another's.
func TestHoldsIsPerOrgAndPerProduct(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.Enable(ctx, "acme", "crm", "z@acme.test", 1); err != nil {
		t.Fatalf("enable: %v", err)
	}

	for _, c := range []struct {
		org, product string
		want         bool
	}{
		{"acme", "crm", true},
		{"acme", "team", false},    // a product this org never enabled
		{"initech", "crm", false},  // another org's enablement is not this org's
		{"initech", "team", false}, // neither column matches
	} {
		got, err := s.Holds(ctx, c.org, c.product)
		if err != nil {
			t.Fatalf("Holds(%s,%s): %v", c.org, c.product, err)
		}
		if got != c.want {
			t.Errorf("Holds(%s,%s) = %v, want %v", c.org, c.product, got, c.want)
		}
	}
}

// Disabling turns it back off: the switch is a switch, not a one-way grant.
func TestHoldsFollowsDisable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.Enable(ctx, "acme", "crm", "z@acme.test", 1); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := s.Disable(ctx, "acme", "crm"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err := s.Holds(ctx, "acme", "crm")
	if err != nil {
		t.Fatalf("Holds: %v", err)
	}
	if got {
		t.Error("Holds = true after Disable")
	}
}

// An unmounted store errors rather than answering false: both refuse, but only
// the error tells an operator the subsystem did not start.
func TestHoldsWithoutAStoreIsAnError(t *testing.T) {
	prev := mounted
	mounted = nil
	t.Cleanup(func() { mounted = prev })

	ctx := plane.For(context.Background(), "acme")
	_, err := holds(ctx, &plane.ProductIn{Product: "crm"})
	if err == nil {
		t.Fatal("holds answered with no store mounted — an unstarted subsystem reads as a customer decision")
	}
}

// No org is refused rather than defaulted; the subject is not an input.
func TestHoldsNeedsACaller(t *testing.T) {
	prev := mounted
	mounted = &service{store: openTestStore(t)}
	t.Cleanup(func() { mounted = prev })

	if _, err := holds(context.Background(), &plane.ProductIn{Product: "crm"}); err == nil {
		t.Error("holds answered a call with no org")
	}
	if _, err := holds(plane.For(context.Background(), "acme"), &plane.ProductIn{Product: "  "}); err == nil {
		t.Error("holds answered a call naming no product")
	}
	// The happy path through the handler.
	if err := mounted.store.Enable(context.Background(), "acme", "crm", "", 1); err != nil {
		t.Fatalf("enable: %v", err)
	}
	out, err := holds(plane.For(context.Background(), "acme"), &plane.ProductIn{Product: "crm"})
	if err != nil || out == nil || !out.On {
		t.Errorf("holds(acme, crm) = %+v, %v — the org enabled it and was told no", out, err)
	}
}
