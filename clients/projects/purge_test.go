package projects

// Tests for POST /v1/projects/:slug/purge — the dedicated edge cache purge that
// flushes a project's edge cache-tag WITHOUT a redeploy. They prove the contract:
// org-scoped (403 no principal, 404 wrong org / unknown slug), stamps LastPurgeAt,
// 200 even when the edge (CF) is unconfigured, and — critically — the S3 origin is
// never written or deleted (only the edge is flushed). Driven over HTTP through the
// REAL Mount + zip stack against the in-memory S3 double, exactly like the /v1/sites
// tests; CF is unconfigured in this harness (no CF_API_TOKEN/CF_ZONE_ID), so the
// purge is a warn-only no-op that must still succeed.

import (
	"net/http"
	"testing"
)

// TestPurge_StampsLastPurgeAt_DraftProject: a never-deployed project (LastPurgeAt
// == 0) purges to 200 and returns a non-zero lastPurgeAt. CF is unconfigured, so
// this also proves an unconfigured edge purge is non-fatal.
func TestPurge_StampsLastPurgeAt_DraftProject(t *testing.T) {
	startFakeS3(t)
	bs := &billServer{available: 1000000}
	app := mountSites(t, &fakeAI{content: okManifest()}, bs.start(t))

	if code, _ := doSite(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Draft Flush", "slug": "draftflush"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}

	code, body := doSite(t, app, http.MethodPost, "/v1/projects/draftflush/purge", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("purge want 200 (unconfigured edge is non-fatal), got %d (%s)", code, body)
	}
	var pv struct {
		Slug        string `json:"slug"`
		LastPurgeAt int64  `json:"lastPurgeAt"`
	}
	mustJSON(t, body, &pv)
	if pv.Slug != "draftflush" || pv.LastPurgeAt <= 0 {
		t.Fatalf("purge must stamp lastPurgeAt: %+v", pv)
	}
}

// TestPurge_S3OriginUntouched: purging a LIVE deployed project flushes the edge but
// never writes or deletes the S3 origin — the exact stored bytes and the object
// count under the site prefix are unchanged, and no new PUT is issued.
func TestPurge_S3OriginUntouched(t *testing.T) {
	f := startFakeS3(t)
	bs := &billServer{available: 1000000}
	app := mountSites(t, &fakeAI{content: okManifest()}, bs.start(t))

	if url := deployVia(t, app, "acme", "flushme"); url != "https://flushme.hanzo.app" {
		t.Fatalf("deploy url=%q", url)
	}
	const prefix = "hanzo-sites/acme/flushme/"
	putsBefore := f.puts
	countBefore := f.count(prefix)
	f.mu.Lock()
	indexBefore := string(f.objects[prefix+"index.html"])
	f.mu.Unlock()
	if countBefore == 0 || indexBefore == "" {
		t.Fatalf("precondition: site not written to S3 (count=%d)", countBefore)
	}

	code, body := doSite(t, app, http.MethodPost, "/v1/projects/flushme/purge", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("purge want 200, got %d (%s)", code, body)
	}
	var pv struct {
		LastPurgeAt int64 `json:"lastPurgeAt"`
	}
	mustJSON(t, body, &pv)
	if pv.LastPurgeAt <= 0 {
		t.Fatalf("purge must stamp lastPurgeAt, got %d", pv.LastPurgeAt)
	}

	if f.puts != putsBefore {
		t.Fatalf("purge must not write S3: puts %d → %d", putsBefore, f.puts)
	}
	if got := f.count(prefix); got != countBefore {
		t.Fatalf("purge must not add/remove S3 objects: count %d → %d", countBefore, got)
	}
	f.mu.Lock()
	indexAfter := string(f.objects[prefix+"index.html"])
	f.mu.Unlock()
	if indexAfter != indexBefore {
		t.Fatal("purge must not modify the S3 origin object")
	}
}

// TestPurge_OrgScoped: no principal → 403; a different org or an unknown slug → 404.
// The tenant boundary is the gateway-minted X-Org-Id, exactly like every other
// projects route.
func TestPurge_OrgScoped(t *testing.T) {
	startFakeS3(t)
	bs := &billServer{available: 1000000}
	app := mountSites(t, &fakeAI{content: okManifest()}, bs.start(t))

	deployVia(t, app, "acme", "flushme") // owned by acme

	if code, _ := doSite(t, app, http.MethodPost, "/v1/projects/flushme/purge", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal purge want 403, got %d", code)
	}
	if code, _ := doSite(t, app, http.MethodPost, "/v1/projects/flushme/purge", "other", nil); code != http.StatusNotFound {
		t.Fatalf("wrong-org purge want 404, got %d", code)
	}
	if code, _ := doSite(t, app, http.MethodPost, "/v1/projects/nope/purge", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("unknown-slug purge want 404, got %d", code)
	}
}
