package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// ── sync windows ─────────────────────────────────────────────────────────────

// TestDashSyncWindows_TenantScoped: a caller reads the permissive-empty sync-window
// state for its OWN app; a cross-tenant name 404s (no oracle); a SuperAdmin reads the
// fleet; an unvalidated caller 403s; the caller's own app NEVER 404s.
func TestDashSyncWindows_TenantScoped(t *testing.T) {
	s := twoTenantFleet()

	// own app → 200 with the exact permissive-empty shape.
	body := jsonBody(t, getAs(t, s, "/v1/deploy/applications/acme-web/syncwindows", orgHeaders("acme")))
	if body["canSync"] != true {
		t.Fatalf("canSync = %v, want true", body["canSync"])
	}
	if v, present := body["activeWindows"]; !present || v != nil {
		t.Fatalf("activeWindows = %v (present=%v), want null", v, present)
	}
	if v, present := body["assignedWindows"]; !present || v != nil {
		t.Fatalf("assignedWindows = %v (present=%v), want null", v, present)
	}

	// cross-tenant name → 404 (bravo cannot read acme's app's sync windows).
	if r := getAs(t, s, "/v1/deploy/applications/acme-web/syncwindows", orgHeaders("bravo")); r.StatusCode != http.StatusNotFound {
		t.Fatalf("bravo→acme syncwindows = %d, want 404", r.StatusCode)
		_ = r.Body.Close()
	}

	// SuperAdmin reads a fleet app.
	if r := getAs(t, s, "/v1/deploy/applications/cloud/syncwindows", map[string]string{"X-User-IsAdmin": "true"}); r.StatusCode != http.StatusOK {
		t.Fatalf("admin cloud syncwindows = %d, want 200", r.StatusCode)
		_ = r.Body.Close()
	}

	// unvalidated / forged org → 403 (fail closed), and empty org too.
	for _, h := range []map[string]string{{"X-Org-Id": "acme"}, {"X-User-Id": "u"}} {
		if r := getAs(t, s, "/v1/deploy/applications/acme-web/syncwindows", h); r.StatusCode != http.StatusForbidden {
			t.Fatalf("unvalidated syncwindows (%v) = %d, want 403", h, r.StatusCode)
			_ = r.Body.Close()
		}
	}
}

// ── revision metadata ────────────────────────────────────────────────────────

// TestDashRevisionMetadata_TenantScoped: a caller reads honest minimal metadata for its
// OWN app's revision (date always populated, message = the revision; HEAD → the declared
// tag); cross-tenant 404s; SuperAdmin reads the fleet; unvalidated 403s; NEVER 404 for the
// caller's own app.
func TestDashRevisionMetadata_TenantScoped(t *testing.T) {
	s := twoTenantFleet()

	// own app, explicit revision → message echoes the revision, date is populated.
	body := jsonBody(t, getAs(t, s, "/v1/deploy/applications/acme-web/revisions/sha-abc123/metadata", orgHeaders("acme")))
	if body["date"] == nil || body["date"] == "" {
		t.Fatalf("date = %v, want a non-empty models.Time", body["date"])
	}
	if body["message"] != "sha-abc123" {
		t.Fatalf("message = %v, want the revision sha-abc123", body["message"])
	}

	// HEAD → message resolves to the CR's declared image tag ("v1" from the fixture).
	head := jsonBody(t, getAs(t, s, "/v1/deploy/applications/acme-web/revisions/HEAD/metadata", orgHeaders("acme")))
	if head["message"] != "v1" {
		t.Fatalf("HEAD message = %v, want the declared tag v1", head["message"])
	}

	// cross-tenant → 404.
	if r := getAs(t, s, "/v1/deploy/applications/acme-web/revisions/HEAD/metadata", orgHeaders("bravo")); r.StatusCode != http.StatusNotFound {
		t.Fatalf("bravo→acme revision metadata = %d, want 404", r.StatusCode)
		_ = r.Body.Close()
	}

	// SuperAdmin reads a fleet app; unvalidated fails closed.
	if r := getAs(t, s, "/v1/deploy/applications/cloud/revisions/HEAD/metadata", map[string]string{"X-User-IsAdmin": "true"}); r.StatusCode != http.StatusOK {
		t.Fatalf("admin cloud revision metadata = %d, want 200", r.StatusCode)
		_ = r.Body.Close()
	}
	if r := getAs(t, s, "/v1/deploy/applications/acme-web/revisions/HEAD/metadata", map[string]string{"X-Org-Id": "acme"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("unvalidated revision metadata = %d, want 403", r.StatusCode)
		_ = r.Body.Close()
	}
}

// ── live resource-tree stream ────────────────────────────────────────────────

// TestDashStreamResourceTree_ScopeGateBeforeEmission: the scope gate runs BEFORE any SSE is
// opened — an unvalidated caller 403s and a cross-tenant name 404s, both as plain error
// responses (SendStreamWriter is never reached, so nothing is emitted).
func TestDashStreamResourceTree_ScopeGateBeforeEmission(t *testing.T) {
	s := twoTenantFleet()

	// forged org (no validated principal) → 403, no stream.
	if r := getAs(t, s, "/v1/deploy/stream/applications/acme-web/resource-tree", map[string]string{"X-Org-Id": "acme"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("forged-org tree stream = %d, want 403", r.StatusCode)
		_ = r.Body.Close()
	}
	// cross-tenant name → 404, no stream.
	if r := getAs(t, s, "/v1/deploy/stream/applications/acme-web/resource-tree", orgHeaders("bravo")); r.StatusCode != http.StatusNotFound {
		t.Fatalf("bravo→acme tree stream = %d, want 404", r.StatusCode)
		_ = r.Body.Close()
	}
}

// TestStreamResourceTree_EmitsTreeOnce: the stream core emits the ApplicationTree as a
// `data: {"result": …}` frame (the envelope the SPA unwraps), then returns on ctx cancel.
func TestStreamResourceTree_EmitsTreeOnce(t *testing.T) {
	s := twoTenantFleet()
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		streamResourceTree(s, ctx, "tenant-acme", "acme-web", w)
		close(done)
	}()
	time.Sleep(40 * time.Millisecond) // let the initial synchronous emit land
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamResourceTree did not return within 2s of context cancel (leak)")
	}

	frames := parseSSE(buf.String())
	if len(frames) < 1 {
		t.Fatalf("no tree frame emitted: %q", buf.String())
	}
	var env struct {
		Result argoTree `json:"result"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &env); err != nil {
		t.Fatalf("frame is not a {result:tree} envelope: %q (%v)", frames[0], err)
	}
	// The ApplicationTree shape the SPA renders: nodes/orphanedNodes/hosts marshal as arrays.
	b, _ := json.Marshal(env.Result)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["nodes"].([]any); !ok {
		t.Fatalf("tree.nodes must be a JSON array, got %T", m["nodes"])
	}
	if _, ok := m["orphanedNodes"]; !ok {
		t.Fatalf("tree.orphanedNodes must be present: %v", m)
	}
}

// TestStreamResourceTree_HonorsCancel: the stream returns promptly on context cancel — no
// hung goroutine holding the keep-alive loop open.
func TestStreamResourceTree_HonorsCancel(t *testing.T) {
	s := twoTenantFleet()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		streamResourceTree(s, ctx, "tenant-acme", "acme-web", bufio.NewWriter(&bytes.Buffer{}))
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamResourceTree did not return within 2s of cancel")
	}
}

// TestRevisionMetadataOf_Honest: the pure builder always populates date, echoes the
// revision as message, and resolves HEAD to the declared image tag — never fabricating an
// author.
func TestRevisionMetadataOf_Honest(t *testing.T) {
	cr := orgAppCR("tenant-acme", "acme-web", "acme", "storefront")
	rm := revisionMetadataOf(cr, "sha-deadbeef")
	if rm.Date == "" {
		t.Fatal("date must always be populated (models.Time is required)")
	}
	if rm.Message != "sha-deadbeef" {
		t.Fatalf("message = %q, want the revision", rm.Message)
	}
	if rm.Author != "" {
		t.Fatalf("author = %q, want empty (no fabricated git author)", rm.Author)
	}
	// HEAD resolves to the declared image tag.
	if got := revisionMetadataOf(cr, "HEAD"); got.Message != "v1" {
		t.Fatalf("HEAD message = %q, want the declared tag v1", got.Message)
	}
	// over-long revision is bounded.
	long := make([]byte, maxRevisionLen+50)
	for i := range long {
		long[i] = 'a'
	}
	if got := revisionMetadataOf(cr, string(long)); len(got.Message) != maxRevisionLen {
		t.Fatalf("message len = %d, want bounded to %d", len(got.Message), maxRevisionLen)
	}
}
