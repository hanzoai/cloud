package plugin

// Wire-level tests for the control plane: the SuperAdmin gate, the refusals that
// must be an HTTP status rather than a 200 envelope, and a real disable that
// moves zip's own state. They drive whole requests through the app with the
// bridge installed, because a typed op sees the caller ONLY through the bridge —
// registering the routes without it would test a wiring that cannot exist.
//
// The child-process semantics of Reload/Unload (pin, rollback by digest, refusal
// of an unverified URL) are zip's, and zip tests them against real children in
// load_test.go. What is only testable HERE is that these routes reach those
// calls with the caller's request gated and the operation recorded.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	auditstore "github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/ha"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// app is the name the harness loads a plugin under. It must be one the manifest
// declares, because known() refuses anything else before the op runs.
const app = "billing"

var (
	superAdmin = map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "z@hanzo.ai"}
	tenant     = map[string]string{"X-Org-Id": "acme", "X-User-Id": "mallory"}
)

// mount wires the control plane against a real zip app holding one loaded
// plugin and a real on-disk audit chain, and returns a request helper.
//
// The plugin is loaded by Addr so the harness starts no child: Load records it
// either way, which is exactly the surface list/disable read and write.
func mount(t *testing.T) (*ops, func(method, path string, hdr map[string]string, body any) (*http.Response, []byte)) {
	t.Helper()
	rec, err := auditstore.Open(filepath.Join(t.TempDir(), "audit.db"), nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	z := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := z.Add(zip.Load(zip.Plugin{Name: app, Addr: "127.0.0.1:65535"}, "/v1/"+app)); err != nil {
		t.Fatalf("load %s: %v", app, err)
	}
	o := &ops{
		z:       z,
		audit:   rec,
		members: func() []ha.Member { return nil }, // a one-host fleet: no peer hop
		self:    "cloud-0",
		log:     luxlog.New("test"),
	}
	// Mirrors production, where serve.go installs the bridge globally before any
	// typed route.
	z.Use(cloud.Bridge())
	Routes(z, o)
	fa := z.Fiber()

	do := func(method, p string, hdr map[string]string, body any) (*http.Response, []byte) {
		t.Helper()
		var rdr io.Reader = bytes.NewReader(nil)
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, p, rdr)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", method, p, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	return o, do
}

// route is one endpoint of the control plane, named so the gate test can walk
// every one of them without a second list to keep in sync.
var routes = []struct{ method, path string }{
	{"GET", "/v1/admin/plugins"},
	{"POST", "/v1/admin/plugins/" + app + "/reload"},
	{"POST", "/v1/admin/plugins/" + app + "/enable"},
	{"POST", "/v1/admin/plugins/" + app + "/disable"},
}

// TestGate is the test this surface exists to pass. Every route can take
// production down or install new code, so a caller who is not a SuperAdmin must
// be refused BEFORE anything is touched — and refused on the read too, since
// what a host is running is itself intelligence about the fleet.
func TestGate(t *testing.T) {
	_, do := mount(t)
	for _, r := range routes {
		for _, caller := range []struct {
			name string
			hdr  map[string]string
		}{
			{"anonymous", nil},
			{"tenant admin without a minted IsAdmin", tenant},
			{"a forged header the identity boundary strips", map[string]string{"X-User-IsAdmin": "false"}},
		} {
			t.Run(r.method+" "+r.path+" / "+caller.name, func(t *testing.T) {
				resp, body := do(r.method, r.path, caller.hdr, nil)
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("got %d, want 403 (body=%s)", resp.StatusCode, body)
				}
			})
		}
	}
}

// TestGate_AdmitsSuperAdmin is the other half: the gate must not be refusing
// everyone. Without this a broken predicate would pass the test above.
func TestGate_AdmitsSuperAdmin(t *testing.T) {
	_, do := mount(t)
	resp, body := do("GET", "/v1/admin/plugins", superAdmin, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%s)", resp.StatusCode, body)
	}
}

// TestReload_RefusesUnverifiedURL is the arbitrary-code-execution refusal at the
// route. zip refuses a URL with no digest, but only once the request is three
// layers down; refusing here makes it a clean 400 the operator can act on, and
// proves the host will not be talked into fetching and running unpinned bits.
func TestReload_RefusesUnverifiedURL(t *testing.T) {
	_, do := mount(t)
	resp, body := do("POST", "/v1/admin/plugins/"+app+"/reload", superAdmin,
		map[string]string{"url": "https://s3.hanzo.ai/evil", "scope": scopeHost})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("unverified")) {
		t.Errorf("body = %s, want it to name the unverified download", body)
	}
}

// TestReload_RefusesUnknownApp proves the generated manifest is the authority on
// which apps exist. A typo is the caller's error, so it is a 400 here rather
// than a confusing "no plugin named" from inside zip.
func TestReload_RefusesUnknownApp(t *testing.T) {
	_, do := mount(t)
	for _, action := range []string{"reload", "enable", "disable"} {
		t.Run(action, func(t *testing.T) {
			p := "/v1/admin/plugins/definitely-not-an-app/" + action
			resp, body := do("POST", p, superAdmin, map[string]string{"scope": scopeHost})
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("got %d, want 400 (body=%s)", resp.StatusCode, body)
			}
			if !bytes.Contains(body, []byte("manifest")) {
				t.Errorf("body = %s, want it to name the manifest as the authority", body)
			}
		})
	}
}

// TestList_ReportsWhatIsLoaded proves the read answers from zip's own live plugin
// set rather than from config: the plugin the harness loaded appears, named,
// with the prefix it answers and the source it came from.
func TestList_ReportsWhatIsLoaded(t *testing.T) {
	_, do := mount(t)
	resp, body := do("GET", "/v1/admin/plugins?scope=host", superAdmin, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	var out ListOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	if out.Status != core.OK || len(out.Data) != 1 {
		t.Fatalf("out = %+v, want one host's own account", out)
	}
	h := out.Data[0]
	if h.Host != "cloud-0" || !h.Self {
		t.Errorf("host = %+v, want this host marked self", h)
	}
	if len(h.Plugins) != 1 || h.Plugins[0].Name != app {
		t.Fatalf("plugins = %+v, want the one loaded plugin", h.Plugins)
	}
	if got := h.Plugins[0]; got.Prefix != "/v1/"+app || got.Source != "remote" {
		t.Errorf("status = %+v, want the loaded prefix and source", got)
	}
}

// TestDisable_MovesZipsOwnState is the end-to-end one: the route must actually
// change what the host is running, not merely answer as though it had. It
// disables through HTTP and then reads the flag back off zip's live set, so a
// handler that returned ok without calling Unload fails here.
func TestDisable_MovesZipsOwnState(t *testing.T) {
	o, do := mount(t)
	if o.z.Plugins()[0].Disabled {
		t.Fatal("the plugin started disabled — the test proves nothing")
	}
	resp, body := do("POST", "/v1/admin/plugins/"+app+"/disable", superAdmin,
		map[string]string{"scope": scopeHost})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	var out ActionOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	if out.Status != core.OK || len(out.Data) != 1 || !out.Data[0].OK {
		t.Fatalf("out = %+v, want one host reporting success", out)
	}
	if !o.z.Plugins()[0].Disabled {
		t.Fatal("the route answered ok but zip still has the plugin enabled")
	}
}

// TestMutation_IsRecordedBeforeItIsReported is AU-5 at the route: a lifecycle
// change that cannot be accounted for must not be made. The refusal is an
// outcome rather than a status, because nothing is wrong with the REQUEST — the
// deployment is what is unfit — and it must come before the change.
func TestMutation_IsRecordedBeforeItIsReported(t *testing.T) {
	o, do := mount(t)
	resp, body := do("POST", "/v1/admin/plugins/"+app+"/disable", superAdmin,
		map[string]string{"scope": scopeHost})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d (body=%s)", resp.StatusCode, body)
	}
	got, total, err := o.audit.Query(context.Background(), auditstore.Filter{Action: "plugin.disable"})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].Resource.ID != app {
		t.Fatalf("audit chain holds %d plugin.disable records (%+v), want exactly this one", total, got)
	}
}
