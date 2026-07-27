package writerpin

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// leasepin.go — a REAL writer pin, backed by a Kubernetes Lease.
//
// SingleWriter is only correct because replicas:1 makes Kubernetes the elector.
// The moment a second pod exists, something has to decide which one may open the
// RWO stores, and be RIGHT about it: two writers double-open the SQLite stores and
// corrupt the audit chain's in-memory head. That is not a race to lose.
//
// This wraps client-go's leaderelection rather than renewing a Lease by hand. The
// fencing rules — a candidate may only take over an EXPIRED lease, the holder must
// stop the instant renewal fails, clock skew is bounded by RenewDeadline — are
// exactly the ones that are easy to write and hard to write correctly, and this
// implementation is the one Kubernetes itself runs on.
//
// The contract this must honour, from Held: "the holder MUST stop all writes the
// instant Lost() fires". That is why ReleaseOnCancel is set and why OnStoppedLeading
// closes lost — a pin that keeps quiet about losing itself is worse than no pin.
//
// Timings default to the shape k8s controllers use: a 15s lease renewed every 2s
// with a 10s deadline. The holder therefore learns it is out ~5s before the lease
// can be taken, which is the margin that keeps two writers from overlapping.

// LeasePin elects a single writer through a coordination.k8s.io Lease.
type LeasePin struct {
	client    kubernetes.Interface
	namespace string
	name      string
	identity  string

	leaseDuration time.Duration
	renewDeadline time.Duration
	retryPeriod   time.Duration
}

// LeaseOptions configures a LeasePin. Namespace, Name and Identity are required;
// Identity MUST be unique per process (the pod name), because it is what the lease
// records as the holder — two processes sharing an identity would each believe the
// other's renewal was their own.
type LeaseOptions struct {
	Namespace string
	Name      string
	Identity  string

	// LeaseDuration is how long a lease stays valid without renewal — the window a
	// dead holder blocks a takeover. RenewDeadline is how long the holder keeps
	// trying before it declares itself out; it MUST be shorter than LeaseDuration
	// or a holder could still think it leads after another pod took over.
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
}

// NewLeasePin builds a Lease-backed pin. It validates the timings rather than
// accepting a combination that cannot be safe: RenewDeadline >= LeaseDuration is
// the classic split-brain misconfiguration and is refused here, not in production.
func NewLeasePin(client kubernetes.Interface, opts LeaseOptions) (*LeasePin, error) {
	if client == nil {
		return nil, fmt.Errorf("writerpin: nil kubernetes client")
	}
	if opts.Namespace == "" || opts.Name == "" {
		return nil, fmt.Errorf("writerpin: lease namespace and name are required")
	}
	if opts.Identity == "" {
		return nil, fmt.Errorf("writerpin: lease identity is required (use the pod name — a shared identity makes two holders indistinguishable)")
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = 15 * time.Second
	}
	if opts.RenewDeadline <= 0 {
		opts.RenewDeadline = 10 * time.Second
	}
	if opts.RetryPeriod <= 0 {
		opts.RetryPeriod = 2 * time.Second
	}
	if opts.RenewDeadline >= opts.LeaseDuration {
		return nil, fmt.Errorf("writerpin: RenewDeadline (%s) must be shorter than LeaseDuration (%s), else a holder can still believe it leads after another pod has taken the lease",
			opts.RenewDeadline, opts.LeaseDuration)
	}
	if opts.RetryPeriod >= opts.RenewDeadline {
		return nil, fmt.Errorf("writerpin: RetryPeriod (%s) must be shorter than RenewDeadline (%s)", opts.RetryPeriod, opts.RenewDeadline)
	}
	return &LeasePin{
		client:        client,
		namespace:     opts.Namespace,
		name:          opts.Name,
		identity:      opts.Identity,
		leaseDuration: opts.LeaseDuration,
		renewDeadline: opts.RenewDeadline,
		retryPeriod:   opts.RetryPeriod,
	}, nil
}

func (*LeasePin) Kind() string { return "lease(k8s)" }

// Acquire blocks until this process holds the Lease, ctx is done, or the election
// fails. The returned Held loses its pin when renewal fails — at which point the
// caller must stop writing immediately.
func (p *LeasePin) Acquire(ctx context.Context) (Held, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{Namespace: p.namespace, Name: p.name},
		Client:    p.client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: p.identity,
		},
	}

	elected := make(chan struct{})
	h := &leaseHeld{lost: make(chan struct{})}
	runCtx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: p.leaseDuration,
		RenewDeadline: p.renewDeadline,
		RetryPeriod:   p.retryPeriod,
		// The holder gives the lease back on a clean exit instead of making the
		// next writer wait out the full duration — this is what turns a rolling
		// restart from "LeaseDuration of downtime" into a handover.
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(context.Context) { close(elected) },
			// Fires on renewal failure AND on release. Either way the pin is gone
			// and the holder must stop writing, so both close lost.
			OnStoppedLeading: func() { h.markLost() },
		},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("writerpin: build elector: %w", err)
	}

	go func() {
		elector.Run(runCtx)
		// Run returning at all means we are not leading any more, whether or not
		// OnStoppedLeading fired (it does not fire if we never led).
		h.markLost()
	}()

	select {
	case <-elected:
		return h, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-h.lost:
		cancel()
		return nil, fmt.Errorf("writerpin: election ended before this candidate led")
	}
}

// leaseHeld is a Held backed by a running leader election.
type leaseHeld struct {
	once   sync.Once
	lost   chan struct{}
	cancel context.CancelFunc
}

func (h *leaseHeld) Lost() <-chan struct{} { return h.lost }

// Release cancels the election, which (with ReleaseOnCancel) hands the lease back
// so the next writer starts immediately instead of waiting for expiry. Idempotent
// and safe from a defer.
func (h *leaseHeld) Release() {
	if h.cancel != nil {
		h.cancel()
	}
	h.markLost()
}

func (h *leaseHeld) markLost() {
	h.once.Do(func() { close(h.lost) })
}
