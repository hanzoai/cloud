package team

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients/team/token"
)

// memVFS is an in-memory types.VFSClient for tests — the blob backend the files
// plane writes through. Thread-safe.
type memVFS struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemVFS() *memVFS { return &memVFS{m: map[string][]byte{}} }

func (v *memVFS) Put(_ context.Context, key string, payload []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	v.m[key] = cp
	return nil
}

func (v *memVFS) Get(_ context.Context, key string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.m[key]
	if !ok {
		return nil, fmt.Errorf("memvfs: not found")
	}
	return b, nil
}

func (v *memVFS) Delete(_ context.Context, key string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, key) // idempotent — missing key is a no-op
	return nil
}

// uploadFile drives the REAL FrontStorage upload shape: POST
// /v1/team/files/{workspace}, multipart field "file" whose FILENAME is the
// client-generated blob uuid (formData.append('file', file, uuid)).
func uploadFile(t *testing.T, app *zip.App, ws, blobID string, headers map[string]string, data []byte) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", blobID) // sets Content-Disposition filename=blobID
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/team/files/"+ws, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("upload Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// getRaw returns the raw *http.Response so header assertions (Content-Type,
// Content-Disposition) can be made.
func getRaw(t *testing.T, app *zip.App, path string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("get Test: %v", err)
	}
	return resp
}

func bearerFor(t *testing.T, acct, org string) map[string]string {
	t.Helper()
	tok, err := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + tok}
}

// pngBytes is a minimal blob whose magic bytes are the PNG signature — enough for
// imageType() to classify it image/png (it only inspects the signature).
func pngBytes(tail string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte(tail)...)
}

// TestFilesFrontStorageRoundTrip proves the REAL FrontStorage contract: POST
// /v1/team/files/:workspace with the client uuid as the multipart filename stores
// the bytes under THAT id; GET /:workspace/:filename?file=<uuid> returns them. The
// server never mints an id.
func TestFilesFrontStorageRoundTrip(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)

	blobID := uuid.NewString() // CLIENT-generated, sent as the multipart filename
	payload := []byte("hello blob \x00\x01 binary")
	if code, body := uploadFile(t, app, ws.UUID, blobID, auth, payload); code != http.StatusOK {
		t.Fatalf("upload = %d (%s)", code, body)
	}

	// getFileUrl shape: /{workspace}/{filename}?file={blobId}&workspace={workspace}
	code, got := call(t, app, http.MethodGet,
		"/v1/team/files/"+ws.UUID+"/attachment.bin?file="+blobID+"&workspace="+ws.UUID, auth, nil)
	if code != http.StatusOK {
		t.Fatalf("download = %d (%s)", code, got)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("bytes mismatch: got %q want %q", got, payload)
	}

	// Unknown blob id → 404.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+ws.UUID+"/x?file="+uuid.NewString(), auth, nil); code != http.StatusNotFound {
		t.Fatalf("unknown blob = %d, want 404", code)
	}
	// A non-uuid multipart filename is rejected (the id must be the client uuid).
	if code, _ := uploadFile(t, app, ws.UUID, "../evil", auth, payload); code != http.StatusBadRequest {
		t.Fatalf("non-uuid blob id = %d, want 400", code)
	}
}

// TestFilesCrossOrgDenied is the FILES red bar: a caller in org B can never read
// org A's blob — neither by naming org A's workspace (not a member / not its org →
// 404) nor by pointing its OWN workspace at org A's blobId (physical key embeds the
// caller's org+workspace → miss → 404). Every denial is 404 (no oracle).
func TestFilesCrossOrgDenied(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const acctA, acctB = "aaaaaaaa-0000-4000-8000-00000000000a", "bbbbbbbb-0000-4000-8000-00000000000b"
	wsA, _ := mounted.accounts.EnsureWorkspace(ctx, "org-a", acctA, "Alice")
	wsB, _ := mounted.accounts.EnsureWorkspace(ctx, "org-b", acctB, "Bob")
	authA := bearerFor(t, acctA, "org-a")
	authB := bearerFor(t, acctB, "org-b")

	blobA := uuid.NewString()
	if code, _ := uploadFile(t, app, wsA.UUID, blobA, authA, []byte("org-a secret")); code != http.StatusOK {
		t.Fatal("org-a upload failed")
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authA, nil); code != http.StatusOK {
		t.Fatalf("org-a read own blob = %d, want 200", code)
	}
	// (a) org-b naming org-a's workspace → 404.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authB, nil); code != http.StatusNotFound {
		t.Fatalf("org-b via org-a's workspace = %d, want 404", code)
	}
	// (b) org-b pointing its OWN workspace at org-a's blobId → 404 (key isolation).
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsB.UUID+"/x?file="+blobA, authB, nil); code != http.StatusNotFound {
		t.Fatalf("org-b via own workspace + org-a's blobId = %d, want 404", code)
	}
	// Unauthenticated → 401.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauth download = %d, want 401", code)
	}
	// org-b cannot even UPLOAD into org-a's workspace.
	if code, _ := uploadFile(t, app, wsA.UUID, uuid.NewString(), authB, []byte("x")); code != http.StatusNotFound {
		t.Fatalf("org-b upload into org-a's workspace = %d, want 404", code)
	}
}

// TestFilesMembershipRequired is Red F-C: same-org is NOT enough — the caller must
// be a MEMBER of the target workspace. Two members of the SAME org, each with their
// own workspace: neither can touch the other's workspace blobs.
func TestFilesMembershipRequired(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const org = "acme"
	const acctA, acctB = "aaaaaaaa-1111-4111-8111-00000000000a", "bbbbbbbb-1111-4111-8111-00000000000b"
	wsA, _ := mounted.accounts.EnsureWorkspace(ctx, org, acctA, "Alice")
	wsB, _ := mounted.accounts.EnsureWorkspace(ctx, org, acctB, "Bob")
	authA := bearerFor(t, acctA, org)
	authB := bearerFor(t, acctB, org)

	blobA := uuid.NewString()
	if code, _ := uploadFile(t, app, wsA.UUID, blobA, authA, []byte("alice private")); code != http.StatusOK {
		t.Fatal("alice upload failed")
	}
	// Bob is same org but NOT a member of Alice's workspace → download 404.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authB, nil); code != http.StatusNotFound {
		t.Fatalf("same-org non-member download = %d, want 404", code)
	}
	// …and cannot upload into it either.
	if code, _ := uploadFile(t, app, wsA.UUID, uuid.NewString(), authB, []byte("x")); code != http.StatusNotFound {
		t.Fatalf("same-org non-member upload = %d, want 404", code)
	}
	// …and cannot DELETE from it (same membership guard as download).
	if code, _ := call(t, app, http.MethodDelete, "/v1/team/files/"+wsA.UUID+"/"+blobA+"?file="+blobA, authB, nil); code != http.StatusNotFound {
		t.Fatalf("same-org non-member delete = %d, want 404", code)
	}
	// Alice's blob must survive the denied cross-member delete.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authA, nil); code != http.StatusOK {
		t.Fatalf("alice's blob destroyed by a non-member delete")
	}
	// Sanity: Bob CAN use his own workspace.
	blobB := uuid.NewString()
	if code, _ := uploadFile(t, app, wsB.UUID, blobB, authB, []byte("bob")); code != http.StatusOK {
		t.Fatalf("member upload to own workspace failed")
	}
}

// TestFilesDelete proves the FrontStorage deleteFile contract: DELETE
// /:workspace/:filename?file=:blobId removes the blob (subsequent GET → 404), and
// is idempotent + no-oracle (a second delete of the now-missing blob still 204s).
func TestFilesDelete(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, _ := mounted.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	auth := bearerFor(t, acct, org)

	blobID := uuid.NewString()
	if code, _ := uploadFile(t, app, ws.UUID, blobID, auth, []byte("bytes")); code != http.StatusOK {
		t.Fatal("upload failed")
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+ws.UUID+"/x?file="+blobID, auth, nil); code != http.StatusOK {
		t.Fatalf("pre-delete GET = %d, want 200", code)
	}
	// DELETE (front.ts shape: /:workspace/:file?file=:file) → 204.
	if code, _ := call(t, app, http.MethodDelete, "/v1/team/files/"+ws.UUID+"/"+blobID+"?file="+blobID+"&workspace="+ws.UUID, auth, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+ws.UUID+"/x?file="+blobID, auth, nil); code != http.StatusNotFound {
		t.Fatalf("post-delete GET = %d, want 404 (blob gone)", code)
	}
	// Idempotent + no-oracle: deleting the now-missing blob still 204s.
	if code, _ := call(t, app, http.MethodDelete, "/v1/team/files/"+ws.UUID+"/"+blobID+"?file="+blobID, auth, nil); code != http.StatusNoContent {
		t.Fatalf("idempotent delete = %d, want 204", code)
	}
}

// TestFilesDeleteCrossTenantDenied proves DELETE is tenant-isolated with no
// existence oracle: org-b cannot destroy org-a's blob — via org-a's workspace it is
// refused (404, not a member), and via its OWN workspace with org-a's blobId it is
// a harmless no-op (204, different physical key). org-a's blob survives both.
func TestFilesDeleteCrossTenantDenied(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const acctA, acctB = "aaaaaaaa-2222-4222-8222-00000000000a", "bbbbbbbb-2222-4222-8222-00000000000b"
	wsA, _ := mounted.accounts.EnsureWorkspace(ctx, "org-a", acctA, "Alice")
	wsB, _ := mounted.accounts.EnsureWorkspace(ctx, "org-b", acctB, "Bob")
	authA := bearerFor(t, acctA, "org-a")
	authB := bearerFor(t, acctB, "org-b")

	blobA := uuid.NewString()
	if code, _ := uploadFile(t, app, wsA.UUID, blobA, authA, []byte("org-a secret")); code != http.StatusOK {
		t.Fatal("org-a upload failed")
	}
	// (a) org-b via org-a's workspace → 404 (not a member).
	if code, _ := call(t, app, http.MethodDelete, "/v1/team/files/"+wsA.UUID+"/"+blobA+"?file="+blobA, authB, nil); code != http.StatusNotFound {
		t.Fatalf("cross-org delete via org-a ws = %d, want 404", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authA, nil); code != http.StatusOK {
		t.Fatalf("org-a blob destroyed by cross-org delete (via org-a ws)")
	}
	// (b) org-b via its OWN workspace + org-a's blobId → 204 no-op (different key).
	if code, _ := call(t, app, http.MethodDelete, "/v1/team/files/"+wsB.UUID+"/"+blobA+"?file="+blobA, authB, nil); code != http.StatusNoContent {
		t.Fatalf("org-b delete in own ws = %d, want 204 (no-op)", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authA, nil); code != http.StatusOK {
		t.Fatalf("org-a blob destroyed by cross-org delete (via org-b ws + org-a blobId) — KEY ISOLATION BROKEN")
	}
}

// TestFilesContentTypeAllowList is Red F-B: the served Content-Type derives from
// the STORED BYTES via an image allow-list, NOT the client :filename. A real PNG is
// served inline as image/png; a crafted .svg / .xhtml / .html (whose name would
// otherwise force an active type) is served application/octet-stream + attachment +
// nosniff, so it can never execute in the app origin.
func TestFilesContentTypeAllowList(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, _ := mounted.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	auth := bearerFor(t, acct, org)

	// A real PNG → inline image/png (byte-derived), nosniff, NO attachment.
	pngID := uuid.NewString()
	if code, _ := uploadFile(t, app, ws.UUID, pngID, auth, pngBytes("realpng")); code != http.StatusOK {
		t.Fatal("png upload failed")
	}
	resp := getRaw(t, app, "/v1/team/files/"+ws.UUID+"/pic.png?file="+pngID, auth)
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("png Content-Type = %q, want image/png", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("png missing nosniff")
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		t.Fatalf("png must be inline, got Content-Disposition %q", cd)
	}

	// Crafted active-content uploads, named to LOOK renderable → all served inert.
	inert := map[string][]byte{
		"attack.svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"attack.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><script>alert(1)</script></html>`),
		"attack.html":  []byte(`<script>alert(1)</script>`),
		// A .png NAME but non-image bytes → still inert (type is byte-derived).
		"fake.png": []byte(`<script>alert(1)</script>`),
	}
	for name, body := range inert {
		id := uuid.NewString()
		if code, _ := uploadFile(t, app, ws.UUID, id, auth, body); code != http.StatusOK {
			t.Fatalf("upload %s failed", name)
		}
		r := getRaw(t, app, "/v1/team/files/"+ws.UUID+"/"+name+"?file="+id, auth)
		ct := r.Header.Get("Content-Type")
		cd := r.Header.Get("Content-Disposition")
		xcto := r.Header.Get("X-Content-Type-Options")
		_ = r.Body.Close()
		if ct != "application/octet-stream" {
			t.Fatalf("%s Content-Type = %q, want application/octet-stream (inert)", name, ct)
		}
		if !bytes.HasPrefix([]byte(cd), []byte("attachment")) {
			t.Fatalf("%s Content-Disposition = %q, want attachment", name, cd)
		}
		if xcto != "nosniff" {
			t.Fatalf("%s missing X-Content-Type-Options: nosniff", name)
		}
	}
}
