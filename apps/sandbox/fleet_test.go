// Copyright © 2026 Hanzo AI. MIT License.

package sandbox

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/internal/planetest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// serveFleet stands the settings app up on a REAL plane socket answering one
// runtime, which is how a sandbox learns the fleet's preference in production.
//
// A real peer and not a stub field: the value has to survive the same JSON
// document, socket and typed op that carry it in the fleet, and a field set in a
// test would prove only that the derivation reads a field.
func serveFleet(t *testing.T, runtime string) {
	t.Helper()
	dir := planetest.Dir(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	cloud.ResetPlane()

	doc, err := json.Marshal(map[string]string{"runtime": runtime})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	zip.Post[plane.Product, plane.Configured](cloud.Plane(), "/settings/fleet",
		func(context.Context, *plane.Product) (*plane.Configured, error) {
			return &plane.Configured{Config: string(doc)}, nil
		},
		zip.WithOperationID(plane.SettingsFleet))

	stop, err := cloud.ServePlane("settings", nil)
	if err != nil {
		t.Fatalf("ServePlane(settings): %v", err)
	}
	t.Cleanup(func() { _ = stop(); cloud.ResetPlane(); plane.Unbind() })
	waitBound(t, filepath.Join(dir, "settings.sock"))
}

// waitBound blocks until the peer has actually bound. ServePlane returns before the
// listener accepts, and zip.DialApp is lazy — it hands back a live client for a
// peer that is not there — so a call made too early reads as "not configured"
// rather than as a race.
func waitBound(t *testing.T, sock string) {
	t.Helper()
	for range 200 {
		if c, err := zip.Dial(sock); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("peer never bound %s", sock)
}

// A knob an operator turns must reach a sandbox WITHOUT A ROLLOUT. That is the
// whole point of moving it out of the environment, and it is the one property a
// unit test of the derivation cannot show: runtimeFor is pure over three facts,
// so it proves what happens GIVEN a preference, never that the live one arrives.
func TestTheFleetsSettingReachesASandbox(t *testing.T) {
	serveFleet(t, "kata-fc")
	r := newRuntime()
	if got := r.preference(context.Background()); got != "kata-fc" {
		t.Fatalf("preference = %q, want kata-fc — the setting did not reach the sandbox", got)
	}
}

// An operator can mistype, and an unknown runtimeClassName is a pod that waits
// Pending with nothing said. It used to be refused at BOOT, which only worked
// while the value could only come from the environment: a setting that changes
// under a running fleet has no boot to be refused at. So the table catches it at
// the read, and what a typo gets is the boundary that can serve the sandbox —
// never the apiserver's silence.
func TestAMistypedSettingFallsToTheBoundaryThatServes(t *testing.T) {
	m := Sandbox{ID: "m_1", Org: "acme", Volume: "m-acme-p-abc"}
	for _, c := range []struct{ set, want string }{
		{"gvisor", "gvisor"},
		{"kata-clh", "kata-clh"},
		{"", shared},          // unconfigured isolates; it does not fall to the node
		{"gvisor ", "gvisor"}, // trimmed on the way in
		{"runsc", shared},     // the HANDLER's name, not the class's
		{"gVisor", shared},    // case matters — the apiserver's does
		{"default", shared},   // a plausible guess, and wrong
		{"kata-fc", shared},   // known, but it cannot hold this volume
	} {
		t.Run(c.set, func(t *testing.T) {
			serveFleet(t, c.set)
			r := newRuntime()
			got, err := r.runtimeFor(m, "", r.preference(context.Background()))
			if err != nil {
				t.Fatalf("fleet setting %q must not fail a lease: %v", c.set, err)
			}
			if got != c.want {
				t.Fatalf("fleet %q gave runtime %q, want %q", c.set, got, c.want)
			}
		})
	}
}

// Settings being down is not a reason to refuse a sandbox, and it is not a reason
// to weaken one either. An unreachable peer reads as unconfigured, and
// unconfigured isolates — so an outage in the settings app cannot move a tenant's
// sandbox onto the node's kernel.
func TestNoSettingsPeerStillIsolates(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(func() { cloud.ResetPlane(); plane.Unbind() })

	r := newRuntime()
	if got := r.preference(context.Background()); got != "" {
		t.Fatalf("preference with no peer = %q, want empty", got)
	}
	// And empty is not a weaker sandbox.
	got, err := r.runtimeFor(Sandbox{ID: "m_1", Org: "acme", Volume: "m-acme-p-abc"}, "", "")
	if err != nil || got != shared {
		t.Fatalf("no settings peer gave runtime %q (err %v), want %q", got, err, shared)
	}
}

// THE FLOOR, over every fleet value at once: a tenant's sandbox never lands on
// the node's own kernel, whatever an operator types in the field.
//
// The table tests state cases; this states the property, so a boundary added to
// `runtimes` later cannot open a hole that no case happened to cover. It is the
// isolation half of the volume invariant in runtime_test.go — same shape, same reason.
func TestNoFleetValuePutsATenantOnTheNodesKernel(t *testing.T) {
	r := &runtime{}
	for _, fleet := range append([]string{"", " ", "runsc", "default", "gVisor"}, sorted()...) {
		for _, vol := range []string{"", "m-acme-p-abc"} {
			m := Sandbox{ID: "m_1", Org: "acme", Volume: vol}
			got, err := r.runtimeFor(m, "", fleet)
			if err != nil {
				continue // a refusal is the other acceptable answer
			}
			if got == "" {
				t.Fatalf("fleet %q + volume %q gave the node's own runtime", fleet, vol)
			}
			if !runtimes[got].kernel {
				t.Fatalf("fleet %q + volume %q gave %q, which has no kernel of its own", fleet, vol, got)
			}
		}
	}
}

// The DEFAULT is the strongest boundary the sandbox can hold, and the two facts
// that would make that wrong are both asked before it is reached.
//
// `fast` is empty until the cluster is seen to install the class, so a
// deployment without it keeps the floor rather than getting a pod that waits
// Pending; and a sandbox carrying a volume does not fit on a boundary with no
// shared filesystem, so it keeps the floor too — its writes would land in a
// tmpfs and be lost when the sandbox ends.
//
// Set as a FIELD rather than through the constructor because that is the fact
// the constructor resolves against a live cluster, and the derivation is pure
// over it — which is the whole reason it can be stated here at all.
func TestTheDefaultIsTheStrongestBoundaryTheSandboxCanHold(t *testing.T) {
	for _, c := range []struct {
		name, fast, volume, want string
	}{
		{"installed, keeps nothing", preferred, "", preferred},
		{"installed, carries a disk", preferred, "m-acme-p-abc", shared},
		{"not installed, keeps nothing", "", "", shared},
		{"not installed, carries a disk", "", "m-acme-p-abc", shared},
		// A cluster that answered with a name we do not run fits nothing, so the
		// floor takes it — the same answer a typo gets everywhere else here.
		{"unknown class", "runsc", "", shared},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &runtime{fast: c.fast}
			got, err := r.runtimeFor(Sandbox{ID: "m_1", Org: "acme", Volume: c.volume}, "", "")
			if err != nil {
				t.Fatalf("default must not fail a lease: %v", err)
			}
			if got != c.want {
				t.Fatalf("fast=%q volume=%q gave %q, want %q", c.fast, c.volume, got, c.want)
			}
		})
	}
}

// The floor holds over the new default too: whatever the cluster installs, a
// tenant's sandbox never lands on the node's own kernel. Same property as
// TestNoFleetValuePutsATenantOnTheNodesKernel, asked of the other input — a
// boundary added to `runtimes` later cannot open a hole through `fast`.
func TestNoInstalledBoundaryPutsATenantOnTheNodesKernel(t *testing.T) {
	for _, fast := range append([]string{"", " ", "runsc", "default"}, sorted()...) {
		for _, vol := range []string{"", "m-acme-p-abc"} {
			r := &runtime{fast: fast}
			got, err := r.runtimeFor(Sandbox{ID: "m_1", Org: "acme", Volume: vol}, "", "")
			if err != nil {
				continue // a refusal is the other acceptable answer
			}
			if b, ok := runtimes[got]; !ok || !b.kernel {
				t.Fatalf("fast=%q volume=%q gave %q, which is not a kernel of its own", fast, vol, got)
			}
		}
	}
}
