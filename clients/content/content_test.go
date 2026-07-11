package content

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ---- unit: the ONE state machine (lifecycle.go) ----

func TestLifecycleTransitions(t *testing.T) {
	legal := [][2]string{
		{StatusDraft, StatusInReview}, {StatusDraft, StatusArchived},
		{StatusInReview, StatusApproved}, {StatusInReview, StatusDraft},
		{StatusApproved, StatusQueued}, {StatusApproved, StatusPublished}, {StatusApproved, StatusDraft},
		{StatusQueued, StatusPublished}, {StatusQueued, StatusApproved},
		{StatusPublished, StatusArchived}, {StatusPublished, StatusDraft},
		{StatusArchived, StatusDraft},
		{StatusDraft, StatusDraft}, // no-op is legal
	}
	for _, e := range legal {
		if !CanTransition(e[0], e[1]) {
			t.Errorf("expected %q → %q to be legal", e[0], e[1])
		}
	}
	illegal := [][2]string{
		{StatusDraft, StatusApproved}, {StatusDraft, StatusPublished}, {StatusDraft, StatusQueued},
		{StatusInReview, StatusQueued}, {StatusInReview, StatusPublished},
		{StatusQueued, StatusDraft}, {StatusArchived, StatusPublished},
		{StatusPublished, StatusApproved}, {"bogus", StatusDraft}, {StatusDraft, "bogus"},
	}
	for _, e := range illegal {
		if CanTransition(e[0], e[1]) {
			t.Errorf("expected %q → %q to be ILLEGAL", e[0], e[1])
		}
	}
}

func TestLifecycleInvariants(t *testing.T) {
	if !IsStatus(StatusDraft) || IsStatus("nope") {
		t.Fatal("IsStatus wrong")
	}
	if !IsLive(StatusPublished) || IsLive(StatusDraft) {
		t.Fatal("IsLive must be exactly published")
	}
	// Distribution fires on EXACTLY ONE edge — published. queued is staging (scheduled/
	// ready, not yet posted), so a legal walk approved→queued→published fans out once.
	if !entersDistribution(StatusPublished) {
		t.Fatal("entersDistribution must be true for published")
	}
	if entersDistribution(StatusQueued) || entersDistribution(StatusApproved) {
		t.Fatal("entersDistribution must be published-only (queued is staging, not a fan-out)")
	}
	// Every reachable target is itself a valid state (no dangling edge).
	for from, tos := range transitions {
		if !IsStatus(from) {
			t.Errorf("edge source %q is not a state", from)
		}
		for to := range tos {
			if !IsStatus(to) {
				t.Errorf("edge target %q (from %q) is not a state", to, from)
			}
		}
	}
	// The Select options render exactly the canonical states, in order.
	if got := statusOptions(); got != strings.Join(statuses, "\n") {
		t.Errorf("statusOptions drift: %q", got)
	}
	g := lifecycleGraph()
	if g.Initial != StatusDraft || g.Live != StatusPublished || len(g.States) != len(statuses) {
		t.Errorf("lifecycleGraph header wrong: %+v", g)
	}
}

// ---- unit: the before_save enforcement hook (hooks.go) ----

func TestEnforceLifecycleHook(t *testing.T) {
	doc := func(status string) *framework.Document { return &framework.Document{Data: map[string]any{"status": status}} }

	// Create (Prev nil): draft OK, anything else rejected.
	if err := enforceLifecycle(nil, &framework.Event{DocType: DocTypeCampaign, Doc: doc(StatusDraft)}); err != nil {
		t.Errorf("create-as-draft must pass: %v", err)
	}
	if err := enforceLifecycle(nil, &framework.Event{DocType: DocTypeCampaign, Doc: doc(StatusPublished)}); err == nil {
		t.Error("create-as-published must be rejected")
	}
	// Update: legal edge passes, illegal aborts, unknown status aborts, no-op passes.
	if err := enforceLifecycle(nil, &framework.Event{DocType: DocTypeCampaign, Prev: doc(StatusInReview), Doc: doc(StatusApproved)}); err != nil {
		t.Errorf("in_review→approved must pass: %v", err)
	}
	if err := enforceLifecycle(nil, &framework.Event{DocType: DocTypeCampaign, Prev: doc(StatusDraft), Doc: doc(StatusPublished)}); err == nil {
		t.Error("draft→published must abort")
	}
	if err := enforceLifecycle(nil, &framework.Event{DocType: DocTypeCampaign, Prev: doc(StatusDraft), Doc: doc("weird")}); err == nil {
		t.Error("unknown target status must abort")
	}
	if err := enforceLifecycle(nil, &framework.Event{DocType: DocTypeCampaign, Prev: doc(StatusApproved), Doc: doc(StatusApproved)}); err != nil {
		t.Errorf("no-op status write must pass: %v", err)
	}
}

// ---- integration: the loop end-to-end over the REAL framework store ----

func mountContent(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := framework.Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("framework.Mount: %v", err)
	}
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Domain: "api.test"}); err != nil {
		t.Fatalf("content.Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(); _ = framework.Shutdown() })
	return app
}

// call issues a request; user=="" omits the validated-principal header (the forge path).
func call(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	hr := httptest.NewRequest(method, path, r)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		hr.Header.Set("X-User-Id", user)
	}
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// req is the common validated-principal case: user "u_<org>".
func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	return call(t, app, method, path, org, "u_"+org, body)
}

func TestContentLoopEndToEnd(t *testing.T) {
	app := mountContent(t)
	const org = "acme"

	// Install the marketing module (seeds the caller as System Manager, trust-on-first-use).
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
		t.Fatalf("install marketing: %d %s", code, b)
	}

	// Lifecycle metadata is served and complete.
	if code, b := req(t, app, http.MethodGet, "/v1/content/lifecycle", org, nil); code != http.StatusOK ||
		!strings.Contains(string(b), StatusInReview) || !strings.Contains(string(b), StatusPublished) {
		t.Fatalf("lifecycle graph: %d %s", code, b)
	}

	// Create a Campaign through the framework surface (fires the before_save hook).
	code, b := req(t, app, http.MethodPost, "/v1/framework/Campaign", org, map[string]any{"title": "Spring Launch"})
	if code != http.StatusCreated {
		t.Fatalf("create campaign: %d %s", code, b)
	}
	var created struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(b, &created)
	if created.Name == "" || created.Status != StatusDraft {
		t.Fatalf("campaign not born as draft: %s", b)
	}
	name := created.Name

	// The board shows it in the draft column.
	if code, b := req(t, app, http.MethodGet, "/v1/content/board?status=draft", org, nil); code != http.StatusOK ||
		!strings.Contains(string(b), name) || !strings.Contains(string(b), "Spring Launch") {
		t.Fatalf("board(draft): %d %s", code, b)
	}

	tpath := "/v1/content/Campaign/" + name + "/transition"

	// An illegal jump is refused (409) without touching the document.
	if code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": StatusPublished}); code != http.StatusConflict {
		t.Fatalf("draft→published must be 409, got %d %s", code, b)
	}

	// Walk the legal path draft → in_review → approved → published.
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": to})
		if code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
		var tr TransitionResult
		_ = json.Unmarshal(b, &tr)
		if tr.To != to {
			t.Fatalf("transition →%s wrong result: %s", to, b)
		}
		// Entering published fans out to the (unconfigured) distributor — honestly
		// reported, never fatal.
		if to == StatusPublished {
			if tr.Distribution == nil || tr.Distribution.Status != "not_configured" {
				t.Fatalf("published must record not_configured distribution, got: %s", b)
			}
		}
	}

	// It now shows in the published column and left the draft column.
	if code, b := req(t, app, http.MethodGet, "/v1/content/board?status=published", org, nil); code != http.StatusOK ||
		!strings.Contains(string(b), name) {
		t.Fatalf("board(published): %d %s", code, b)
	}

	// generate honestly fail-closes (no real generator wired) — 503, never a 5xx crash.
	if code, _ := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{"doctype": DocTypeSocialPost}); code != http.StatusServiceUnavailable {
		t.Fatalf("generate must be 503 (not configured), got %d", code)
	}

	// A transition on an unknown item is 404 (framework.ErrNotFound), not a 5xx.
	if code, _ := req(t, app, http.MethodPost, "/v1/content/Campaign/nope/transition", org, map[string]any{"to": StatusInReview}); code != http.StatusNotFound {
		t.Fatalf("transition unknown item must be 404, got %d", code)
	}
	// An unknown content type is 404.
	if code, _ := req(t, app, http.MethodPost, "/v1/content/Widget/x/transition", org, map[string]any{"to": StatusInReview}); code != http.StatusNotFound {
		t.Fatalf("unknown content type must be 404, got %d", code)
	}
}

// TestContentRefusesForge proves every /v1/content route refuses an org header with NO
// validated principal (the anonymous cross-tenant forge) — 403 before any store access.
func TestContentRefusesForge(t *testing.T) {
	app := mountContent(t)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/content/board", nil},
		{http.MethodGet, "/v1/content/lifecycle", nil},
		{http.MethodGet, "/v1/content/channels", nil},
		{http.MethodPost, "/v1/content/generate", map[string]any{"doctype": DocTypeSocialPost}},
		{http.MethodPost, "/v1/content/Campaign/x/transition", map[string]any{"to": StatusInReview}},
	} {
		if code, _ := call(t, app, tc.method, tc.path, "victim", "", tc.body); code != http.StatusForbidden {
			t.Errorf("%s %s with forged org must be 403, got %d", tc.method, tc.path, code)
		}
	}
}

// TestBoardPerOrgIsolation proves org A's content never appears on org B's board.
func TestBoardPerOrgIsolation(t *testing.T) {
	app := mountContent(t)
	for _, org := range []string{"a", "b"} {
		if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
			t.Fatalf("install %s: %d %s", org, code, b)
		}
	}
	// Org A creates a SocialPost.
	code, b := req(t, app, http.MethodPost, "/v1/framework/SocialPost", "a", map[string]any{"title": "A-secret"})
	if code != http.StatusCreated {
		t.Fatalf("create in a: %d %s", code, b)
	}
	// A sees it; B does not.
	if _, b := req(t, app, http.MethodGet, "/v1/content/board", "a", nil); !strings.Contains(string(b), "A-secret") {
		t.Fatalf("org A must see its own content: %s", b)
	}
	if _, b := req(t, app, http.MethodGet, "/v1/content/board", "b", nil); strings.Contains(string(b), "A-secret") {
		t.Fatalf("CROSS-ORG LEAK: org B saw org A's content: %s", b)
	}
}
