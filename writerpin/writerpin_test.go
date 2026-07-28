package writerpin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSingleWriterHeldImmediatelyAndNeverLost(t *testing.T) {
	p := NewSingleWriter()
	if p.Kind() != "single-writer" {
		t.Fatalf("kind=%q", p.Kind())
	}
	h, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	select {
	case <-h.Lost():
		t.Fatal("single-writer pin reported Lost before Release")
	case <-time.After(20 * time.Millisecond):
		// expected: never lost
	}
	h.Release()
	select {
	case <-h.Lost():
		// expected: Lost fires after Release
	case <-time.After(time.Second):
		t.Fatal("Lost did not fire after Release")
	}
	h.Release() // idempotent, must not panic
}

func TestSingleWriterRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSingleWriter().Acquire(ctx); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestConsensusPinFailsClosed(t *testing.T) {
	p := NewConsensusPin()
	h, err := p.Acquire(context.Background())
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
	if h != nil {
		t.Fatal("ConsensusPin must not hand out a Held it cannot back")
	}
}

func TestResolveDefaultsToSingleWriter(t *testing.T) {
	if Resolve().Kind() != "single-writer" {
		t.Fatalf("Resolve should default to single-writer until consensus is wired")
	}
}

// The opt-in switch itself: only explicit truthy values arm the lease.
func TestResolve_OptInIsExplicit(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		if !truthy(v) {
			t.Fatalf("%q must arm the lease", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		if truthy(v) {
			t.Fatalf("%q must NOT arm the lease", v)
		}
	}
}

// Resolve must never half-configure an election: every incomplete combination
// falls back to SingleWriter and SAYS SO, because a silent fallback is how a
// cluster ends up believing it elects when it does not.
func TestResolve_FallsBackAndExplains(t *testing.T) {
	cases := map[string]map[string]string{
		"lease off":      {},
		"no downward":    {"CLOUD_WRITER_LEASE": "1"},
		"no pod name":    {"CLOUD_WRITER_LEASE": "1", "POD_NAMESPACE": "hanzo"},
		"no namespace":   {"CLOUD_WRITER_LEASE": "1", "POD_NAME": "cloud-0"},
		"not in-cluster": {"CLOUD_WRITER_LEASE": "1", "POD_NAMESPACE": "hanzo", "POD_NAME": "cloud-0"},
	}
	for name, env := range cases {
		pin, reason := resolve(func(k string) string { return env[k] })
		if pin.Kind() != "single-writer" {
			t.Fatalf("%s: kind = %q, want single-writer", name, pin.Kind())
		}
		if reason == "" {
			t.Fatalf("%s: fallback gave no reason", name)
		}
	}
}

// The seam itself: with an elector registered, resolve USES it — and without one,
// a binary that asked for a lease is told plainly that it cannot have one rather
// than silently running as a single writer it did not choose.
func TestResolve_UsesRegisteredElector(t *testing.T) {
	t.Cleanup(func() { UseElector(nil) })

	full := map[string]string{"CLOUD_WRITER_LEASE": "1", "POD_NAMESPACE": "hanzo", "POD_NAME": "cloud-0"}
	getenv := func(k string) string { return full[k] }

	// No elector: fall back, and the reason must name the missing piece so nobody
	// goes looking for a cluster problem that is really a link-time one.
	UseElector(nil)
	pin, reason := resolve(getenv)
	if pin.Kind() != "single-writer" {
		t.Fatalf("kind = %q, want single-writer with no elector", pin.Kind())
	}
	if !strings.Contains(reason, "no elector") {
		t.Fatalf("reason = %q, want it to name the unregistered elector", reason)
	}

	// Registered: resolve hands off, passing through the identity it resolved.
	var gotNS, gotLease, gotID string
	UseElector(func(ns, lease, id string) (Pin, string, error) {
		gotNS, gotLease, gotID = ns, lease, id
		return NewConsensusPin(), "elected", nil
	})
	pin, reason = resolve(getenv)
	if pin.Kind() == "single-writer" {
		t.Fatal("a registered elector was not used")
	}
	if reason != "elected" {
		t.Fatalf("reason = %q, want the elector's own", reason)
	}
	if gotNS != "hanzo" || gotID != "cloud-0" || gotLease != "cloud-writer" {
		t.Fatalf("elector got (%q,%q,%q), want (hanzo, cloud-writer, cloud-0)", gotNS, gotLease, gotID)
	}

	// An elector that fails must degrade with its reason, never panic or elect.
	UseElector(func(string, string, string) (Pin, string, error) {
		return nil, "", errors.New("api server unreachable")
	})
	pin, reason = resolve(getenv)
	if pin.Kind() != "single-writer" {
		t.Fatal("a failing elector must fall back to single-writer")
	}
	if !strings.Contains(reason, "api server unreachable") {
		t.Fatalf("reason = %q, want the elector's error", reason)
	}
}
