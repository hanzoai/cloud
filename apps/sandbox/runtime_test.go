package sandbox

// The derivation that picks a sandbox's isolation boundary.
//
// These cases are a DATA-LOSS boundary, not input hygiene. kata-fc has no
// shared filesystem, so a sandbox that mounts a project volume under it writes
// into a tmpfs the VM destroys on exit — and nothing in Kubernetes reports
// that. The refusal has to happen here because there is no later moment at
// which it could.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestRuntimeForAsksTheVolumeNotTheClass(t *testing.T) {
	// api.go sets Volume from the PROJECT, never from the class. These three are
	// the shapes that reach runtimeFor.
	dev := Sandbox{ID: "m_1", Class: "dev", Project: "p", Volume: "m-acme-p-abc"}
	// THE CASE A class→runtime TABLE WOULD HAVE LOST. `exec` is the class the
	// fast runtime exists for, and an exec sandbox that names a project carries
	// a volume exactly like a dev one. Keyed on class, this row silently threw
	// the org's checkout away.
	execVol := Sandbox{ID: "m_2", Class: "exec", Project: "p", Volume: "m-acme-p-abc"}
	keeps := Sandbox{ID: "m_3", Class: "exec"}

	for _, c := range []struct {
		name string
		// deploy is SANDBOX_RUNTIME_CLASS: what the deployment states.
		deploy string
		// ask is the caller's explicit request, empty for none.
		ask     string
		m       Sandbox
		want    string
		wantErr string
	}{
		// The deployment states a preference, so it is derived DOWN. Refusing a
		// dev sandbox on a fleet set to kata-fc would make the setting unusable.
		{"volumeless takes the fast runtime", "kata-fc", "", keeps, "kata-fc", ""},
		{"a volume forces the shared one", "kata-fc", "", dev, "gvisor", ""},
		{"an exec WITH a project is a volume", "kata-fc", "", execVol, "gvisor", ""},

		// Rollback is one string: everything lands on gvisor, nothing derives.
		{"rollback: volumeless", "gvisor", "", keeps, "gvisor", ""},
		{"rollback: volume", "gvisor", "", dev, "gvisor", ""},

		// Empty is the node's DEFAULT runtime, which is a different request from
		// any named class. A cluster with no gVisor installed must not be handed
		// one because a sandbox happened to have a volume.
		{"unset stays unset, volumeless", "", "", keeps, "", ""},
		{"unset stays unset, with a volume", "", "", dev, "", ""},

		// kata-clh DOES share a filesystem (configuration-clh.toml sets
		// shared_fs = "virtio-fs"), so it holds a volume and is not derived away.
		{"clh shares a filesystem", "kata-clh", "", dev, "kata-clh", ""},

		// A CALLER states a request, so a contradiction is refused rather than
		// corrected — answering with a runtime nobody asked for is the same
		// silence this table exists to end.
		{"forcing fc onto a volume is refused", "gvisor", "kata-fc", dev, "",
			"cannot mount project volume"},
		{"forcing fc onto an exec volume is refused", "kata-fc", "kata-fc", execVol, "",
			"cannot mount project volume"},
		{"a caller may still choose fc for a volumeless sandbox", "gvisor", "kata-fc", keeps, "kata-fc", ""},
		{"a caller may choose the shared one", "kata-fc", "gvisor", dev, "gvisor", ""},

		// A runtimeClassName the cluster never heard of is a pod that waits
		// Pending with no explanation, so a typo stops here.
		{"an unknown runtime is refused", "gvisor", "runsc", keeps, "", "is not one we run"},
		{"case matters — the apiserver's does", "gvisor", "gVisor", keeps, "", "is not one we run"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &runtime{runtimeClass: c.deploy}
			got, err := r.runtimeFor(c.m, c.ask)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("runtimeFor(%+v, %q) = %q, want a refusal", c.m, c.ask, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("refusal %q does not say %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("runtimeFor(%+v, %q): %v", c.m, c.ask, err)
			}
			if got != c.want {
				t.Fatalf("runtimeFor(%+v, %q) = %q, want %q", c.m, c.ask, got, c.want)
			}
		})
	}
}

// THE INVARIANT, stated once and independent of the table above: whatever
// runtimeFor answers for a sandbox that mounts a volume, that runtime can hold
// one. Every case that reaches a pod goes through here, so this is the property
// that makes the silent-tmpfs failure unreachable rather than merely untested.
// It holds across the TRUST axis too, and that is the point of running both
// orgs through it: a boundary chosen for who owns the code still has to be one
// that can hold what the sandbox keeps. Two facts, one answer, no order in
// which one of them gets forgotten.
func TestRuntimeForNeverPutsAVolumeOnARuntimeThatCannotHoldIt(t *testing.T) {
	for _, org := range []string{"acme", authz.AdminOrg} {
		m := Sandbox{ID: "m_1", Org: org, Class: "dev", Project: "p", Volume: "m-acme-p-abc"}
		for _, deploy := range append([]string{""}, sorted()...) {
			for _, want := range append([]string{""}, sorted()...) {
				for _, contained := range []bool{false, true} {
					r := &runtime{runtimeClass: deploy}
					if contained {
						r.bare = bare()
					}
					got, err := r.runtimeFor(m, want)
					if err != nil {
						continue // refused, which is the other acceptable answer
					}
					if got == "" {
						// The node's own runtime shares a filesystem like any
						// ordinary pod, so a volume is safe there — but only the
						// deployment may ask for it. Deriving it from under a
						// deployment that NAMED a boundary would be the silent
						// downgrade this test exists to make unreachable.
						if deploy != "" {
							t.Fatalf("org=%q deploy=%q want=%q answered the node default, which the deployment did not ask for",
								org, deploy, want)
						}
						continue
					}
					if !runtimes[got].shares {
						t.Fatalf("org=%q deploy=%q want=%q = %q, which has no shared filesystem — the volume would be a tmpfs",
							org, deploy, want, got)
					}
				}
			}
		}
	}
}

// THE REFUSAL REACHES THE DOOR, not just the derivation.
//
// Lease is where a forced runtime would do its damage, and it refuses before it
// writes a row, creates a PVC or asks the cluster for anything — so a request
// that cannot be honoured leaves nothing behind to clean up. No cluster needed
// to prove it, which is the point: the refusal happens before the first call to
// one.
func TestLeaseRefusesAForcedRuntimeBeforeItBuildsAnything(t *testing.T) {
	s, err := New(cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	const org = "acme"

	_, err = Lease(s, ctx, org, Spec{Class: "dev", Project: "p", RuntimeClass: "kata-fc"})
	if err == nil {
		t.Fatal("Lease accepted kata-fc for a sandbox that mounts a volume")
	}
	he, ok := err.(*zip.HTTPError)
	if !ok || he.Status != http.StatusBadRequest {
		t.Fatalf("refusal is %v, want a 400 — the request is wrong, not the cluster", err)
	}
	if !strings.Contains(err.Error(), "cannot mount project volume") {
		t.Fatalf("refusal %q does not say what is wrong with the request", err)
	}

	// NOTHING WAS BUILT. A refusal that still wrote the row would leave an
	// operator reading a sandbox that never existed, and would hold the
	// one-live-sandbox-per-project slot against a caller who asked correctly.
	out, err := List(s, ctx, org, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("refused lease left %d row(s) behind: %+v", len(out), out)
	}
}
