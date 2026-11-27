// Copyright © 2026 Hanzo AI. MIT License.

package sandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/authz"
)

// everywhere pools every boundary, for tests about a fact other than placement.
func everywhere() map[string]bool {
	out := map[string]bool{}
	for _, name := range sorted() {
		out[name] = true
	}
	return out
}

// A fleet set to a class pinned OFF the sandbox nodes leases on them anyway.
//
// The topology is the one AWS runs: `visor` selects the tainted sandbox pool and
// tolerates its taint; `gvisor` selects one untainted node that serves everything
// else — the control plane. A fleet set to gvisor put every tenant sandbox on
// that node, and the lease failed whenever it was busy.
func TestAFleetSettingOffTheSandboxPoolLeasesOnThePool(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		taint := []any{map[string]any{"key": "hanzo.ai/visor", "operator": "Exists", "effect": "NoSchedule"}}
		r := &runtime{dyn: fakeClasses(
			runtimeClass("visor", map[string]any{"hanzo.ai/visor": "true"}, taint),
			runtimeClass("gvisor", map[string]any{"hanzo.ai/gvisor": "true"}, nil),
			runtimeClass("kata-clh", map[string]any{"hanzo.ai/kata": "true"}, taint),
		)}
		r.pools = r.pooled(context.Background())
		if r.pools["gvisor"] || !r.pools["visor"] || !r.pools["kata-clh"] {
			t.Fatalf("pools = %v, want visor and kata-clh only", r.pools)
		}

		dev := Sandbox{ID: "m_1", Org: "hanzo", Class: "dev", Project: "console", Volume: "m-hanzo-console-abc"}
		for _, c := range []struct{ fleet, want string }{
			{"gvisor", shared},
			{"kata-clh", "kata-clh"},
			{"visor", "visor"},
			{"", shared},
		} {
			got, err := r.runtimeFor(dev, "", c.fleet)
			if err != nil || got != c.want {
				t.Fatalf("fleet %q gave %q (err %v), want %q", c.fleet, got, err, c.want)
			}
		}

		// A caller naming the class is refused, not placed there.
		if got, err := r.runtimeFor(dev, "gvisor", ""); err == nil || !strings.Contains(err.Error(), "not kept") {
			t.Fatalf("asking for gvisor gave %q (err %v), want a refusal", got, err)
		}
		if got, err := r.runtimeFor(dev, shared, "gvisor"); err != nil || got != shared {
			t.Fatalf("asking for the floor gave %q (err %v)", got, err)
		}
	})
}

// THE INVARIANT, over every input: whatever runtimeFor answers with a kernel of
// its own is the floor or a class the cluster pools. No setting, request, org or
// volume reaches a node the cluster did not set aside for sandboxes.
func TestNoAnswerLandsOffTheSandboxPool(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, pools := range []map[string]bool{nil, {"kata-clh": true}, everywhere()} {
			for _, org := range []string{"acme", authz.AdminOrg} {
				for _, deploy := range append([]string{""}, sorted()...) {
					for _, ask := range append([]string{""}, sorted()...) {
						for _, vol := range []string{"", "m-acme-p-abc"} {
							r := &runtime{pools: pools, fast: preferred}
							got, err := r.runtimeFor(leased(org, vol), ask, deploy)
							if err != nil || got == "" || !runtimes[got].kernel {
								continue
							}
							if got != shared && !pools[got] {
								t.Fatalf("pools=%v org=%q deploy=%q ask=%q answered %q, which this cluster does not pool",
									pools, org, deploy, ask, got)
							}
						}
					}
				}
			}
		}
	})
}

// pooled reads the same predicate confine does, and every no-answer is empty.
func TestPooledNeedsBothHalves(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		taint := []any{map[string]any{"key": "k", "operator": "Exists", "effect": "NoSchedule"}}
		sel := map[string]any{"pool": "sandbox"}
		r := &runtime{dyn: fakeClasses(
			runtimeClass("a", sel, taint),
			runtimeClass("b", sel, nil),
			runtimeClass("c", nil, taint),
			runtimeClass("d", nil, nil),
		)}
		got := r.pooled(context.Background())
		if len(got) != 1 || !got["a"] {
			t.Fatalf("pooled = %v, want only a", got)
		}
		if (&runtime{}).pooled(context.Background()) != nil {
			t.Fatal("pooled answered with no cluster client")
		}
	})
}
