package s3_test

// The byte plane: what a mint answers when there is nowhere public to sign
// against, and that the address it answers with is real.
//
// newApp clears S3_PUBLIC_ENDPOINT, which is the SET AND EMPTY case s3admin
// reads as "this deployment has no browser-routable store" — the exact posture
// of a cluster whose object store is internal only. Before the byte plane every
// upload and every download answered 503 there, so a Drive could list buckets
// and carry nothing.

import (
	"net/http"
	"testing"
)

// TestMintAnswersTheByteAddressWithNoPublicStore is the property the whole
// change rests on: a deployment with no public store still tells a caller where
// to send bytes, in the SAME shape a presigned mint answers, so the client that
// follows {url, method} needs to know nothing about which it got.
func TestMintAnswersTheByteAddressWithNoPublicStore(t *testing.T) {
	app := newApp(t, true)

	resp := do(t, app, http.MethodPost, "/v1/s3/buckets/photos/objects", "acme", `{"key":"a/b.txt"}`, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint upload = %d, want 200 — a store with no public host still has this API", resp.StatusCode)
	}
	got := decode(t, resp.Body)
	if got["method"] != http.MethodPut {
		t.Fatalf("method = %v, want PUT", got["method"])
	}
	if got["url"] != "/v1/s3/buckets/photos/blob/a/b.txt" {
		t.Fatalf("url = %v, want the object's byte address", got["url"])
	}
	if got["key"] != "a/b.txt" {
		t.Fatalf("key = %v, want the cleaned key", got["key"])
	}
}

// The download half, same property: the mint names the byte address and the verb
// to fetch it with.
func TestDownloadMintAnswersTheByteAddress(t *testing.T) {
	app := newApp(t, true)

	resp := do(t, app, http.MethodGet, "/v1/s3/buckets/photos/objects/a/b.txt", "acme", "", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint download = %d, want 200", resp.StatusCode)
	}
	got := decode(t, resp.Body)
	if got["method"] != http.MethodGet {
		t.Fatalf("method = %v, want GET", got["method"])
	}
	if got["url"] != "/v1/s3/buckets/photos/blob/a/b.txt" {
		t.Fatalf("url = %v, want the object's byte address", got["url"])
	}
}

// TestByteRoutesAreMountedAndOrgGated pins that the address the mint hands out
// is a real route and that it is behind the same admission every other operation
// is. A caller with no org must be refused, NOT 404 — a 404 here would mean the
// route never registered and the mint is naming somewhere that does not exist.
func TestByteRoutesAreMountedAndOrgGated(t *testing.T) {
	app := newApp(t, true)

	for _, m := range []string{http.MethodPut, http.MethodGet} {
		resp := do(t, app, m, "/v1/s3/buckets/photos/blob/a/b.txt", "", "x", false)
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("%s blob = 404 — the route the mint names is not mounted", m)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s blob with no org = %d, want 403", m, resp.StatusCode)
		}
	}
}

// A key that is not a clean object path is refused before the store is asked,
// on the byte plane exactly as on the record plane.
func TestByteRouteRefusesAnEscapingKey(t *testing.T) {
	app := newApp(t, true)

	resp := do(t, app, http.MethodPut, "/v1/s3/buckets/photos/blob/../../etc/passwd", "acme", "x", false)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("an escaping key was accepted")
	}
}
