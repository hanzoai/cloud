package space

// What this surface does when it cannot serve, and who it serves at all.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// every address this surface answers, with the shape a caller sends. It is the
// list the posture tests quantify over, so a route added to Use without a
// thought about the unconfigured or unauthenticated case goes red here.
var addresses = []struct {
	method, path, body string
}{
	{http.MethodGet, "/v1/space/spaces", ""},
	{http.MethodPost, "/v1/space/spaces", `{"name":"hq"}`},
	{http.MethodGet, "/v1/space/hq/drives", ""},
	{http.MethodPost, "/v1/space/hq/drives", `{"name":"media"}`},
	{http.MethodDelete, "/v1/space/hq/drives/media", ""},
	{http.MethodGet, "/v1/space/hq/drives/media/files", ""},
	{http.MethodGet, "/v1/space/hq/drives/media/files/a.txt", ""},
	{http.MethodPut, "/v1/space/hq/drives/media/files/a.txt", ""},
	{http.MethodDelete, "/v1/space/hq/drives/media/files/a.txt", ""},
}

// TestTheSurfaceIsRegistered pins the served set EXACTLY against the live
// router, so a route added, renamed or deleted in Use goes red here rather than
// only in a regenerated document — and so `addresses` below, which the two
// posture tests quantify over, cannot fall behind what is served.
func TestTheSurfaceIsRegistered(t *testing.T) {
	st := newStore()
	app := mount(t, st)

	want := map[string]bool{
		"GET /v1/space/health":                          true,
		"GET /v1/space/spaces":                          true,
		"POST /v1/space/spaces":                         true,
		"GET /v1/space/:space/drives":                   true,
		"POST /v1/space/:space/drives":                  true,
		"DELETE /v1/space/:space/drives/:drive":         true,
		"GET /v1/space/:space/drives/:drive/files":      true,
		"GET /v1/space/:space/drives/:drive/files/+":    true,
		"PUT /v1/space/:space/drives/:drive/files/+":    true,
		"DELETE /v1/space/:space/drives/:drive/files/+": true,
	}
	live := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		// The router registers HEAD beside every GET, which is the framework's
		// answer and not a declaration of this subsystem's.
		if strings.HasPrefix(r.Path, "/v1/space") && r.Method != http.MethodHead {
			live[r.Method+" "+r.Path] = true
		}
	}
	for k := range want {
		if !live[k] {
			t.Errorf("%s is expected here and not served", k)
		}
	}
	for k := range live {
		if !want[k] {
			t.Errorf("%s is served and not expected here — add it to `addresses` and give it a posture", k)
		}
	}
	// The probe is the one served address that is NOT in `addresses`, because the
	// posture tests below are about the ops that name a space.
	if len(want) != len(addresses)+1 {
		t.Fatalf("`addresses` names %d of the %d served ops", len(addresses), len(want)-1)
	}
}

// TestUnconfiguredFailsClosedUnderItsOwnName. The route set mounts whatever the
// deployment configured, so an unconfigured one answers 503 — the honest "storage
// is unavailable" — at every address, under this subsystem's own name. Mounted
// only when configured, these addresses would fall to whatever answers the /v1
// remainder and a caller would read someone else's 404.
func TestUnconfiguredFailsClosedUnderItsOwnName(t *testing.T) {
	t.Setenv("S3_ADMIN_ACCESS_KEY", "")
	t.Setenv("S3_ADMIN_SECRET_KEY", "")
	t.Setenv(feeEnv, "0")
	t.Setenv("CLOUD_ENV", "mainnet")
	app := mountWith(t, cloud.Deps{})

	for _, a := range addresses {
		code, body := send(t, app, a.method, a.path, "acme", a.body)
		if code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d (%s), want 503", a.method, a.path, code, body)
			continue
		}
		var refusal struct{ Detail string }
		_ = json.Unmarshal([]byte(body), &refusal)
		if refusal.Detail != "object storage is not configured" {
			t.Errorf("%s %s refused with %q, want the honest sentence", a.method, a.path, refusal.Detail)
		}
	}

	// And the probe says the same thing in its own shape, at its own status,
	// without a credential.
	code, body := send(t, app, http.MethodGet, "/v1/space/health", "", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("health = %d (%s), want 503", code, body)
	}
	var h spaceHealth
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("health body %s: %v", body, err)
	}
	if h.Service != "space" || h.Status != "degraded" || h.Ready {
		t.Fatalf("health = %+v, want space/degraded/not ready", h)
	}
}

// TestHealthIsUngated. Liveness has to be probe-able without a token, so the
// probe is the one operation here that names no space, bills nothing and admits
// nobody.
func TestHealthIsUngated(t *testing.T) {
	st := newStore()
	app := mount(t, st)

	code, body := send(t, app, http.MethodGet, "/v1/space/health", "", "")
	if code != http.StatusOK {
		t.Fatalf("health with no principal = %d (%s), want 200", code, body)
	}
	var h spaceHealth
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("health body %s: %v", body, err)
	}
	if !h.Ready || !h.Presign || h.Status != "ok" {
		t.Fatalf("health = %+v, want ready and able to sign", h)
	}
}

// TestNothingIsReachedWithoutAValidatedPrincipal. X-Org-Id survives the identity
// boundary on the anonymous data path, so a caller presenting one and no
// validated user is exactly the forge this admission exists to refuse — and it is
// refused with NOTHING touched.
func TestNothingIsReachedWithoutAValidatedPrincipal(t *testing.T) {
	for _, tc := range []struct {
		name string
		org  string
		user string
	}{
		{"nobody at all", "", ""},
		{"an org with no validated user", "acme", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			app := mount(t, st)

			for _, a := range addresses {
				req := httptestRequest(a.method, a.path, a.body)
				if tc.org != "" {
					req.Header.Set("X-Org-Id", tc.org)
				}
				if tc.user != "" {
					req.Header.Set("X-User-Id", tc.user)
				}
				resp, err := app.Test(req)
				if err != nil {
					t.Fatalf("%s %s: %v", a.method, a.path, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("%s %s = %d, want 403", a.method, a.path, resp.StatusCode)
				}
			}
			if n := len(st.seen()); n != 0 {
				t.Fatalf("the store was reached %d times for an unadmitted caller, want 0", n)
			}
		})
	}
}

// TestTwoOrgsNeverMeet. The same space and drive names from two orgs address two
// buckets, because the bucket is a function of the VALIDATED org and no request
// field is in it.
func TestTwoOrgsNeverMeet(t *testing.T) {
	st := newStore()
	app := mount(t, st)

	for _, org := range []string{"acme", "globex"} {
		code, body := send(t, app, http.MethodDelete, "/v1/space/hq/drives/media/files/a.txt", org, "")
		if code != http.StatusNoContent {
			t.Fatalf("%s delete = %d (%s), want 204", org, code, body)
		}
	}
	del := st.asked(http.MethodDelete)
	if len(del) != 2 {
		t.Fatalf("store saw %d deletes, want 2", len(del))
	}
	if del[0].Path == del[1].Path {
		t.Fatalf("both orgs deleted %q — one bucket for two orgs", del[0].Path)
	}
	for i, org := range []string{"acme", "globex"} {
		if want := "/" + bucket(org, "hq") + "/media/a.txt"; del[i].Path != want {
			t.Fatalf("%s deleted %q, want %q", org, del[i].Path, want)
		}
	}
}
