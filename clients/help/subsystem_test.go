package help

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountPublic mounts the framework engine + the /v1/help public plane, pinning the
// public-center org via the env override so the test is hermetic (independent of any
// ambient CLOUD_HELP_PUBLIC_ORG or brand). An empty publicOrg exercises fail-closed.
func mountPublic(t *testing.T, publicOrg string) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_HELP_PUBLIC_ORG", publicOrg)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := framework.Mount(app, deps); err != nil {
		t.Fatalf("mount framework: %v", err)
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("mount help: %v", err)
	}
	t.Cleanup(func() { _ = framework.Shutdown() })
	return app
}

// seedArticle installs help in org (owner-seeding the caller as System Manager) and
// creates one article through the generic role-gated surface — the agent authoring
// path — so the public-plane read tests read real, agent-authored data.
func seedArticle(t *testing.T, app *zip.App, org, slug, title, status string, public bool) {
	t.Helper()
	call(t, app, http.MethodPost, "/v1/framework/modules/help/install", org, nil)
	body := map[string]any{"title": title, "slug": slug, "status": status, "is_public": public, "body": "BODY:" + slug}
	if code, raw := call(t, app, http.MethodPost, "/v1/framework/"+DTArticle, org, body); code != http.StatusCreated {
		t.Fatalf("seed article %q want 201, got %d (%s)", slug, code, raw)
	}
}

// anon issues an UNAUTHENTICATED request to the public plane, optionally with a
// spoofed X-Org-Id/X-User-Id to prove the plane ignores client-supplied identity.
func anon(t *testing.T, app *zip.App, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func dataArray(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var res struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode {data:[]}: %v (%s)", err, raw)
	}
	return res.Data
}

// TestPublicKB_PublishedOnly proves the public knowledge base exposes ONLY
// status=Published AND is_public=1 articles — a Draft or an internal (agent-only)
// article never appears in the list and 404s on a direct fetch.
func TestPublicKB_PublishedOnly(t *testing.T) {
	const org = "acme"
	app := mountPublic(t, org)
	seedArticle(t, app, org, "getting-started", "Getting Started", "Published", true) // visible
	seedArticle(t, app, org, "internal-runbook", "Runbook", "Published", false)       // published but PRIVATE
	seedArticle(t, app, org, "draft-note", "Draft Note", "Draft", true)               // public flag but DRAFT

	code, raw := anon(t, app, http.MethodGet, "/v1/help/articles", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("list articles want 200, got %d (%s)", code, raw)
	}
	list := dataArray(t, raw)
	if len(list) != 1 || list[0]["slug"] != "getting-started" {
		t.Fatalf("public KB must show ONLY the published+public article, got %+v", list)
	}
	// list projection must NOT carry the body (light card).
	if _, hasBody := list[0]["body"]; hasBody {
		t.Fatalf("list card must not include body, got %+v", list[0])
	}

	// The visible article: detail has the body.
	if code, raw := anon(t, app, http.MethodGet, "/v1/help/articles/getting-started", nil, nil); code != http.StatusOK {
		t.Fatalf("get public article want 200, got %d (%s)", code, raw)
	} else {
		var d map[string]any
		_ = json.Unmarshal(raw, &d)
		if d["body"] != "BODY:getting-started" {
			t.Fatalf("public article detail must include body, got %+v", d)
		}
	}

	// The private and the draft articles: 404 on direct fetch (fail-closed).
	for _, slug := range []string{"internal-runbook", "draft-note"} {
		if code, _ := anon(t, app, http.MethodGet, "/v1/help/articles/"+slug, nil, nil); code != http.StatusNotFound {
			t.Fatalf("non-public article %q must 404 on direct fetch, got %d", slug, code)
		}
	}
}

// TestPublicKB_CrossTenantIsolation proves the public plane serves EXACTLY the
// configured public org — never another tenant's data — and that a spoofed
// X-Org-Id/X-User-Id is ignored (the org is server-fixed, not client-chosen).
func TestPublicKB_CrossTenantIsolation(t *testing.T) {
	const pub = "acme"
	app := mountPublic(t, pub)
	seedArticle(t, app, pub, "acme-doc", "Acme Doc", "Published", true)
	// A DIFFERENT tenant with its OWN published+public article — must never surface.
	seedArticle(t, app, "evil", "evil-doc", "Evil Doc", "Published", true)

	// Spoofed identity headers pointing at the other tenant must be IGNORED.
	spoof := map[string]string{"X-Org-Id": "evil", "X-User-Id": "u_evil"}
	code, raw := anon(t, app, http.MethodGet, "/v1/help/articles", nil, spoof)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, raw)
	}
	list := dataArray(t, raw)
	if len(list) != 1 || list[0]["slug"] != "acme-doc" {
		t.Fatalf("public plane must serve ONLY the configured org (acme), never spoofed evil; got %+v", list)
	}
	// evil's article, by its real slug, must 404 on the public plane (wrong tenant).
	if code, _ := anon(t, app, http.MethodGet, "/v1/help/articles/evil-doc", nil, spoof); code != http.StatusNotFound {
		t.Fatalf("cross-tenant article must 404, got %d", code)
	}
}

// TestPublicIntake_CreatesTicketAndThread proves an anonymous customer submission
// lands a ticket (Open, source portal, customer email) in the public org AND an
// opening conversation message linked to it — verified through the agent surface.
func TestPublicIntake_CreatesTicketAndThread(t *testing.T) {
	const org = "acme"
	app := mountPublic(t, org)
	// Install help in the public org (owner-seed the agent identity used below to verify).
	call(t, app, http.MethodPost, "/v1/framework/modules/help/install", org, nil)

	code, raw := anon(t, app, http.MethodPost, "/v1/help/tickets", map[string]any{
		"subject": "Cannot sign in", "email": "bob@example.com", "description": "the login button does nothing",
	}, nil)
	if code != http.StatusCreated {
		t.Fatalf("file ticket want 201, got %d (%s)", code, raw)
	}
	var res struct {
		Ticket string `json:"ticket"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.Ticket == "" || res.Status != "Open" {
		t.Fatalf("intake must return the new ticket id + Open status, got %+v", res)
	}

	// Verify via the agent (generic) surface: the ticket is Open, portal-sourced, and
	// carries the customer email + message.
	_, traw := call(t, app, http.MethodGet, "/v1/framework/"+DTTicket+"/"+res.Ticket, org, nil)
	var tk map[string]any
	_ = json.Unmarshal(traw, &tk)
	if tk["status"] != "Open" || tk["source"] != "portal" || tk["customer"] != "bob@example.com" {
		t.Fatalf("filed ticket must be Open/portal/bob, got %+v", tk)
	}

	// And an opening conversation message linked to that ticket, from the customer.
	_, craw := call(t, app, http.MethodGet, `/v1/framework/`+DTCommunication+`?filters={"ticket":"`+res.Ticket+`"}`, org, nil)
	comms := dataArray(t, craw)
	if len(comms) != 1 || comms[0]["sender_type"] != "customer" || comms[0]["sender"] != "bob@example.com" {
		t.Fatalf("intake must record ONE opening customer message, got %+v", comms)
	}
}

// TestPublicIntake_Validation proves the intake rejects a missing subject or email
// with 400 (an unauthenticated write must validate its input).
func TestPublicIntake_Validation(t *testing.T) {
	const org = "acme"
	app := mountPublic(t, org)
	call(t, app, http.MethodPost, "/v1/framework/modules/help/install", org, nil)

	if code, _ := anon(t, app, http.MethodPost, "/v1/help/tickets", map[string]any{"email": "x@y.z"}, nil); code != http.StatusBadRequest {
		t.Fatalf("missing subject want 400, got %d", code)
	}
	if code, _ := anon(t, app, http.MethodPost, "/v1/help/tickets", map[string]any{"subject": "hi"}, nil); code != http.StatusBadRequest {
		t.Fatalf("missing email want 400, got %d", code)
	}
}

// TestPublicIntake_FailsClosedNotConfigured proves that when the public org has NOT
// installed the help module, intake fails closed with 503 (not a 500, not a silent
// success) — the desk is simply not configured for that org.
func TestPublicIntake_FailsClosedNotConfigured(t *testing.T) {
	app := mountPublic(t, "acme") // public org set, but help NEVER installed in it
	code, _ := anon(t, app, http.MethodPost, "/v1/help/tickets", map[string]any{
		"subject": "hi", "email": "x@y.z", "description": "hello",
	}, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("intake with help not installed want 503, got %d", code)
	}
}

// TestPublicPlane_FailsClosedNoOrg proves that with NO public org resolved (no env
// override, no brand) the whole public plane is inert: reads and intake 404, so
// nothing is ever exposed until an operator names the org.
func TestPublicPlane_FailsClosedNoOrg(t *testing.T) {
	app := mountPublic(t, "") // env pinned empty + deps.Brand empty → publicOrg ""
	if code, _ := anon(t, app, http.MethodGet, "/v1/help/articles", nil, nil); code != http.StatusNotFound {
		t.Fatalf("articles with no public org want 404, got %d", code)
	}
	if code, _ := anon(t, app, http.MethodGet, "/v1/help/categories", nil, nil); code != http.StatusNotFound {
		t.Fatalf("categories with no public org want 404, got %d", code)
	}
	if code, _ := anon(t, app, http.MethodPost, "/v1/help/tickets", map[string]any{"subject": "hi", "email": "x@y.z"}, nil); code != http.StatusNotFound {
		t.Fatalf("intake with no public org want 404, got %d", code)
	}
}

// TestPublicIntake_RateLimited proves the unauthenticated intake is per-IP
// rate-limited: past the limit, submissions are rejected with 429 (spam/DoS guard).
func TestPublicIntake_RateLimited(t *testing.T) {
	const org = "acme"
	app := mountPublic(t, org)
	call(t, app, http.MethodPost, "/v1/framework/modules/help/install", org, nil)

	saw429 := false
	first := 0
	for i := 0; i < intakeRateLimit+5; i++ {
		code, _ := anon(t, app, http.MethodPost, "/v1/help/tickets", map[string]any{
			"subject": "spam", "email": "x@y.z", "description": "flood",
		}, nil)
		if i == 0 {
			first = code
		}
		if code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if first != http.StatusCreated {
		t.Fatalf("first intake want 201, got %d", first)
	}
	if !saw429 {
		t.Fatalf("intake past the per-IP limit (%d) must 429", intakeRateLimit)
	}
}
