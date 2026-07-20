package projects

// Tests for the static-site RELEASE plane (release.go), driven over HTTP through
// the REAL Mount + zip stack against the in-memory S3 double. They prove the
// security contract of a SERVER-SIDE COPY, which is the whole risk of this
// feature: a caller names a source and the server copies bytes with ITS OWN
// credentials, so the tenant boundary on that source is the make-or-break
// property. The vectors covered, in order:
//
//	cross-org publish + cross-org source prefix (the exfiltration primitive)
//	traversal in the source and in a source object key
//	partial copy never activates
//	rollback restores the prior release
//	idempotent re-publish
//	reserved slug
//	object-count and byte caps
//	double-activate / concurrent activate
//
// SanitizeIdentity is not wired in this harness, so a validated principal is
// simulated by setting the identity headers the gateway would mint (X-Org-Id +
// X-User-Id), exactly like sites_http_test.go.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

const testBucket = "hanzo-sites"

// seedBuild writes a minimal, valid build output at <org>/<rel>/ — the shape the
// agentic builder leaves behind in our object store.
func seedBuild(f *fakeS3, org, rel, marker string) {
	f.seed(testBucket, org+"/"+rel+"/index.html", "<!doctype html><title>"+marker+"</title>")
	f.seed(testBucket, org+"/"+rel+"/app.js", "console.log('"+marker+"')")
}

// newSite creates a project (a site) for org.
func newSite(t *testing.T, app *zip.App, org, slug string) {
	t.Helper()
	code, body := doSite(t, app, http.MethodPost, "/v1/projects", org,
		map[string]any{"name": slug, "slug": slug})
	if code != http.StatusCreated {
		t.Fatalf("create site %q for %q: want 201, got %d (%s)", slug, org, code, body)
	}
}

// postRaw sends a raw (non-JSON) body with a simulated validated principal — the
// artifact-deploy ergonomics, used here to prove the legacy path's interaction
// with the release pointer.
func postRaw(t *testing.T, app *zip.App, path, org string, raw []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u-"+org)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// publish promotes source and goes live in one call, returning status + body.
func publish(t *testing.T, app *zip.App, org, slug, source string) (int, []byte) {
	t.Helper()
	return doSite(t, app, http.MethodPost, "/v1/sites/"+slug+"/publish", org,
		map[string]any{"source": source})
}

func decodeRelease(t *testing.T, body []byte) releaseView {
	t.Helper()
	var v releaseView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode release view: %v (%s)", err, body)
	}
	return v
}

// releaseHarness boots the standard fixture: an S3 double, a funded commerce
// double, the mounted projects surface, and a site owned by org "acme".
func releaseHarness(t *testing.T) (*fakeS3, *zip.App) {
	t.Helper()
	f := startFakeS3(t)
	bs := &billServer{available: 1000000}
	app := mountSites(t, nil, bs.start(t))
	newSite(t, app, "acme", "shop")
	return f, app
}

// TestRelease_PublishPromotesAndServesFromTheReleasePrefix is the happy path and
// the shape of the whole feature: bytes move server-side (COPY, never through
// the API), land at an immutable content-addressed prefix, and the site's
// serving prefix flips to that release.
func TestRelease_PublishPromotesAndServesFromTheReleasePrefix(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")

	code, body := publish(t, app, "acme", "shop", "builds/v1")
	if code != http.StatusOK {
		t.Fatalf("publish want 200, got %d (%s)", code, body)
	}
	rel := decodeRelease(t, body)
	if !releaseIDRE.MatchString(rel.ReleaseID) {
		t.Fatalf("release id %q must be a content address matching %s", rel.ReleaseID, releaseIDRE)
	}
	if rel.Objects != 2 || !rel.Active {
		t.Fatalf("want 2 objects and active, got %+v", rel)
	}
	// The bytes landed at the immutable release prefix, not at the mutable one.
	want := releasePrefix("acme", "shop", rel.ReleaseID) + "/index.html"
	if got, ok := f.body(testBucket, want); !ok || !strings.Contains(got, "one") {
		t.Fatalf("release object missing at %q (ok=%v, body=%q)", want, ok, got)
	}
	// NOTHING was uploaded through the API — every byte moved by server-side COPY.
	if f.puts != 0 {
		t.Fatalf("publish must not PUT object bodies through the API, got %d puts", f.puts)
	}
	if f.copyCount() != 2 {
		t.Fatalf("want 2 server-side copies, got %d", f.copyCount())
	}
	// The serving pointer now resolves to the release prefix (what the edge reads).
	p := getProject(t, "acme", "shop")
	if p.CurrentRelease != rel.ReleaseID {
		t.Fatalf("pointer not flipped: %q != %q", p.CurrentRelease, rel.ReleaseID)
	}
	if got := servePrefix(p); got != releasePrefix("acme", "shop", rel.ReleaseID) {
		t.Fatalf("site serves %q, want the release prefix", got)
	}
}

// getProject reads a project straight from the mounted store (the same row the
// site resolver reads), so tests assert on the SERVING truth, not a view.
func getProject(t *testing.T, org, slug string) Project {
	t.Helper()
	p, err := mounted.State.store.GetProject(t.Context(), org, slug)
	if err != nil {
		t.Fatalf("get project %s/%s: %v", org, slug, err)
	}
	return p
}

// ── vector 1: cross-org publish + cross-org source (the exfiltration primitive) ──

// TestRelease_CrossOrgPublishDenied: org "evil" may not publish to a slug owned
// by org "acme". The slug must 404 exactly as a nonexistent one does — no
// existence oracle — and nothing may be copied.
func TestRelease_CrossOrgPublishDenied(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "evil", "builds/v1", "evil")

	code, body := publish(t, app, "evil", "shop", "builds/v1")
	if code != http.StatusNotFound {
		t.Fatalf("cross-org publish want 404, got %d (%s)", code, body)
	}
	// A slug that exists for nobody must be INDISTINGUISHABLE from acme's slug.
	ghostCode, ghostBody := publish(t, app, "evil", "nosuchsite", "builds/v1")
	if ghostCode != code || string(ghostBody) != string(body) {
		t.Fatalf("existence oracle: foreign slug %d/%s differs from unknown slug %d/%s",
			code, body, ghostCode, ghostBody)
	}
	if f.copyCount() != 0 {
		t.Fatalf("a denied publish must copy nothing, got %d copies", f.copyCount())
	}
	if p := getProject(t, "acme", "shop"); p.CurrentRelease != "" {
		t.Fatalf("victim site pointer moved: %q", p.CurrentRelease)
	}
}

// TestRelease_CrossOrgSourceIsUnreachable is THE test for the server-side-copy
// exfiltration primitive. Org "evil" owns its own site and tries every syntax
// for naming ANOTHER org's prefix as the copy source. None may reach it: the org
// segment is prepended server-side, so a caller has no way to express one.
func TestRelease_CrossOrgSourceIsUnreachable(t *testing.T) {
	f, app := releaseHarness(t)
	newSite(t, app, "evil", "front")
	// The victim's private bytes, in acme's space and in acme's release space.
	f.seed(testBucket, "acme/shop/index.html", "ACME SECRET")
	f.seed(testBucket, "acme/.releases/shop/rel_0/index.html", "ACME SECRET RELEASE")
	seedBuild(f, "evil", "builds/v1", "evil")

	for _, source := range []string{
		"../acme/shop",           // parent escape
		"../../acme/shop",        // deeper escape
		"/acme/shop",             // absolute
		"builds/../../acme/shop", // escape after a valid-looking head
		`..\acme\shop`,           // backslash separators
		"./../acme/shop",         // dot-relative escape
		"s3://hanzo-sites/acme/shop",
		"https://s3.hanzo.ai/hanzo-sites/acme/shop",
		"acme/shop",      // names the victim org verbatim — resolves under evil/, misses
		".releases/shop", // evil's own release space, not acme's
		"",               // empty
		"/",              // root
		".",              // dot
		"..",             // bare parent
	} {
		code, body := publish(t, app, "evil", "front", source)
		if code == http.StatusOK || code == http.StatusCreated {
			t.Fatalf("source %q was ACCEPTED (%d): %s", source, code, body)
		}
		if strings.Contains(string(body), "ACME") {
			t.Fatalf("source %q leaked victim content: %s", source, body)
		}
	}
	// Not one byte of acme's data was copied anywhere, and evil's site never went live.
	if f.copyCount() != 0 {
		t.Fatalf("no cross-org source may be copied, got %d copies", f.copyCount())
	}
	if p := getProject(t, "evil", "front"); p.CurrentRelease != "" {
		t.Fatalf("evil site went live off a rejected source: %q", p.CurrentRelease)
	}
	for k := range map[string]bool{"evil/.releases/front": true} {
		if n := f.count(testBucket + "/" + k); n != 0 {
			t.Fatalf("release space %q must be empty, has %d objects", k, n)
		}
	}
}

// TestSourcePrefix_ContainmentIsStructural proves the property directly on the
// resolver: whatever a caller supplies, the result either fails or stays under
// `<org>/`. This is the unit-level statement of the isolation argument.
func TestSourcePrefix_ContainmentIsStructural(t *testing.T) {
	hostile := []string{
		"../other", "../../other", "/etc/passwd", "a/../../b", `..\..\b`, "./..",
		"", " ", ".", "..", "/", "//", "s3://b/k", "http://h/p", "a\x00b", "a\nb",
		strings.Repeat("a", maxSourceLen+1),
		strings.Repeat("../", 64) + "other",
	}
	for _, in := range hostile {
		got, err := sourcePrefix("acme", in)
		if err != nil {
			continue // refused outright — fine
		}
		if !strings.HasPrefix(got, "acme/") || strings.Contains(got, "..") {
			t.Fatalf("sourcePrefix(%q) escaped its org: %q", in, got)
		}
	}
	// A benign path resolves under the org, and an inner ".." that does NOT escape
	// is normalized rather than refused.
	if got, err := sourcePrefix("acme", "builds/v1"); err != nil || got != "acme/builds/v1" {
		t.Fatalf("sourcePrefix benign = %q, %v", got, err)
	}
	if got, err := sourcePrefix("acme", "builds/tmp/../v1"); err != nil || got != "acme/builds/v1" {
		t.Fatalf("sourcePrefix inner-dotdot = %q, %v", got, err)
	}
	// Two DISTINCT orgs can never resolve to the same prefix (the org is verbatim,
	// never folded), so no pair of tenants shares a release space.
	a, _ := sourcePrefix("Acme", "b")
	b, _ := sourcePrefix("acme", "b")
	if a == b {
		t.Fatalf("distinct orgs collapsed onto one prefix: %q", a)
	}
}

// ── vector 2: traversal in a source OBJECT KEY ──

// TestRelease_UnsafeObjectKeyRejected: S3 is a flat keyspace, so a build output
// can literally contain a ".." key. The release is refused wholesale rather than
// the key being rewritten (rewriting could collapse two distinct keys onto one),
// and nothing is copied or activated.
func TestRelease_UnsafeObjectKeyRejected(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/bad", "bad")
	f.seed(testBucket, "acme/builds/bad/../escape.html", "ESCAPED")

	code, body := publish(t, app, "acme", "shop", "builds/bad")
	if code != http.StatusBadRequest {
		t.Fatalf("unsafe key want 400, got %d (%s)", code, body)
	}
	if f.copyCount() != 0 {
		t.Fatalf("an unsafe manifest must copy nothing, got %d copies", f.copyCount())
	}
	if p := getProject(t, "acme", "shop"); p.CurrentRelease != "" {
		t.Fatalf("site went live off an unsafe manifest: %q", p.CurrentRelease)
	}
}

// ── vector 3: a partial copy is never activated ──

// TestRelease_PartialCopyNeverActivates: the copy fails halfway. No release row
// is written, so the pointer cannot be flipped to it — not by the publish that
// failed, and not by a later explicit activate naming the same id.
func TestRelease_PartialCopyNeverActivates(t *testing.T) {
	f, app := releaseHarness(t)
	// A build big enough that "fail after 1" leaves a genuinely partial prefix.
	for i := 0; i < 4; i++ {
		f.seed(testBucket, fmt.Sprintf("acme/builds/big/a%d.js", i), fmt.Sprintf("payload-%d", i))
	}
	f.seed(testBucket, "acme/builds/big/index.html", "<!doctype html>")
	f.failCopyAfter.Store(2)

	code, body := publish(t, app, "acme", "shop", "builds/big")
	if code != http.StatusBadGateway {
		t.Fatalf("partial copy want 502, got %d (%s)", code, body)
	}
	p := getProject(t, "acme", "shop")
	if p.CurrentRelease != "" {
		t.Fatalf("pointer flipped to a partially-copied release: %q", p.CurrentRelease)
	}
	if servePrefix(p) != sitePrefix("acme", "shop") {
		t.Fatalf("site must still serve its previous prefix, got %q", servePrefix(p))
	}
	// No release row exists, so the orphaned objects are unreachable...
	rows, err := mounted.State.store.ListReleases(t.Context(), "acme", "shop", 10)
	if err != nil {
		t.Fatalf("list releases: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a failed promote must record no release, got %d", len(rows))
	}
	// ...and naming any release id explicitly cannot flip the pointer either.
	code, _ = doSite(t, app, http.MethodPost,
		"/v1/sites/shop/releases/rel_"+strings.Repeat("a", 32)+"/activate", "acme", nil)
	if code != http.StatusNotFound {
		t.Fatalf("activating a release with no row want 404, got %d", code)
	}
	if p := getProject(t, "acme", "shop"); p.CurrentRelease != "" {
		t.Fatalf("pointer moved via explicit activate: %q", p.CurrentRelease)
	}
}

// TestRelease_SourceMutatedMidPublishIsRefused: the manifest is digested from
// per-object etags, and every copy is conditional on the etag it was digested
// from. A concurrent writer that mutates the build output mid-publish therefore
// invalidates the release rather than producing one whose bytes disagree with
// the content address that names it.
func TestRelease_SourceMutatedMidPublishIsRefused(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/race", "race")
	f.seed(testBucket, "acme/builds/race/zz-last.js", "tail")
	f.mutateOnCopy.Store("acme/builds/race/zz-last.js") // mutated after it was listed

	code, body := publish(t, app, "acme", "shop", "builds/race")
	if code != http.StatusConflict {
		t.Fatalf("mutated source want 409, got %d (%s)", code, body)
	}
	if p := getProject(t, "acme", "shop"); p.CurrentRelease != "" {
		t.Fatalf("site went live off a mutated source: %q", p.CurrentRelease)
	}
}

// ── vector 4: rollback ──

// TestRelease_RollbackRestoresThePriorRelease: two releases, then activate the
// first again. The pointer returns to it, the OLD release's bytes are still
// there (releases are immutable and retained), and nothing is re-copied — a
// rollback costs one UPDATE.
func TestRelease_RollbackRestoresThePriorRelease(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")
	seedBuild(f, "acme", "builds/v2", "two")

	_, b1 := publish(t, app, "acme", "shop", "builds/v1")
	first := decodeRelease(t, b1)
	_, b2 := publish(t, app, "acme", "shop", "builds/v2")
	second := decodeRelease(t, b2)
	if first.ReleaseID == second.ReleaseID {
		t.Fatalf("different content must yield different releases: %q", first.ReleaseID)
	}
	if got := getProject(t, "acme", "shop").CurrentRelease; got != second.ReleaseID {
		t.Fatalf("pointer should be at v2, got %q", got)
	}

	copiesBefore := f.copyCount()
	code, body := doSite(t, app, http.MethodPost,
		"/v1/sites/shop/releases/"+first.ReleaseID+"/activate", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("rollback want 200, got %d (%s)", code, body)
	}
	p := getProject(t, "acme", "shop")
	if p.CurrentRelease != first.ReleaseID {
		t.Fatalf("rollback did not restore v1: %q", p.CurrentRelease)
	}
	if servePrefix(p) != releasePrefix("acme", "shop", first.ReleaseID) {
		t.Fatalf("serving prefix not rolled back: %q", servePrefix(p))
	}
	if f.copyCount() != copiesBefore {
		t.Fatalf("a rollback must copy nothing, %d new copies", f.copyCount()-copiesBefore)
	}
	// v1's bytes survived v2 — immutable and retained is what makes this free.
	if got, ok := f.body(testBucket, releasePrefix("acme", "shop", first.ReleaseID)+"/index.html"); !ok || !strings.Contains(got, "one") {
		t.Fatalf("prior release was not retained: ok=%v body=%q", ok, got)
	}
	// The rollback menu marks exactly one active release, newest first.
	code, body = doSite(t, app, http.MethodGet, "/v1/sites/shop/releases", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list releases want 200, got %d (%s)", code, body)
	}
	var list []releaseView
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 releases, got %d (%s)", len(list), body)
	}
	active := 0
	for _, v := range list {
		if v.Active {
			active++
			if v.ReleaseID != first.ReleaseID {
				t.Fatalf("wrong release marked active: %q", v.ReleaseID)
			}
		}
	}
	if active != 1 {
		t.Fatalf("exactly one release must be active, got %d", active)
	}
}

// ── vector 5: idempotence ──

// TestRelease_RepublishIsIdempotent: publishing an unchanged source yields the
// SAME release id and copies nothing the second time — idempotence falls out of
// content-addressing, it is not a remembered request key.
func TestRelease_RepublishIsIdempotent(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")

	_, b1 := publish(t, app, "acme", "shop", "builds/v1")
	first := decodeRelease(t, b1)
	after := f.copyCount()

	_, b2 := publish(t, app, "acme", "shop", "builds/v1")
	second := decodeRelease(t, b2)

	if first.ReleaseID != second.ReleaseID {
		t.Fatalf("identical source must yield the same release: %q vs %q", first.ReleaseID, second.ReleaseID)
	}
	if f.copyCount() != after {
		t.Fatalf("a re-publish of unchanged content must copy nothing, %d new copies", f.copyCount()-after)
	}
	rows, _ := mounted.State.store.ListReleases(t.Context(), "acme", "shop", 10)
	if len(rows) != 1 {
		t.Fatalf("idempotent re-publish must not duplicate the release row, got %d", len(rows))
	}
	// A one-byte change to the SAME path must produce a DIFFERENT release — the
	// content address must not degenerate into a name address.
	f.seed(testBucket, "acme/builds/v1/app.js", "console.log('CHANGED')")
	_, b3 := publish(t, app, "acme", "shop", "builds/v1")
	if third := decodeRelease(t, b3); third.ReleaseID == first.ReleaseID {
		t.Fatalf("changed content reused release id %q", third.ReleaseID)
	}
}

// TestReleaseID_IsAContentAddress states the digest properties directly.
func TestReleaseID_IsAContentAddress(t *testing.T) {
	base := []releaseObject{{rel: "index.html", size: 10, etag: "aa"}, {rel: "app.js", size: 4, etag: "bb"}}
	id := releaseID(base)
	if !releaseIDRE.MatchString(id) {
		t.Fatalf("id %q must match %s", id, releaseIDRE)
	}
	same := []releaseObject{{rel: "index.html", size: 10, etag: "aa"}, {rel: "app.js", size: 4, etag: "bb"}}
	if releaseID(same) != id {
		t.Fatalf("identical manifests must share an id")
	}
	for name, other := range map[string][]releaseObject{
		"changed etag": {{rel: "index.html", size: 10, etag: "aa"}, {rel: "app.js", size: 4, etag: "cc"}},
		"changed size": {{rel: "index.html", size: 11, etag: "aa"}, {rel: "app.js", size: 4, etag: "bb"}},
		"changed key":  {{rel: "index.html", size: 10, etag: "aa"}, {rel: "app.ts", size: 4, etag: "bb"}},
		"extra object": append(append([]releaseObject{}, base...), releaseObject{rel: "x", size: 1, etag: "dd"}),
		"dropped":      {{rel: "index.html", size: 10, etag: "aa"}},
	} {
		if releaseID(other) == id {
			t.Fatalf("%s must change the release id", name)
		}
	}
	// Field boundaries are unambiguous: no key/size pair can be re-read as another.
	a := []releaseObject{{rel: "a", size: 1, etag: "b"}}
	b := []releaseObject{{rel: "a\x001", size: 0, etag: "b"}}
	if releaseID(a) == releaseID(b) {
		t.Fatalf("manifest fields are ambiguously framed")
	}
}

// ── vector 6: reserved slugs ──

// TestRelease_ReservedSlugRejected: a reserved label can never be a site, so
// every release route refuses it with the same honest 404 — no route may become
// a way to write into a reserved subdomain's space.
func TestRelease_ReservedSlugRejected(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")

	for _, slug := range []string{"api", "admin", "login", "www"} {
		code, body := publish(t, app, "acme", slug, "builds/v1")
		if code != http.StatusNotFound {
			t.Fatalf("reserved slug %q publish want 404, got %d (%s)", slug, code, body)
		}
		code, _ = doSite(t, app, http.MethodGet, "/v1/sites/"+slug+"/releases", "acme", nil)
		if code != http.StatusNotFound {
			t.Fatalf("reserved slug %q list want 404, got %d", slug, code)
		}
	}
	if f.copyCount() != 0 {
		t.Fatalf("a reserved slug must copy nothing, got %d copies", f.copyCount())
	}
}

// ── vector 7: caps ──

// TestRelease_ObjectCountCapEnforced: a source with more objects than a site may
// hold is refused (413) before anything is copied — the listing itself stops at
// the cap rather than enumerating an arbitrarily large prefix.
func TestRelease_ObjectCountCapEnforced(t *testing.T) {
	f, app := releaseHarness(t)
	f.seed(testBucket, "acme/builds/many/index.html", "<!doctype html>")
	for i := 0; i < maxFiles+10; i++ {
		f.seed(testBucket, fmt.Sprintf("acme/builds/many/f%05d.js", i), "x")
	}
	code, body := publish(t, app, "acme", "shop", "builds/many")
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("object cap want 413, got %d (%s)", code, body)
	}
	if f.copyCount() != 0 {
		t.Fatalf("an over-cap source must copy nothing, got %d copies", f.copyCount())
	}
}

// TestRelease_ByteCapEnforced: total bytes are capped the same way, with the
// SAME budget the artifact deploy path uses — one "how big is a site" policy.
func TestRelease_ByteCapEnforced(t *testing.T) {
	f, app := releaseHarness(t)
	f.seed(testBucket, "acme/builds/heavy/index.html", "<!doctype html>")
	// Sparse: the cap is enforced on the SIZE the listing reports (bodies are never
	// read by the release plane), so this asserts on >512 MiB without allocating it.
	for i := 0; i < (maxTotalBytes>>20)+2; i++ {
		f.seedSparse(testBucket, fmt.Sprintf("acme/builds/heavy/blob%03d.bin", i), 1<<20)
	}
	code, body := publish(t, app, "acme", "shop", "builds/heavy")
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("byte cap want 413, got %d (%s)", code, body)
	}
	if f.copyCount() != 0 {
		t.Fatalf("an over-cap source must copy nothing, got %d copies", f.copyCount())
	}
}

// TestRelease_EmptyAndIndexlessSourcesRefused: a release must be a site — a
// prefix with no objects, or one with no index.html at its root, is refused with
// the same contract the artifact path enforces.
func TestRelease_EmptyAndIndexlessSourcesRefused(t *testing.T) {
	f, app := releaseHarness(t)
	f.seed(testBucket, "acme/builds/noindex/app.js", "console.log(1)")
	// A sibling prefix that merely shares a name stem must NOT be pulled in.
	f.seed(testBucket, "acme/builds/noindex-old/index.html", "<!doctype html>")

	code, body := publish(t, app, "acme", "shop", "builds/empty")
	if code != http.StatusBadRequest {
		t.Fatalf("empty source want 400, got %d (%s)", code, body)
	}
	code, body = publish(t, app, "acme", "shop", "builds/noindex")
	if code != http.StatusBadRequest {
		t.Fatalf("index-less source want 400, got %d (%s)", code, body)
	}
	if f.copyCount() != 0 {
		t.Fatalf("a refused source must copy nothing, got %d copies", f.copyCount())
	}
}

// ── vector 8: activation races ──

// TestRelease_ConcurrentActivateIsSerialized: activation is one atomic statement,
// so racing activations of two releases leave the pointer at exactly one of them
// — never blended, never empty, never a release that does not exist.
func TestRelease_ConcurrentActivateIsSerialized(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")
	seedBuild(f, "acme", "builds/v2", "two")
	_, b1 := publish(t, app, "acme", "shop", "builds/v1")
	_, b2 := publish(t, app, "acme", "shop", "builds/v2")
	first, second := decodeRelease(t, b1).ReleaseID, decodeRelease(t, b2).ReleaseID

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		id := first
		if i%2 == 0 {
			id = second
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			doSite(t, app, http.MethodPost, "/v1/sites/shop/releases/"+id+"/activate", "acme", nil)
		}(id)
	}
	wg.Wait()

	got := getProject(t, "acme", "shop").CurrentRelease
	if got != first && got != second {
		t.Fatalf("pointer landed on neither release: %q", got)
	}
}

// TestRelease_DoubleActivateIsIdempotent: re-activating the already-active
// release succeeds and leaves the pointer where it is (a retried request is not
// an error).
func TestRelease_DoubleActivateIsIdempotent(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")
	_, b := publish(t, app, "acme", "shop", "builds/v1")
	id := decodeRelease(t, b).ReleaseID

	for i := 0; i < 2; i++ {
		code, body := doSite(t, app, http.MethodPost, "/v1/sites/shop/releases/"+id+"/activate", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("activate #%d want 200, got %d (%s)", i, code, body)
		}
	}
	if got := getProject(t, "acme", "shop").CurrentRelease; got != id {
		t.Fatalf("pointer moved: %q != %q", got, id)
	}
}

// TestRelease_CrossOrgActivateDenied: a release id is a content address, so an
// attacker who learns one (or guesses one produced from bytes they also hold)
// still cannot aim ANOTHER org's site at it — the store lookup is keyed by org.
func TestRelease_CrossOrgActivateDenied(t *testing.T) {
	f, app := releaseHarness(t)
	newSite(t, app, "evil", "front")
	seedBuild(f, "acme", "builds/v1", "one")
	_, b := publish(t, app, "acme", "shop", "builds/v1")
	victim := decodeRelease(t, b).ReleaseID

	// evil publishes byte-identical content, so it holds the SAME content address.
	seedBuild(f, "evil", "builds/v1", "one")
	_, eb := publish(t, app, "evil", "front", "builds/v1")
	if mine := decodeRelease(t, eb).ReleaseID; mine != victim {
		t.Fatalf("identical bytes should content-address alike: %q vs %q", mine, victim)
	}
	// Yet the two releases are independent rows over independent object copies:
	// evil cannot activate the victim's release ON THE VICTIM'S SITE.
	code, _ := doSite(t, app, http.MethodPost,
		"/v1/sites/shop/releases/"+victim+"/activate", "evil", nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-org activate want 404, got %d", code)
	}
	// evil's own release lives in evil's space, never acme's.
	if _, ok := f.body(testBucket, releasePrefix("evil", "front", victim)+"/index.html"); !ok {
		t.Fatalf("evil's release should exist in evil's own space")
	}
}

// ── surface + regression ──

// TestRelease_NoPrincipalDenied: every release route requires a validated
// principal. Without one there is no tenant, so there is nothing to publish to.
func TestRelease_NoPrincipalDenied(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/sites/shop/publish"},
		{http.MethodPost, "/v1/sites/shop/releases"},
		{http.MethodGet, "/v1/sites/shop/releases"},
		{http.MethodPost, "/v1/sites/shop/releases/rel_" + strings.Repeat("a", 32) + "/activate"},
	} {
		code, body := doSite(t, app, tc.method, tc.path, "", map[string]any{"source": "builds/v1"})
		if code != http.StatusForbidden {
			t.Fatalf("%s %s want 403, got %d (%s)", tc.method, tc.path, code, body)
		}
	}
	if f.copyCount() != 0 {
		t.Fatalf("an unauthenticated request must copy nothing, got %d copies", f.copyCount())
	}
}

// TestRelease_MalformedReleaseIDRejected: the id is a URL path segment AND an S3
// key segment, so anything that is not a content address is refused before it
// can be interpolated into a prefix.
func TestRelease_MalformedReleaseIDRejected(t *testing.T) {
	_, app := releaseHarness(t)
	for _, id := range []string{
		"rel_" + strings.Repeat("a", 31), "rel_" + strings.Repeat("A", 32),
		"rel_" + strings.Repeat("z", 32), "..", "rel_..", "%2e%2e", "rel_" + strings.Repeat("a", 33),
	} {
		code, _ := doSite(t, app, http.MethodPost, "/v1/sites/shop/releases/"+id+"/activate", "acme", nil)
		if code != http.StatusNotFound {
			t.Fatalf("malformed id %q want 404, got %d", id, code)
		}
	}
}

// TestRelease_StagedCreateDoesNotServe: creating a release does NOT flip the
// pointer. The site keeps serving what it served until an explicit activate.
func TestRelease_StagedCreateDoesNotServe(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")

	code, body := doSite(t, app, http.MethodPost, "/v1/sites/shop/releases", "acme",
		map[string]any{"source": "builds/v1"})
	if code != http.StatusCreated {
		t.Fatalf("create release want 201, got %d (%s)", code, body)
	}
	rel := decodeRelease(t, body)
	if rel.Active {
		t.Fatalf("a created release must not be active until activated")
	}
	if p := getProject(t, "acme", "shop"); p.CurrentRelease != "" {
		t.Fatalf("create must not flip the pointer, got %q", p.CurrentRelease)
	}
	// The bytes ARE staged, so activation is a pure pointer move.
	if _, ok := f.body(testBucket, releasePrefix("acme", "shop", rel.ReleaseID)+"/index.html"); !ok {
		t.Fatalf("staged release bytes missing")
	}
	code, _ = doSite(t, app, http.MethodPost, "/v1/sites/shop/releases/"+rel.ReleaseID+"/activate", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("activate want 200, got %d", code)
	}
	if got := getProject(t, "acme", "shop").CurrentRelease; got != rel.ReleaseID {
		t.Fatalf("activate did not flip the pointer: %q", got)
	}
}

// TestServePrefix_PointerFallbackIsSafe: the serving prefix re-validates the
// pointer, so a poisoned row degrades to the legacy prefix instead of widening
// the tenant boundary.
func TestServePrefix_PointerFallbackIsSafe(t *testing.T) {
	for _, poison := range []string{"", "../../other", "rel_../..", "rel_zzz", "/etc", ".."} {
		p := Project{Org: "acme", Slug: "shop", CurrentRelease: poison}
		got := servePrefix(p)
		if got != sitePrefix("acme", "shop") {
			t.Fatalf("poisoned pointer %q produced prefix %q", poison, got)
		}
	}
	good := "rel_" + strings.Repeat("a", 32)
	p := Project{Org: "acme", Slug: "shop", CurrentRelease: good}
	if got, want := servePrefix(p), releasePrefix("acme", "shop", good); got != want {
		t.Fatalf("servePrefix = %q, want %q", got, want)
	}
}

// TestRelease_ArtifactDeployTakesBackThePointer: the legacy full-artifact deploy
// replaces the site at its mutable prefix, so it must reclaim the pointer — else
// the site would keep serving the old release and the upload would be invisible.
// The release objects survive (they live in a sibling space), so the release
// remains re-activatable.
func TestRelease_ArtifactDeployTakesBackThePointer(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")
	_, b := publish(t, app, "acme", "shop", "builds/v1")
	rel := decodeRelease(t, b)

	code, body := postRaw(t, app, "/v1/projects/shop/deploy", "acme",
		buildTar(t, true, map[string]string{"index.html": "<!doctype html><title>artifact</title>"}))
	if code != http.StatusOK {
		t.Fatalf("artifact deploy want 200, got %d (%s)", code, body)
	}
	p := getProject(t, "acme", "shop")
	if p.CurrentRelease != "" {
		t.Fatalf("artifact deploy must reclaim the pointer, still %q", p.CurrentRelease)
	}
	if servePrefix(p) != sitePrefix("acme", "shop") {
		t.Fatalf("site must serve the artifact prefix, got %q", servePrefix(p))
	}
	// The release itself survived and can be rolled back to.
	if _, ok := f.body(testBucket, releasePrefix("acme", "shop", rel.ReleaseID)+"/index.html"); !ok {
		t.Fatalf("artifact deploy destroyed a retained release")
	}
	code, _ = doSite(t, app, http.MethodPost, "/v1/sites/shop/releases/"+rel.ReleaseID+"/activate", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("re-activating the surviving release want 200, got %d", code)
	}
}

// TestRelease_PlatformSurfaceIsTheSameEngine: /v1/platform/sites and /v1/sites
// are one engine over one store, so a release published on one is activatable on
// the other. Two surfaces, never two implementations.
func TestRelease_PlatformSurfaceIsTheSameEngine(t *testing.T) {
	f, app := releaseHarness(t)
	seedBuild(f, "acme", "builds/v1", "one")

	code, body := doSite(t, app, http.MethodPost, "/v1/platform/sites/shop/releases", "acme",
		map[string]any{"source": "builds/v1"})
	if code != http.StatusCreated {
		t.Fatalf("platform create want 201, got %d (%s)", code, body)
	}
	id := decodeRelease(t, body).ReleaseID
	code, body = doSite(t, app, http.MethodPost, "/v1/sites/shop/releases/"+id+"/activate", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("cross-surface activate want 200, got %d (%s)", code, body)
	}
	if got := getProject(t, "acme", "shop").CurrentRelease; got != id {
		t.Fatalf("pointer not flipped across surfaces: %q", got)
	}
}
