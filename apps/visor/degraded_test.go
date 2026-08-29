package visor

// An outage must not read as an empty estate.
//
// This is the regression test for a defect that shipped and stayed invisible for
// weeks: production Visor sat four tags behind the commit that introduced
// /v1/k8s/clusters, so every call 404'd, listK8sClusters folded the failure to a
// Warn, and the endpoint answered 200 {"clusters":[]} to an operator running eight
// clusters. Nothing in the response distinguished that from a healthy answer for
// an org that owns nothing — which is exactly the question a fleet check asks.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

// downVisor mounts the surface against a Visor that answers every path the way the
// real one answered an unknown route: 404, with an HTML error page as the body.
// The body shape matters — it is what made the raw error unfit to return.
func downVisor(t *testing.T) *zip.App {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!DOCTYPE html>\n<html lang=\"en\">\n\t<head>\n\t\t<title>Not Found</title>\n"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("VISOR_URL", srv.URL)
	t.Setenv("VISOR_CLIENT_ID", "")
	t.Setenv("VISOR_CLIENT_SECRET", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// TestClusterListReportsDegradedSource pins that a Visor outage is REPORTED, on
// both cluster surfaces, rather than folded into an empty success.
func TestClusterListReportsDegradedSource(t *testing.T) {
	for _, path := range []string{"/v1/visor/k8s/clusters", "/v1/visor/clusters"} {
		t.Run(path, func(t *testing.T) {
			app := downVisor(t)
			status, body := reqK8s(t, app, http.MethodGet, path, "acme", false, nil)

			// Still a 200 with the BYO half: a down optional provider must not take
			// the whole page. The fold is deliberate; only the silence was the bug.
			if status != http.StatusOK {
				t.Fatalf("status = %d; want 200 — an optional provider must not 502 the page: %s", status, body)
			}
			var got clusterList
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode: %v (%s)", err, body)
			}
			if len(got.Degraded) == 0 {
				t.Fatal("a Visor outage was reported as a complete, empty result — " +
					"an outage is indistinguishable from owning no clusters")
			}
			if got.Degraded[0].Source != "visor" {
				t.Errorf("degraded source = %q; want %q", got.Degraded[0].Source, "visor")
			}
			// The reason must be usable in a CLI line and a log: no markup, one line.
			reason := got.Degraded[0].Reason
			if reason == "" {
				t.Error("degraded entry carries no reason")
			}
			for _, bad := range []string{"<", "\n", "DOCTYPE"} {
				if strings.Contains(reason, bad) {
					t.Errorf("reason leaks the upstream response body (%q): %q", bad, reason)
				}
			}
			if len(reason) > 200 {
				t.Errorf("reason is %d chars; want a terse line", len(reason))
			}
		})
	}
}

// TestHealthyClusterListIsUnchanged pins that the field is ADDITIVE: when every
// source answers, the response carries no `degraded` key at all, so an existing
// consumer sees exactly the bytes it saw before.
func TestHealthyClusterListIsUnchanged(t *testing.T) {
	app := mountK8s(t, &k8sFake{})
	status, body := reqK8s(t, app, http.MethodGet, "/v1/visor/k8s/clusters", "acme", false, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200: %s", status, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if _, present := raw["degraded"]; present {
		t.Errorf("a healthy response carries a `degraded` key: %s", body)
	}
}

// TestTerseIsFitForAResponseField pins the reduction directly, including the exact
// production string — "visor: upstream 404: <!DOCTYPE html>…" — that motivated it.
func TestTerseIsFitForAResponseField(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"the production error", "visor: upstream 404: <!DOCTYPE html>\n<html lang=\"en\">\n\t<head>", "visor: upstream 404"},
		{"multi-line is truncated at the first break", "visor: unreachable\nstack frame", "visor: unreachable"},
		{"a clean message survives whole", "visor: unreachable: dial tcp: refused", "visor: unreachable: dial tcp: refused"},
		{"a trailing colon is trimmed", "visor: upstream 500:", "visor: upstream 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := terse(errString(tc.in)); got != tc.want {
				t.Errorf("terse(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := terse(nil); got != "" {
		t.Errorf("terse(nil) = %q; want empty", got)
	}
	long := make([]byte, 400)
	for i := range long {
		long[i] = 'x'
	}
	if got := terse(errString(string(long))); len(got) > 200 {
		t.Errorf("terse did not cap a long message: %d chars", len(got))
	}
}

type errString string

func (e errString) Error() string { return string(e) }
