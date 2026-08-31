package writerpin

import (
	"context"
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

func TestResolveDefaultsToSingleWriter(t *testing.T) {
	pin, _ := ResolveWithReason()
	if pin.Kind() != "single-writer" {
		t.Fatalf("the default pin should be single-writer")
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
		"lease off": {},
		"lease on":  {"CLOUD_WRITER_LEASE": "1", "POD_NAMESPACE": "hanzo", "POD_NAME": "cloud-0"},
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
