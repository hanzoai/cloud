package share

// The wire, through the REAL router. Before the typed migration this package had
// no route-level test at all — only the pure helpers below it — so nothing here
// measured the two things a caller actually depends on: the org gate, and the
// honest-empty degradation a console load relies on.
//
// It drives routes(), which is exactly what Mount calls, so a route or a
// middleware added there is exercised here rather than in a reconstruction of it.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountShare wires the share surface over an in-memory controller.
func mountShare(t *testing.T, cl controller) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{Logger: luxlog.New("test")}, "share"), State: state{cl: cl}}
	routes(app, s)
	return app
}

// call drives one request. org == "" means no validated principal at all —
// principal.Org needs X-User-Id, which the gateway mints ONLY from a verified
// credential.
func call(t *testing.T, app *zip.App, method, path, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestSharesAnswerAtBothPathForms — the collection is declared at /v1/share, with
// no trailing slash, and fiber's non-strict routing means /v1/share/ still reaches
// it. Both forms must answer identically: the document now names ONE path, and a
// client that had been calling the other must not break.
func TestSharesAnswerAtBothPathForms(t *testing.T) {
	app := mountShare(t, &fakeController{configuredV: true, tokenV: "tok"})
	for _, p := range []string{"/v1/share", "/v1/share/"} {
		code, body := call(t, app, http.MethodGet, p, "acme")
		if code != http.StatusOK {
			t.Fatalf("GET %s want 200, got %d (%s)", p, code, body)
		}
	}
}

// TestReadsFailClosedWithoutAValidatedPrincipal — a caller with no verified
// credential reads nothing and provisions nothing.
func TestReadsFailClosedWithoutAValidatedPrincipal(t *testing.T) {
	app := mountShare(t, &fakeController{configuredV: true, tokenV: "tok"})
	if code, _ := call(t, app, http.MethodGet, "/v1/share", ""); code != http.StatusForbidden {
		t.Errorf("unauth GET /v1/share want 403, got %d", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/share/enable", ""); code != http.StatusForbidden {
		t.Errorf("unauth POST /v1/share/enable want 403, got %d", code)
	}
}

// TestUnconfiguredDeployment — the list degrades to an honest EMPTY list at 200 so
// the console never error-toasts on load, while the write fails closed at 503. The
// asymmetry is the contract: a read that cannot answer says "none", a write that
// cannot act says so.
func TestUnconfiguredDeployment(t *testing.T) {
	app := mountShare(t, &fakeController{configuredV: false})

	code, body := call(t, app, http.MethodGet, "/v1/share", "acme")
	if code != http.StatusOK {
		t.Fatalf("unconfigured GET want 200, got %d (%s)", code, body)
	}
	if got := string(body); got != `{"shares":[]}` {
		t.Errorf("unconfigured GET body = %s, want {\"shares\":[]} — an empty LIST, never null", got)
	}

	if code, _ := call(t, app, http.MethodPost, "/v1/share/enable", "acme"); code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured POST /v1/share/enable want 503, got %d", code)
	}
}

// TestListDegradesWhenNotProvisioned — an org that has never enabled a share has no
// account, and that is an empty list rather than an error.
func TestListDegradesWhenNotProvisioned(t *testing.T) {
	app := mountShare(t, &fakeController{configuredV: true, tokenErr: errors.New("no account")})
	code, body := call(t, app, http.MethodGet, "/v1/share", "acme")
	if code != http.StatusOK || string(body) != `{"shares":[]}` {
		t.Fatalf("unprovisioned GET = %d %s, want 200 {\"shares\":[]}", code, body)
	}
}

// TestEnableShape — the credential answer the `hanzo share` CLI parses.
func TestEnableShape(t *testing.T) {
	t.Setenv("SHARE_URL_TEMPLATE", "https://{token}.share.hanzo.ai")
	app := mountShare(t, &fakeController{configuredV: true, tokenV: "acct-tok"})
	code, body := call(t, app, http.MethodPost, "/v1/share/enable", "acme")
	if code != http.StatusOK {
		t.Fatalf("enable want 200, got %d (%s)", code, body)
	}
	var got enableResp
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if got.AccountToken != "acct-tok" {
		t.Errorf("accountToken = %q, want the controller's", got.AccountToken)
	}
	if got.URLTemplate != "https://{token}.share.hanzo.ai" {
		t.Errorf("urlTemplate = %q", got.URLTemplate)
	}
}

// TestListProjectsEveryEnvironmentsShares — the list flattens across environments,
// which is what makes it the org's shares rather than one machine's.
func TestListProjectsEveryEnvironmentsShares(t *testing.T) {
	t.Setenv("SHARE_URL_TEMPLATE", "https://{token}.share.hanzo.ai")
	// overviewResp's rows are anonymous structs, so build the value the only way a
	// caller ever does: from the controller's own JSON.
	var ov overviewResp
	if err := json.Unmarshal([]byte(`{"environments":[
	  {"shares":[{"token":"aaa","backendMode":"proxy","backendProxyEndpoint":"http://localhost:3000","createdAt":7}]},
	  {"shares":[{"token":"bbb"}]}]}`), &ov); err != nil {
		t.Fatalf("overview fixture: %v", err)
	}
	app := mountShare(t, &fakeController{configuredV: true, tokenV: "tok", overviewV: ov})
	code, body := call(t, app, http.MethodGet, "/v1/share", "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var got sharesOut
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(got.Shares) != 2 {
		t.Fatalf("want 2 shares across 2 environments, got %d (%s)", len(got.Shares), body)
	}
	if got.Shares[0].Token != "aaa" || got.Shares[0].URL != "https://aaa.share.hanzo.ai" {
		t.Errorf("share[0] = %+v", got.Shares[0])
	}
	if got.Shares[0].Backend != "http://localhost:3000" || got.Shares[0].CreatedAt != 7 {
		t.Errorf("share[0] backend/createdAt = %+v", got.Shares[0])
	}
}
