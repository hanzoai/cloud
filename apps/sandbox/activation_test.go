package sandbox

// activation_test.go — every door to a running sandbox says how the session was
// made ready (cold, relocated, warm), only the call that did the work says it, and
// none of it changes what a retry or a relocation does: a resumed session asked
// for again starts nothing, and a command its relocation interrupted is not run
// again by anyone but its caller.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/activation"
	"github.com/hanzoai/account"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// activations installs a span recorder for one test and returns what it has seen
// of activations so far, oldest first.
func activations(t *testing.T) func() []sdktrace.ReadOnlySpan {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })
	return func() []sdktrace.ReadOnlySpan {
		var out []sdktrace.ReadOnlySpan
		for _, s := range sr.Ended() {
			if s.Name() == activation.Span {
				out = append(out, s)
			}
		}
		return out
	}
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	for _, kv := range s.Attributes() {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

// activationKinds is each activation as "<kind>/<resumed>".
func activationKinds(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		a := spanAttrs(s)
		out = append(out, a[activation.AttrKind]+"/"+a[activation.AttrResumed])
	}
	return out
}

// podCreates is how many pods the fake cluster has been asked to create.
func podCreates(s *Service) int {
	n := 0
	for _, a := range s.State.rt.dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
			n++
		}
	}
	return n
}

// A session's life through every door: a new lease is cold, asking for it again
// while it runs is warm, its pod lost under it comes back relocated, and a resume
// after a park is cold again — this time a session that already existed. Each is
// one span, for this sandbox, in this org, and none is a model call.
func TestEachDoorSaysHowTheSessionWasMadeReady(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		seen := activations(t)
		s, _ := lostService(t)
		ctx := context.Background()
		payer := account.Org("acme")

		m, err := Lease(s, ctx, "acme", payer, false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		if _, err := Lease(s, ctx, "acme", payer, false, "", Spec{ID: m.ID}); err != nil {
			t.Fatalf("lease again: %v", err)
		}
		evict(t, s, m)
		if _, err := Lease(s, ctx, "acme", payer, false, "", Spec{ID: m.ID}); err != nil {
			t.Fatalf("lease a lost pod: %v", err)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatalf("park: %v", err)
		}
		if _, err := Lease(s, ctx, "acme", payer, false, "", Spec{ID: m.ID}); err != nil {
			t.Fatalf("resume: %v", err)
		}

		got := seen()
		want := []string{"cold/false", "warm/true", "relocated/true", "cold/true"}
		if strings.Join(activationKinds(got), " ") != strings.Join(want, " ") {
			t.Fatalf("activations = %v, want %v", activationKinds(got), want)
		}
		for i, sp := range got {
			a := spanAttrs(sp)
			if a["hanzo.sandbox.id"] != m.ID || a["hanzo.org"] != "acme" || a[activation.AttrSurface] != "sandbox" {
				t.Errorf("activation %d is not this session's: %v", i, a)
			}
			for k := range a {
				if strings.HasPrefix(k, "gen_ai.") {
					t.Errorf("activation %d carries %s: a reader of model calls would count it", i, k)
				}
			}
			if sp.Status().Code == codes.Error {
				t.Errorf("activation %d succeeded and is marked failed", i)
			}
		}
	})
}

// A lease refused for what it asked — a class nobody runs, no org — never looked
// at compute, so it is not an activation, fast or otherwise.
func TestARefusedLeaseIsNotAnActivation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		seen := activations(t)
		s, _ := lostService(t)
		ctx := context.Background()
		if _, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "mainframe"}); httpStatus(err) != http.StatusBadRequest {
			t.Fatalf("an unknown class answered %d %v, want 400", httpStatus(err), err)
		}
		if _, err := Lease(s, ctx, "", account.Org("acme"), false, "", Spec{Class: "exec"}); httpStatus(err) != http.StatusForbidden {
			t.Fatalf("no org answered %d %v, want 403", httpStatus(err), err)
		}
		if got := seen(); len(got) != 0 {
			t.Fatalf("refusals recorded activations: %v", activationKinds(got))
		}
	})
}

// Two calls that find the same lost pod relocate it once, and so record ONE
// relocation: the call that lost the compare-and-set started nothing, and a
// second span for it would double the relocations a board shows.
func TestOnlyTheCallThatRelocatesRecordsIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		seen := activations(t)
		s, _ := lostService(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		evict(t, s, m)
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		lost := errors.New(`unable to upgrade connection: container not found ("sandbox")`)
		_ = fault(s, ctx, st, m, "exec", lost)
		_ = fault(s, ctx, st, m, "exec", lost) // the same stale read, after the first moved the row

		want := []string{"cold/false", "relocated/true"}
		if got := activationKinds(seen()); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("activations = %v, want %v", got, want)
		}
	})
}

// A client that resumes a parked session and, not hearing back, asks again gets
// the same session twice and ONE pod: the retry finds it running and is warm. The
// row is not leased a second time — its pod, its clock and its lease are the ones
// the first resume set — and resumes that race each other start one pod between
// them (TestResumesThatRaceStartOnePod), so a retry anywhere starts nothing twice.
func TestARetriedResumeStartsNothingTwice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		seen := activations(t)
		s, _, _ := parking(t)
		ctx := context.Background()
		payer := account.Org("acme")
		m, err := Lease(s, ctx, "acme", payer, false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatal(err)
		}
		before := podCreates(s)
		first, err := Lease(s, ctx, "acme", payer, false, "", Spec{ID: m.ID})
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if n := podCreates(s) - before; n != 1 {
			t.Fatalf("a resume created %d pods, want 1", n)
		}
		retry, err := Lease(s, ctx, "acme", payer, false, "", Spec{ID: m.ID})
		if err != nil {
			t.Fatalf("retried resume: %v", err)
		}
		if n := podCreates(s) - before; n != 1 {
			t.Fatalf("a retried resume created another pod (%d in all)", n)
		}
		if retry.ID != first.ID || retry.Pod != first.Pod || retry.CreatedAt != first.CreatedAt || retry.ExpiresAt != first.ExpiresAt {
			t.Fatalf("the retry leased the session again: first %+v, retry %+v", first, retry)
		}
		if got := pods(t, s, m.ID); got != 1 {
			t.Fatalf("%d pods hold one session", got)
		}
		want := []string{"cold/false", "cold/true", "warm/true"}
		if got := activationKinds(seen()); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("activations = %v, want %v", got, want)
		}
	})
}

// sideEffects counts how many times a command actually ran in a pod — reached a
// running container and finished — as opposed to how many times it was asked for.
type sideEffects struct {
	*kubelet
	mu     sync.Mutex
	marker string
	ran    int
}

func (c *sideEffects) stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	err := c.kubelet.stream(ctx, ns, pod, argv, stdin, stdout, stderr)
	if err == nil && strings.Contains(strings.Join(argv, " "), c.marker) {
		c.mu.Lock()
		c.ran++
		c.mu.Unlock()
	}
	return err
}

func (c *sideEffects) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.ran }

// A tool call whose session moved under it is run once, by its caller: the call
// that met the lost pod is answered 503 with the command not run, the relocation
// that answer triggers puts a new pod under the id WITHOUT replaying it, a lease
// asked for after that is warm and runs nothing, and the caller's own retry runs
// it exactly once. A relocation that replayed the call would apply its side effect
// twice the day the first attempt had in fact reached the pod.
func TestARelocationDoesNotReplayTheCallItInterrupted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		seen := activations(t)
		s, k := lostService(t)
		fx := &sideEffects{kubelet: k, marker: "charge-the-card"}
		s.State.rt.str = fx
		ctx := context.Background()
		payer := account.Org("acme")
		m, err := Lease(s, ctx, "acme", payer, false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		call := Cmd{Command: "echo charge-the-card >> /tmp/ledger"}

		evict(t, s, m)
		if _, err := Run(s, ctx, "acme", m.ID, call); httpStatus(err) != http.StatusServiceUnavailable {
			t.Fatalf("a call on a lost pod answered %d %v, want 503", httpStatus(err), err)
		}
		if n := fx.count(); n != 0 {
			t.Fatalf("the call ran %d times on a pod that was gone, or was replayed by its relocation", n)
		}
		if _, err := Lease(s, ctx, "acme", payer, false, "", Spec{ID: m.ID}); err != nil {
			t.Fatalf("lease after the relocation: %v", err)
		}
		if n := fx.count(); n != 0 {
			t.Fatalf("asking for the relocated session ran the interrupted call %d times", n)
		}
		if r, err := Run(s, ctx, "acme", m.ID, call); err != nil || r.ExitCode != 0 {
			t.Fatalf("the caller's retry: %+v %v", r, err)
		}
		if n := fx.count(); n != 1 {
			t.Fatalf("the call ran %d times in all, want exactly once", n)
		}
		want := []string{"cold/false", "relocated/true", "warm/true"}
		if got := activationKinds(seen()); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("activations = %v, want %v", got, want)
		}
	})
}
