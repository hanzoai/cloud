package writerpin

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

// The pin's whole job is that two writers never overlap, because two writers
// double-open the SQLite stores and corrupt the audit chain. These tests assert
// that property and the misconfigurations that would quietly break it — not that
// the wrapper compiles.

func testOpts(identity string) LeaseOptions {
	return LeaseOptions{
		Namespace:     "hanzo",
		Name:          "cloud-writer",
		Identity:      identity,
		LeaseDuration: 2 * time.Second,
		RenewDeadline: 1 * time.Second,
		RetryPeriod:   200 * time.Millisecond,
	}
}

// THE property: with two candidates on one lease, exactly one leads.
func TestLeasePin_OnlyOneHolderAtATime(t *testing.T) {
	client := fake.NewSimpleClientset()

	a, err := NewLeasePin(client, testOpts("pod-a"))
	if err != nil {
		t.Fatalf("pin a: %v", err)
	}
	b, err := NewLeasePin(client, testOpts("pod-b"))
	if err != nil {
		t.Fatalf("pin b: %v", err)
	}

	ctxA, cancelA := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelA()
	heldA, err := a.Acquire(ctxA)
	if err != nil {
		t.Fatalf("pod-a must win an uncontended lease: %v", err)
	}
	defer heldA.Release()

	// b must NOT be able to acquire while a holds it.
	ctxB, cancelB := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancelB()
	if _, err := b.Acquire(ctxB); err == nil {
		t.Fatal("pod-b acquired the pin while pod-a held it — two writers, corrupt stores")
	}

	select {
	case <-heldA.Lost():
		t.Fatal("pod-a lost its pin while it was still renewing")
	default:
	}
}

// Release hands the lease back so the next writer starts immediately rather than
// waiting out the full duration — this is what makes a restart a handover.
func TestLeasePin_ReleaseLetsTheNextWriterIn(t *testing.T) {
	client := fake.NewSimpleClientset()

	a, _ := NewLeasePin(client, testOpts("pod-a"))
	b, _ := NewLeasePin(client, testOpts("pod-b"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	heldA, err := a.Acquire(ctx)
	if err != nil {
		t.Fatalf("pod-a acquire: %v", err)
	}
	heldA.Release()

	select {
	case <-heldA.Lost():
	case <-time.After(2 * time.Second):
		t.Fatal("Release must make Lost() fire — the holder has to know to stop writing")
	}

	ctxB, cancelB := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelB()
	heldB, err := b.Acquire(ctxB)
	if err != nil {
		t.Fatalf("pod-b must take the released lease: %v", err)
	}
	heldB.Release()
}

// Release is idempotent and safe from a defer; a double Release must not panic on
// a closed channel.
func TestLeasePin_ReleaseIsIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	p, _ := NewLeasePin(client, testOpts("pod-a"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	held, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	held.Release()
	held.Release() // must not panic
}

// RenewDeadline >= LeaseDuration is the classic split-brain misconfiguration: the
// holder can still believe it leads after another pod has taken the lease. Refuse
// it at construction rather than in production.
func TestLeasePin_RefusesUnsafeTimings(t *testing.T) {
	client := fake.NewSimpleClientset()

	bad := testOpts("pod-a")
	bad.RenewDeadline = bad.LeaseDuration
	if _, err := NewLeasePin(client, bad); err == nil {
		t.Fatal("RenewDeadline == LeaseDuration must be refused (split-brain window)")
	}

	bad = testOpts("pod-a")
	bad.RetryPeriod = bad.RenewDeadline
	if _, err := NewLeasePin(client, bad); err == nil {
		t.Fatal("RetryPeriod == RenewDeadline must be refused")
	}
}

// A shared identity makes two holders indistinguishable on the lease, so each
// would read the other's renewal as its own. Required, not defaulted.
func TestLeasePin_RequiresIdentityAndTarget(t *testing.T) {
	client := fake.NewSimpleClientset()

	for name, mutate := range map[string]func(*LeaseOptions){
		"no identity":  func(o *LeaseOptions) { o.Identity = "" },
		"no namespace": func(o *LeaseOptions) { o.Namespace = "" },
		"no name":      func(o *LeaseOptions) { o.Name = "" },
	} {
		o := testOpts("pod-a")
		mutate(&o)
		if _, err := NewLeasePin(client, o); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	if _, err := NewLeasePin(nil, testOpts("pod-a")); err == nil {
		t.Fatal("a nil client must be refused")
	}
}

// A cancelled context must not yield a pin — an Acquire that ignores cancellation
// hands out a pin nobody is renewing.
func TestLeasePin_RespectsCancelledContext(t *testing.T) {
	client := fake.NewSimpleClientset()
	p, _ := NewLeasePin(client, testOpts("pod-a"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Acquire(ctx); err == nil {
		t.Fatal("Acquire must refuse a cancelled context")
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
