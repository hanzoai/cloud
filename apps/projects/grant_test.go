package projects

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// grant_test.go pins the two halves of the credential-free deploy path: the
// grant CI is handed is scoped to ONE prefix and expires, and the reconcile that
// replaced `--delete` prunes only that prefix and never on an empty manifest.

// decodePolicy pulls the base64 policy document out of the signed form fields so
// the CONDITIONS can be asserted, not just the presence of a signature. What the
// policy says is the whole security property; a grant with a signature but no
// key condition would authorize the entire bucket.
func decodePolicy(t *testing.T, fields map[string]string) map[string]any {
	t.Helper()
	raw, ok := fields["policy"]
	if !ok {
		t.Fatalf("no policy field in grant; fields=%v", keysOf(fields))
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("policy is not base64: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("policy is not json: %v", err)
	}
	return doc
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// conditionsText flattens the policy conditions to a searchable string. The
// document mixes arrays and objects, so comparing the rendered form is both
// simpler and stricter than walking a schema that varies by SDK version.
func conditionsText(t *testing.T, doc map[string]any) string {
	t.Helper()
	b, err := json.Marshal(doc["conditions"])
	if err != nil {
		t.Fatalf("marshal conditions: %v", err)
	}
	return string(b)
}

func TestGrantIsScopedToOnePrefixAndExpires(t *testing.T) {
	startFakeS3(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "s3.example.test")
	b := openBlobStore()

	now := time.Unix(1_700_000_000, 0).UTC()
	g, err := mintUploadGrant(context.Background(), b, "acme/site", now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if g == nil {
		t.Fatal("no grant minted despite presign being configured")
	}
	if g.Prefix != "acme/site" {
		t.Errorf("prefix = %q", g.Prefix)
	}
	if want := now.Add(grantTTL).Unix(); g.ExpiresAt != want {
		t.Errorf("expiresAt = %d, want %d", g.ExpiresAt, want)
	}
	// Signed for the PUBLIC host: CI resolves that, and the signature covers it.
	if !strings.Contains(g.URL, "s3.example.test") {
		t.Errorf("grant URL %q is not the public endpoint", g.URL)
	}

	doc := decodePolicy(t, g.Fields)
	conds := conditionsText(t, doc)
	// THE containment. Trailing slash included, or a grant for "acme/site" would
	// also authorize the neighbouring "acme/site-other".
	if !strings.Contains(conds, "starts-with") || !strings.Contains(conds, "acme/site/") {
		t.Fatalf("policy does not confine the key to the prefix: %s", conds)
	}
	if !strings.Contains(conds, b.bucket) {
		t.Errorf("policy does not name the bucket: %s", conds)
	}
	if doc["expiration"] == nil {
		t.Error("policy has no expiration — a grant must not be usable forever")
	}
}

// TestGrantsDifferPerPrefix: two sites must not receive interchangeable grants.
// If one site's grant authorized another's prefix, the shared-bucket hazard this
// whole mechanism exists to remove would still be present.
func TestGrantsDifferPerPrefix(t *testing.T) {
	startFakeS3(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "s3.example.test")
	b := openBlobStore()
	now := time.Unix(1_700_000_000, 0).UTC()

	a, err := mintUploadGrant(context.Background(), b, "acme/site", now)
	if err != nil || a == nil {
		t.Fatalf("mint a: %v", err)
	}
	c, err := mintUploadGrant(context.Background(), b, "other/site", now)
	if err != nil || c == nil {
		t.Fatalf("mint c: %v", err)
	}
	condA := conditionsText(t, decodePolicy(t, a.Fields))
	if strings.Contains(condA, "other/site/") {
		t.Fatal("acme's grant authorizes another org's prefix")
	}
	if a.Fields["policy"] == c.Fields["policy"] {
		t.Fatal("two prefixes produced an identical policy")
	}
	// Distinct policies must carry distinct signatures.
	if sigOf(a.Fields) == sigOf(c.Fields) {
		t.Fatal("distinct policies share a signature")
	}
}

func sigOf(f map[string]string) string {
	for _, k := range []string{"x-amz-signature", "signature", "X-Amz-Signature"} {
		if v, ok := f[k]; ok {
			return v
		}
	}
	return ""
}

func TestGrantAbsentWhenPresignUnconfigured(t *testing.T) {
	// No S3_ADMIN_* at all ⇒ nothing to sign with. A deployment must still be
	// creatable, so this is (nil, nil), not an error.
	t.Setenv("S3_ADMIN_ACCESS_KEY", "")
	t.Setenv("S3_ADMIN_SECRET_KEY", "")
	b := openBlobStore()
	g, err := mintUploadGrant(context.Background(), b, "acme/site", time.Now())
	if err != nil {
		t.Fatalf("unconfigured presign returned an error: %v", err)
	}
	if g != nil {
		t.Fatal("a grant was minted with no credentials configured")
	}
}

func TestReconcileRemovesOnlyStaleKeys(t *testing.T) {
	f := startFakeS3(t)
	b := openBlobStore()
	cli, err := b.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	f.seed(b.bucket, "acme/site/index.html", "new")
	f.seed(b.bucket, "acme/site/about.html", "new")
	f.seed(b.bucket, "acme/site/gone.html", "stale")

	keep := map[string]bool{"index.html": true, "about.html": true}
	removed, err := reconcilePrefix(context.Background(), cli, b.bucket, "acme/site", keep)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, ok := f.body(b.bucket, "acme/site/gone.html"); ok {
		t.Error("the deleted page still serves — this is the bug --delete used to prevent")
	}
	for _, k := range []string{"acme/site/index.html", "acme/site/about.html"} {
		if _, ok := f.body(b.bucket, k); !ok {
			t.Errorf("%s was removed but is in the manifest", k)
		}
	}
}

// TestReconcileNeverLeavesItsPrefix is the cross-tenant boundary: reconciling one
// site must not consider, let alone delete, another org's objects.
func TestReconcileNeverLeavesItsPrefix(t *testing.T) {
	f := startFakeS3(t)
	b := openBlobStore()
	cli, err := b.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	f.seed(b.bucket, "acme/site/index.html", "mine")
	f.seed(b.bucket, "other/site/index.html", "theirs")
	// A sibling prefix that shares acme/site as a STRING prefix but is a different
	// site — the case the trailing slash exists for.
	f.seed(b.bucket, "acme/site-two/index.html", "sibling")

	// A manifest that mentions nothing of theirs.
	keep := map[string]bool{"index.html": true}
	if _, err := reconcilePrefix(context.Background(), cli, b.bucket, "acme/site", keep); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, k := range []string{"other/site/index.html", "acme/site-two/index.html"} {
		if _, ok := f.body(b.bucket, k); !ok {
			t.Fatalf("%s was deleted by another site's reconcile", k)
		}
	}
}

// TestReconcileEmptyManifestDeletesNothing: a build that reports no files has
// almost certainly failed to enumerate its output. Honouring that literally would
// delete the whole live site.
func TestReconcileEmptyManifestDeletesNothing(t *testing.T) {
	f := startFakeS3(t)
	b := openBlobStore()
	cli, err := b.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	f.seed(b.bucket, "acme/site/index.html", "live")

	removed, err := reconcilePrefix(context.Background(), cli, b.bucket, "acme/site", map[string]bool{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d on an empty manifest, want 0", removed)
	}
	if _, ok := f.body(b.bucket, "acme/site/index.html"); !ok {
		t.Fatal("an empty manifest deleted the live site")
	}
}
