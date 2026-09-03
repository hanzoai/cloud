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
	"encoding/json"
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
		// deploy is the fleet's own setting: what the deployment states.
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

		// An UNCONFIGURED fleet gets the boundary that isolates, not the node's
		// own runtime. Empty meant the node default while runsc still had to be
		// installed; it is installed, and an unset field putting a tenant's
		// sandbox on the node's kernel is a downgrade nobody would see.
		{"unset isolates, volumeless", "", "", keeps, "gvisor", ""},
		{"unset isolates, with a volume", "", "", dev, "gvisor", ""},

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
			r := &runtime{}
			got, err := r.runtimeFor(c.m, c.ask, c.deploy)
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
					r := &runtime{}
					if contained {
						r.bare = bare()
					}
					got, err := r.runtimeFor(m, want, deploy)
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

// THE REFUSAL REACHES THE ENDPOINT, not just the derivation.
//
// Lease is where a forced runtime would do its damage, and it refuses before it
// writes a row, creates a PVC or asks the cluster for anything — so a request
// that cannot be honoured leaves nothing behind to clean up. No cluster needed
// to prove it, which is the point: the refusal happens before the first call to
// one.
func TestLeaseRefusesAForcedRuntimeBeforeItBuildsAnything(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	s, err := New(cloud.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	const org = "acme"

	_, err = Lease(s, ctx, org, org, false, "", Spec{Class: "dev", Project: "p", Runtime: "kata-fc"})
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

// THE REFUSAL REACHES THE WIRE. The test above proves the domain refuses; this
// one proves a CLIENT cannot get around it, which is a different claim and the
// one that matters now that `runtime` is a field on the request body.
//
// A browser lets somebody pick a runtime, and a browser can be made to send
// anything. So the question is not whether the picker offers a safe set — it is
// whether the endpoint does. It asks for the combination that loses data (a project
// volume under a runtime with no shared filesystem) the way a crafted client
// would, straight at the route, and requires a 400 carrying cloud's own sentence
// rather than a 201 carrying a substituted runtime.
//
// A SILENT SUBSTITUTION IS THE FAILURE BEING TESTED FOR, not merely a lost
// refusal. Handing back gvisor to someone who asked for kata-fc reads as success
// everywhere: the sandbox starts, the commands run, the files persist — and the
// person measuring the two runtimes writes down Firecracker's name beside
// gVisor's numbers. That is why the assertion is on the status AND on the
// absence of a row, and why the granted runtime is reported at all.
func TestTheEndpointRefusesARuntimeAClientCraftedForItself(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	s, err := New(cloud.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	Routes(app, s)
	const org = "acme"

	code, body := req(t, app, http.MethodPost, "/v1/sandbox", org,
		`{"class":"dev","project":"p","runtime":"kata-fc"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("POST /v1/sandbox with a volume + kata-fc = %d %s, want 400 — "+
			"a client must not be able to obtain a runtime the policy refuses", code, body)
	}
	// The reason has to be READABLE, because a person is going to read it. A bare
	// 400 sends them to the logs of a service they cannot see.
	if !strings.Contains(string(body), "cannot mount project volume") {
		t.Fatalf("refusal body %s does not say why the request is wrong", body)
	}

	// And it refused BEFORE building anything: no row, so no sandbox an operator
	// has to explain and no project slot held against the next honest request.
	code, body = req(t, app, http.MethodGet, "/v1/sandbox", org, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/sandbox = %d %s", code, body)
	}
	var listed struct {
		Sandboxes []Sandbox `json:"sandboxes"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(listed.Sandboxes) != 0 {
		t.Fatalf("a refused lease left %d row(s) behind: %+v", len(listed.Sandboxes), listed.Sandboxes)
	}

	// The unknown-runtime refusal is the same shape, and it is the one a typo
	// produces: without it the pod sits Pending forever with nothing said.
	if code, body = req(t, app, http.MethodPost, "/v1/sandbox", org,
		`{"class":"exec","runtime":"firecracker"}`); code != http.StatusBadRequest ||
		!strings.Contains(string(body), "is not one we run") {
		t.Fatalf("POST with an invented runtime = %d %s, want 400 naming the set we run", code, body)
	}
}

// THE SANDBOX SAYS WHICH RUNTIME IT GOT, and the point of the field is that it
// can differ from the one asked for. A caller that can only read back its own
// request learns nothing; the derivation is allowed to answer something else,
// and the answer is the only honest label for a measurement.
//
// No cluster is needed to prove the reporting, only the lease that fails to
// reach one: Lease writes the row with the granted runtime BEFORE it calls the
// cluster, so the row it leaves behind on a 503 carries exactly the value the
// pod spec would have been built from.
func TestASandboxReportsTheRuntimeItGotNotTheOneItAskedFor(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	s, err := New(cloud.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A fleet set to the fast runtime. A dev sandbox cannot have it — it mounts a
	// volume — so the deployment preference is derived down to the shared one,
	// and that, not "kata-fc", is what the row must say.
	serveFleet(t, "kata-fc")
	// No cluster, said once and immediately. A developer machine may well have a
	// kubeconfig, and then this test spends the full start timeout waiting for a
	// pod it does not need — the row is written before the cluster is asked, so
	// the answer is already there.
	s.State.rt.dyn = nil
	ctx := context.Background()
	const org = "acme"

	if _, err = Lease(s, ctx, org, org, false, "", Spec{Class: "dev", Project: "p"}); err == nil {
		t.Fatal("Lease reached a cluster in a unit test")
	}
	out, err := List(s, ctx, org, "", "")
	if err != nil || len(out) != 1 {
		t.Fatalf("List = %+v, %v — want the one row Lease recorded", out, err)
	}
	if out[0].Runtime != shared {
		t.Fatalf("row says runtime %q, but a volume-bearing sandbox on a kata-fc fleet gets %q",
			out[0].Runtime, shared)
	}
}
