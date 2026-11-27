package projects

// objects_test.go pins what an incremental publish stands on: the listing a
// publisher diffs against is the store's own record of exactly one deployment's
// prefix, and a completion never marks a release live while a key of its
// manifest is absent from the store — whether the publisher uploaded it, asked
// for it to be copied, or skipped it by mistake.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// started creates site slug for org, opens a deployment on it and answers the
// deployment its 202 reports.
func started(t *testing.T, app *zip.App, org, slug string) projectsDeployment {
	t.Helper()
	if code, b := do(t, app, http.MethodPost, "/v1/project", org, map[string]any{"name": slug, "slug": slug}); code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", slug, code, b)
	}
	code, b := do(t, app, http.MethodPost, "/v1/project/"+slug+"/deployments", org, map[string]any{})
	if code != http.StatusAccepted {
		t.Fatalf("start %s: %d %s", slug, code, b)
	}
	var d projectsDeployment
	if err := json.Unmarshal(b, &d); err != nil || d.ID == "" || d.Prefix == "" || d.Bucket == "" {
		t.Fatalf("decode deployment: %v (%s)", err, b)
	}
	return d
}

// statusOf reads a deployment back and answers its status.
func statusOf(t *testing.T, app *zip.App, org, slug, id string) string {
	t.Helper()
	code, b := do(t, app, http.MethodGet, "/v1/project/"+slug+"/deployments/"+id, org, nil)
	if code != http.StatusOK {
		t.Fatalf("read deployment: %d %s", code, b)
	}
	var d projectsDeployment
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode deployment: %v (%s)", err, b)
	}
	return d.Status
}

func complete(t *testing.T, app *zip.App, org, slug, id string, body map[string]any) (int, string) {
	t.Helper()
	code, b := do(t, app, http.MethodPost, "/v1/project/"+slug+"/deployments/"+id+"/complete", org, body)
	return code, string(b)
}

// TestObjectsListOneDeploymentsPrefix: the listing is the deployment's own
// prefix and nothing beside it — not a sibling site whose name extends this one,
// not another org — with keys relative to the prefix and the size and MD5 the
// store holds.
func TestObjectsListOneDeploymentsPrefix(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		d := started(t, app, "acme", "atlas")
		other := started(t, app, "other", "elsewhere")
		path := "/v1/project/atlas/deployments/" + d.ID + "/objects"

		// Nothing uploaded yet: an empty array, never null.
		code, b := do(t, app, http.MethodGet, path, "acme", nil)
		if code != http.StatusOK || strings.TrimSpace(string(b)) != `{"objects":[]}` {
			t.Fatalf("empty prefix: %d %s, want 200 {\"objects\":[]}", code, b)
		}

		f.seed(d.Bucket, d.Prefix+"/index.html", "<h1>hi</h1>")
		f.seed(d.Bucket, d.Prefix+"/_next/static/a.js", "console.log(1)")
		f.seed(d.Bucket, "acme/atlas-two/index.html", "sibling")
		f.seed(other.Bucket, other.Prefix+"/index.html", "theirs")

		code, b = do(t, app, http.MethodGet, path, "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %s", code, b)
		}
		var got projectsObjects
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("decode: %v (%s)", err, b)
		}
		want := []projectsObject{
			{Key: "_next/static/a.js", Size: int64(len("console.log(1)")), ETag: etagOf([]byte("console.log(1)"))},
			{Key: "index.html", Size: int64(len("<h1>hi</h1>")), ETag: etagOf([]byte("<h1>hi</h1>"))},
		}
		if len(got.Objects) != len(want) {
			t.Fatalf("objects = %+v, want %+v", got.Objects, want)
		}
		for i := range want {
			if got.Objects[i] != want[i] {
				t.Errorf("object %d = %+v, want %+v", i, got.Objects[i], want[i])
			}
		}

		// Another tenant cannot list this deployment by its slug, nor reach it by its
		// id through a site of its own.
		if code, b := do(t, app, http.MethodGet, path, "other", nil); code != http.StatusNotFound {
			t.Fatalf("another org's listing: %d %s, want 404", code, b)
		}
		if code, b := do(t, app, http.MethodGet, "/v1/project/elsewhere/deployments/"+d.ID+"/objects", "other", nil); code != http.StatusNotFound {
			t.Fatalf("a foreign deployment id: %d %s, want 404", code, b)
		}
	})
}

// TestCompletionRefusesAReleaseWithAMissingKey: a live completion whose
// manifest names a key the store does not hold is a 409 that leaves the
// deployment queued and the prefix untouched; once the key is uploaded the same
// deployment completes, and the reconcile prunes what the manifest left out.
func TestCompletionRefusesAReleaseWithAMissingKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		d := started(t, app, "acme", "atlas")
		f.seed(d.Bucket, d.Prefix+"/index.html", "index")
		f.seed(d.Bucket, d.Prefix+"/app.js", "app")
		f.seed(d.Bucket, d.Prefix+"/stale.html", "stale")
		keys := []string{"index.html", "/app.js", "style.css"}

		code, body := complete(t, app, "acme", "atlas", d.ID, map[string]any{"status": "live", "keys": keys})
		if code != http.StatusConflict {
			t.Fatalf("a release missing a key: %d %s, want 409", code, body)
		}
		if !strings.Contains(body, "style.css") || !strings.Contains(body, "1 of") {
			t.Errorf("the refusal does not say what is missing: %s", body)
		}
		if s := statusOf(t, app, "acme", "atlas", d.ID); s != "queued" {
			t.Fatalf("a refused release left the deployment %q, want queued", s)
		}
		if _, ok := f.body(d.Bucket, d.Prefix+"/stale.html"); !ok {
			t.Fatal("a refused release deleted from the prefix")
		}

		f.seed(d.Bucket, d.Prefix+"/style.css", "css")
		code, body = complete(t, app, "acme", "atlas", d.ID, map[string]any{"status": "live", "keys": keys})
		if code != http.StatusOK {
			t.Fatalf("the whole release: %d %s, want 200", code, body)
		}
		if s := statusOf(t, app, "acme", "atlas", d.ID); s != "live" {
			t.Fatalf("status = %q, want live", s)
		}
		if _, ok := f.body(d.Bucket, d.Prefix+"/stale.html"); ok {
			t.Error("a key the manifest left out still serves")
		}
		for _, k := range []string{"index.html", "app.js", "style.css"} {
			if _, ok := f.body(d.Bucket, d.Prefix+"/"+k); !ok {
				t.Errorf("%s was pruned but the manifest names it", k)
			}
		}
	})
}

// TestCompletionCopiesFromASiblingSite: the keys a completion asks to have
// copied arrive from the named site of the same org before the release is
// checked, and a copy the completion cannot ask for is refused with the
// deployment still queued.
func TestCompletionCopiesFromASiblingSite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		one := started(t, app, "acme", "one")
		two := started(t, app, "acme", "two")
		f.seed(one.Bucket, one.Prefix+"/index.html", "ONE")
		f.seed(one.Bucket, one.Prefix+"/shared.js", "SHARED")
		f.seed(two.Bucket, two.Prefix+"/index.html", "TWO")
		keys := []string{"index.html", "shared.js", "a/../b.js"}

		for _, c := range []struct {
			name string
			body map[string]any
			want int
		}{
			{"a copy key keys does not name", map[string]any{"status": "live", "keys": keys, "copyFrom": "one", "copy": []string{"other.js"}}, http.StatusBadRequest},
			{"a copy key that is not a clean path", map[string]any{"status": "live", "keys": keys, "copyFrom": "one", "copy": []string{"a/../b.js"}}, http.StatusBadRequest},
			{"a site the org does not hold", map[string]any{"status": "live", "keys": keys, "copyFrom": "nope", "copy": []string{"shared.js"}}, http.StatusNotFound},
		} {
			if code, body := complete(t, app, "acme", "two", two.ID, c.body); code != c.want {
				t.Errorf("%s: %d %s, want %d", c.name, code, body, c.want)
			}
		}
		if n := f.copyCount(); n != 0 {
			t.Fatalf("a refused completion copied %d objects", n)
		}
		if s := statusOf(t, app, "acme", "two", two.ID); s != "queued" {
			t.Fatalf("a refused completion left the deployment %q, want queued", s)
		}

		keys = []string{"index.html", "shared.js"}
		code, body := complete(t, app, "acme", "two", two.ID, map[string]any{"status": "live", "keys": keys, "copyFrom": "one", "copy": []string{"shared.js"}})
		if code != http.StatusOK {
			t.Fatalf("complete with a copy: %d %s, want 200", code, body)
		}
		if got, _ := f.body(two.Bucket, two.Prefix+"/shared.js"); got != "SHARED" {
			t.Errorf("copied body = %q, want SHARED", got)
		}
		if got, _ := f.body(two.Bucket, two.Prefix+"/index.html"); got != "TWO" {
			t.Errorf("the uploaded page = %q, want TWO: a copy overwrote what the publisher sent", got)
		}
		if s := statusOf(t, app, "acme", "two", two.ID); s != "live" {
			t.Fatalf("status = %q, want live", s)
		}
	})
}

// TestCompletionKeepsRecentImmutableKeys: a release that drops a fingerprinted
// asset leaves it in place for graceAge, so a tab opened on the previous
// release can still lazy-load it; an older one, and any page however recent, is
// deleted. The immutable keys the release names are re-stamped once they are
// older than touchAge, without their bytes or content type moving, so
// LastModified keeps meaning "last named by a live release".
func TestCompletionKeepsRecentImmutableKeys(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		d := started(t, app, "acme", "atlas")
		now := time.Now()
		at := func(rel, ct string, age time.Duration) {
			f.seedAt(d.Bucket, d.Prefix+"/"+rel, "bytes of "+rel, ct, now.Add(-age))
		}
		at("index.html", "text/html", time.Hour)
		at("_next/static/named-20h.js", "application/javascript", 20*time.Hour)
		at("_next/static/named-1h.js", "application/javascript", time.Hour)
		at("_next/static/dropped-1h.js", "application/javascript", time.Hour)
		at("_next/static/dropped-40h.js", "application/javascript", 40*time.Hour)
		at("old.html", "text/html", time.Minute)
		young, _ := f.stamp(d.Bucket, d.Prefix+"/_next/static/named-1h.js")

		keys := []string{"index.html", "_next/static/named-20h.js", "_next/static/named-1h.js"}
		if code, body := complete(t, app, "acme", "atlas", d.ID, map[string]any{"status": "live", "keys": keys}); code != http.StatusOK {
			t.Fatalf("complete: %d %s", code, body)
		}

		for rel, want := range map[string]bool{
			"_next/static/dropped-1h.js":  true,  // inside the grace: a previous tab may still ask
			"_next/static/dropped-40h.js": false, // past it
			"old.html":                    false, // a page is never immutable
			"index.html":                  true,
		} {
			if _, ok := f.body(d.Bucket, d.Prefix+"/"+rel); ok != want {
				t.Errorf("%s present = %v, want %v", rel, ok, want)
			}
		}

		stamped, ct := f.stamp(d.Bucket, d.Prefix+"/_next/static/named-20h.js")
		if now.Sub(stamped) > time.Minute {
			t.Errorf("a named immutable key 20h old was not re-stamped: LastModified %v", stamped)
		}
		if ct != "application/javascript" {
			t.Errorf("re-stamping changed the content type to %q", ct)
		}
		if b, _ := f.body(d.Bucket, d.Prefix+"/_next/static/named-20h.js"); b != "bytes of _next/static/named-20h.js" {
			t.Errorf("re-stamping changed the bytes to %q", b)
		}
		if got, _ := f.stamp(d.Bucket, d.Prefix+"/_next/static/named-1h.js"); !got.Equal(young) {
			t.Errorf("a named immutable key 1h old was re-stamped: %v, was %v", got, young)
		}
		if n := f.copyCount(); n != 1 {
			t.Errorf("copies = %d, want the one re-stamp", n)
		}
	})
}

// TestNoStoreProvesNothing: with no object store configured the listing is a 503,
// and so is a live completion that names keys — it cannot be proven whole, so it
// is not marked live.
func TestNoStoreProvesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// A production process with no object store configured refuses to open one.
		t.Setenv("CLOUD_ENV", "mainnet")
		t.Setenv("S3_ADMIN_ACCESS_KEY", "")
		t.Setenv("S3_ADMIN_SECRET_KEY", "")
		app := mountApp(t)
		d := started(t, app, "acme", "atlas")

		if code, b := do(t, app, http.MethodGet, "/v1/project/atlas/deployments/"+d.ID+"/objects", "acme", nil); code != http.StatusServiceUnavailable {
			t.Fatalf("list with no store: %d %s, want 503", code, b)
		}
		code, body := complete(t, app, "acme", "atlas", d.ID, map[string]any{"status": "live", "keys": []string{"index.html"}})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("complete with no store: %d %s, want 503", code, body)
		}
		if s := statusOf(t, app, "acme", "atlas", d.ID); s != "queued" {
			t.Fatalf("an unproven release left the deployment %q, want queued", s)
		}
	})
}

// TestCompletionRetainsNestedAndModernFingerprintedChunks pins the 36-hour
// grace window for dropped fingerprinted chunks across Vite/esbuild/Next/Astro/
// SvelteKit — including nested mounts such as pay/assets/index-DeKYn5x6.js and
// sandbox/assets/index-DPbK6gD1.js that previously missed root-only prefix and
// hex-only regex checks and immediately 404'd open tabs on deploy.
func TestCompletionRetainsNestedAndModernFingerprintedChunks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		d := started(t, app, "hanzo", "hanzo-ai")
		now := time.Now()
		at := func(rel string, age time.Duration) {
			f.seedAt(d.Bucket, d.Prefix+"/"+rel, "bytes of "+rel, "text/javascript; charset=utf-8", now.Add(-age))
		}

		// New release files
		at("index.html", time.Minute)
		at("pay/index.html", time.Minute)
		at("pay/assets/index-NEW12345.js", time.Minute)

		// Dropped recent fingerprinted chunks across bundlers & nested mounts (must stay for 36h)
		recentChunks := []string{
			"pay/assets/index-DeKYn5x6.js",
			"sandbox/assets/index-DPbK6gD1.js",
			"assets/main-KSQZTTM6.js",
			"assets/_next/static/chunks/3h6wuqw8qhrkb.js",
			"_next/static/chunks/0j34vdo5b19hg.js",
			"_astro/client.C4f9q1Lp.js",
			"_app/immutable/entry/start.Dx8k2Lm9.js",
		}
		for _, rel := range recentChunks {
			at(rel, 2*time.Hour)
		}

		// Expired fingerprinted chunk (> 36h) and dropped non-immutable files (must be deleted)
		at("pay/assets/index-OLD99999.js", 40*time.Hour)
		at("pay/old.html", time.Minute)
		at("pay/sw.js", time.Minute)

		keys := []string{"index.html", "pay/index.html", "pay/assets/index-NEW12345.js"}
		if code, body := complete(t, app, "hanzo", "hanzo-ai", d.ID, map[string]any{"status": "live", "keys": keys}); code != http.StatusOK {
			t.Fatalf("complete: %d %s", code, body)
		}

		for _, rel := range recentChunks {
			if _, ok := f.body(d.Bucket, d.Prefix+"/"+rel); !ok {
				t.Errorf("%s was deleted immediately; want retained during 36h grace window", rel)
			}
		}
		for _, gone := range []string{"pay/assets/index-OLD99999.js", "pay/old.html", "pay/sw.js"} {
			if _, ok := f.body(d.Bucket, d.Prefix+"/"+gone); ok {
				t.Errorf("%s survived reconciliation; want deleted", gone)
			}
		}
	})
}

// TestUploadSiteRetainsRecentFingerprintedChunksAndPinsMIME proves artifact
// redeploys (uploadSite) retain dropped fingerprinted chunks for graceAge (36h)
// instead of pre-wiping the prefix, and write pinned web MIME types to S3.
func TestUploadSiteRetainsRecentFingerprintedChunksAndPinsMIME(t *testing.T) {
	f := startFakeS3(t)
	b := openBlobStore()
	now := time.Now()

	// Seed a previous release's objects at 1h old and 40h old.
	f.seedAt(b.bucket, "hanzo/pay-hanzo-ai/pay/assets/index-DeKYn5x6.js", "old-chunk", "text/javascript; charset=utf-8", now.Add(-time.Hour))
	f.seedAt(b.bucket, "hanzo/pay-hanzo-ai/assets/main-KSQZTTM6.js", "old-main", "text/javascript; charset=utf-8", now.Add(-time.Hour))
	f.seedAt(b.bucket, "hanzo/pay-hanzo-ai/pay/assets/index-EXPIRED1.js", "expired", "text/javascript; charset=utf-8", now.Add(-40*time.Hour))
	f.seedAt(b.bucket, "hanzo/pay-hanzo-ai/removed.html", "gone", "text/html; charset=utf-8", now.Add(-time.Minute))

	st := &site{
		files: map[string][]byte{
			"index.html":                    []byte("<!doctype html>v2"),
			"pay/assets/index-NEXT8765.mjs": []byte("export const ok = true"),
			"assets/app.cjs":                []byte("module.exports = 1"),
			"assets/app.css":                []byte("body{}"),
			"assets/engine.wasm":            []byte{0x00, 0x61, 0x73, 0x6d},
		},
	}
	if _, _, _, err := b.uploadSite(t.Context(), "hanzo", "pay-hanzo-ai", "", st); err != nil {
		t.Fatalf("uploadSite: %v", err)
	}

	// Recent dropped fingerprinted chunks must survive; expired and stale HTML must go.
	for rel, want := range map[string]bool{
		"pay/assets/index-DeKYn5x6.js":  true,
		"assets/main-KSQZTTM6.js":       true,
		"pay/assets/index-EXPIRED1.js":  false,
		"removed.html":                  false,
		"index.html":                    true,
		"pay/assets/index-NEXT8765.mjs": true,
	} {
		if _, ok := f.body(b.bucket, "hanzo/pay-hanzo-ai/"+rel); ok != want {
			t.Errorf("%s present = %v, want %v", rel, ok, want)
		}
	}

	// Pinned web MIME types must be stored on uploaded objects.
	for rel, wantCT := range map[string]string{
		"pay/assets/index-NEXT8765.mjs": "text/javascript; charset=utf-8",
		"assets/app.cjs":                "text/javascript; charset=utf-8",
		"assets/app.css":                "text/css; charset=utf-8",
		"assets/engine.wasm":            "application/wasm",
	} {
		if _, gotCT := f.stamp(b.bucket, "hanzo/pay-hanzo-ai/"+rel); gotCT != wantCT {
			t.Errorf("%s Content-Type = %q, want %q", rel, gotCT, wantCT)
		}
	}
}

type tagEdge struct {
	sites.NoEdge
	tags    []string
	onPurge func(tags []string)
}

func (e *tagEdge) PurgeTags(_ context.Context, tags ...string) error {
	e.tags = append(e.tags, tags...)
	if e.onPurge != nil {
		e.onPurge(tags)
	}
	return nil
}

// TestCompletionPurgesEdgeAfterCommitForRootAndSubpathSites verifies native
// Cloudflare Cache-Tag purge fires for both root (`hanzo-ai`) and sub-path
// mount (`pay-hanzo-ai`) publishes, and fires AFTER the project is committed
// live so the edge never re-caches pre-publish state.
func TestCompletionPurgesEdgeAfterCommitForRootAndSubpathSites(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		edge := &tagEdge{}
		mounted.State.edge = edge

		for _, slug := range []string{"hanzo-ai", "pay-hanzo-ai"} {
			d := started(t, app, "hanzo", slug)
			f.seed(d.Bucket, d.Prefix+"/index.html", "<!doctype html>"+slug)
			edge.onPurge = func(_ []string) {
				p, err := mounted.State.store.GetProject(t.Context(), "hanzo", slug)
				if err != nil {
					t.Errorf("%s: GetProject during purge: %v", slug, err)
					return
				}
				if p.Status != "live" || p.CurrentDeploy != d.ID {
					t.Errorf("%s: PurgeTags fired before DB commit: status=%q deploy=%q want live/%s", slug, p.Status, p.CurrentDeploy, d.ID)
				}
			}
			if code, body := complete(t, app, "hanzo", slug, d.ID, map[string]any{"status": "live", "keys": []string{"index.html"}}); code != http.StatusOK {
				t.Fatalf("complete %s: %d %s", slug, code, body)
			}
		}

		want := []string{"site-hanzo-hanzo-ai", "site-hanzo-pay-hanzo-ai"}
		if !slices.Equal(edge.tags, want) {
			t.Fatalf("purged tags = %v, want %v", edge.tags, want)
		}
	})
}
