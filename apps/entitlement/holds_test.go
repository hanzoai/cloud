package entitlement

// The read the refusal makes, against the real store — the end a fake peer stands
// in for, so the chain store → holds → plane has no untested link.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// installs stands in for the base app: it records each module grown and the org
// the call acted for, and answers fail when it is set.
type installs struct {
	mu   sync.Mutex
	got  []string // "org:module"
	fail error
}

func stubInstalls(t *testing.T) *installs {
	t.Helper()
	in := &installs{}
	prev := importFn
	importFn = func(ctx context.Context, b *client.BaseImportIn) (*client.BaseGrowth, error) {
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.fail != nil {
			return nil, in.fail
		}
		in.got = append(in.got, zip.CallerOf(ctx).Org+":"+b.Module)
		return &client.BaseGrowth{}, nil
	}
	t.Cleanup(func() { importFn = prev })
	return in
}

func (in *installs) all() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.got)
}

// Keyed by both columns, so one org's purchase cannot answer for another's.
func TestHoldsIsPerOrgAndPerProduct(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// Disabling turns it back off: the switch is a switch, not a one-way grant.
func TestHoldsFollowsDisable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// An unmounted store errors rather than answering false: both refuse, but only
// the error tells an operator the subsystem did not start.
func TestHoldsWithoutAStoreIsAnError(t *testing.T) {
	prev := mounted
	mounted = nil
	t.Cleanup(func() { mounted = prev })

	ctx := plane.For(context.Background(), "acme")
	_, err := holds(ctx, &client.ProductIn{Product: "crm"})
	if err == nil {
		t.Fatal("holds answered with no store mounted — an unstarted subsystem reads as a customer decision")
	}
}

// No org is refused rather than defaulted; the subject is not an input.
func TestHoldsNeedsACaller(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		prev := mounted
		mounted = &service{store: openTestStore(t)}
		t.Cleanup(func() { mounted = prev })

		if _, err := holds(context.Background(), &client.ProductIn{Product: "crm"}); err == nil {
			t.Error("holds answered a call with no org")
		}
		if _, err := holds(plane.For(context.Background(), "acme"), &client.ProductIn{Product: "  "}); err == nil {
			t.Error("holds answered a call naming no product")
		}
		// The happy path through the handler.
		if err := mounted.store.Enable(context.Background(), "acme", "crm", "", 1); err != nil {
			t.Fatalf("enable: %v", err)
		}
		out, err := holds(plane.For(context.Background(), "acme"), &client.ProductIn{Product: "crm"})
		if err != nil || out == nil || !out.On {
			t.Errorf("holds(acme, crm) = %+v, %v — the org enabled it and was told no", out, err)
		}
	})
}

// A beta grant turns a product in beta on for the caller's own org, once, and
// nothing else: a product not in beta is refused, and so is a call with no org.
func TestGrantTurnsOnTheBetaForTheCallersOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		prev := mounted
		mounted = &service{store: openTestStore(t), log: luxlog.NewNoOpLogger()}
		t.Cleanup(func() { mounted = prev })
		installed := stubInstalls(t)
		acme := plane.For(context.Background(), "acme")

		if _, err := grant(context.Background(), &client.ProductIn{Product: "crm"}); err == nil {
			t.Fatal("a grant with no org on the call answered")
		}
		if _, err := grant(acme, &client.ProductIn{Product: "chat"}); err == nil {
			t.Fatal("a product not in beta was granted")
		}
		for range 2 {
			out, err := grant(acme, &client.ProductIn{Product: "crm"})
			if err != nil || !out.On {
				t.Fatalf("grant(acme, crm) = %+v, %v", out, err)
			}
		}
		if on, _ := mounted.store.Holds(context.Background(), "acme", "crm"); !on {
			t.Fatal("the grant did not turn the CRM on for acme")
		}
		if on, _ := mounted.store.Holds(context.Background(), "initech", "crm"); on {
			t.Fatal("acme's grant reached initech")
		}
		if got := installed.all(); !slices.Equal(got, []string{"acme:crm", "acme:crm"}) {
			t.Fatalf("installs = %v, want the crm module grown in acme's Base on each grant", got)
		}
	})
}

// A grant that cannot give the org the CRM's collections is refused, and the org is
// left without the beta: no org holds the CRM with nowhere to keep its records.
func TestAGrantThatCannotInstallIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		prev := mounted
		mounted = &service{store: openTestStore(t), log: luxlog.NewNoOpLogger()}
		t.Cleanup(func() { mounted = prev })
		installed := stubInstalls(t)
		installed.fail = errors.New("base: hosted Bases are not embedded in this deployment")

		if _, err := grant(plane.For(context.Background(), "acme"), &client.ProductIn{Product: "crm"}); err == nil {
			t.Fatal("a grant answered though its install failed")
		}
		if on, _ := mounted.store.Holds(context.Background(), "acme", "crm"); on {
			t.Fatal("acme holds the CRM without its collections")
		}
	})
}
