package team

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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

// TestFilesUploadDownloadRoundTrip proves the blob contract: POST returns a blobId,
// GET by that blobId streams the exact bytes back — org-scoped by the verified
// session token.
func TestFilesUploadDownloadRoundTrip(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	auth := map[string]string{"Authorization": "Bearer " + sess}
	payload := []byte("hello blob \x00\x01 binary")

	code, body := call(t, app, http.MethodPost, "/v1/team/files?workspace="+ws.UUID, auth, payload)
	if code != http.StatusOK {
		t.Fatalf("upload = %d (%s)", code, body)
	}
	blobID := string(bytes.TrimSpace(body))
	if blobID == "" {
		t.Fatal("upload returned an empty blobId")
	}

	code, got := call(t, app, http.MethodGet, "/v1/team/files/"+ws.UUID+"/file.bin?file="+blobID, auth, nil)
	if code != http.StatusOK {
		t.Fatalf("download = %d (%s)", code, got)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes mismatch: got %q want %q", got, payload)
	}

	// Unknown blobId in the caller's own workspace → 404 (no such blob).
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+ws.UUID+"/x?file=nonexistent", auth, nil); code != http.StatusNotFound {
		t.Fatalf("unknown blob = %d, want 404", code)
	}
}

// TestFilesCrossOrgDenied is the FILES red bar: a caller in org B can never read
// org A's blob — neither by naming org A's workspace (workspace-not-in-org → 404)
// nor by pointing its OWN workspace at org A's blobId (physical key embeds the
// caller's org → miss → 404). Two independent layers, both 404 (no oracle).
func TestFilesCrossOrgDenied(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const acctA, acctB = "aaaaaaaa-0000-4000-8000-00000000000a", "bbbbbbbb-0000-4000-8000-00000000000b"
	wsA, _ := mounted.accounts.EnsureWorkspace(ctx, "org-a", acctA, "Alice")
	wsB, _ := mounted.accounts.EnsureWorkspace(ctx, "org-b", acctB, "Bob")

	tokA, _ := token.Generate(acctA, "", map[string]any{"org": "org-a"}, expUnix(sessionTokenTTL), testSecret)
	tokB, _ := token.Generate(acctB, "", map[string]any{"org": "org-b"}, expUnix(sessionTokenTTL), testSecret)
	authA := map[string]string{"Authorization": "Bearer " + tokA}
	authB := map[string]string{"Authorization": "Bearer " + tokB}

	// org-a uploads a secret blob.
	code, body := call(t, app, http.MethodPost, "/v1/team/files?workspace="+wsA.UUID, authA, []byte("org-a secret"))
	if code != http.StatusOK {
		t.Fatalf("org-a upload = %d (%s)", code, body)
	}
	blobA := string(bytes.TrimSpace(body))

	// org-a can read its own blob.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authA, nil); code != http.StatusOK {
		t.Fatalf("org-a read own blob = %d, want 200", code)
	}
	// (a) org-b naming org-a's workspace → 404 (workspace not in org-b).
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, authB, nil); code != http.StatusNotFound {
		t.Fatalf("org-b via org-a's workspace = %d, want 404 (cross-tenant leak)", code)
	}
	// (b) org-b pointing its OWN workspace at org-a's blobId → 404 (key mismatch).
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsB.UUID+"/x?file="+blobA, authB, nil); code != http.StatusNotFound {
		t.Fatalf("org-b via own workspace + org-a's blobId = %d, want 404 (key isolation)", code)
	}
	// An unauthenticated download is refused.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/files/"+wsA.UUID+"/x?file="+blobA, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauth download = %d, want 401", code)
	}
}

// TestFilesHTMLServedInert proves the stored-XSS guard: a blob whose filename ends
// .html is served as application/octet-stream, never text/html — a stored blob can
// never execute as a document in the app origin.
func TestFilesHTMLServedInert(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, _ := mounted.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	sess, _ := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	auth := map[string]string{"Authorization": "Bearer " + sess}

	_, body := call(t, app, http.MethodPost, "/v1/team/files?workspace="+ws.UUID, auth, []byte("<script>alert(1)</script>"))
	blobID := string(bytes.TrimSpace(body))

	// Raw request so we can read the Content-Type header.
	req := httptest.NewRequest(http.MethodGet, "/v1/team/files/"+ws.UUID+"/evil.html?file="+blobID, nil)
	req.Header.Set("Authorization", "Bearer "+sess)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("html blob Content-Type = %q, want application/octet-stream (inert)", ct)
	}
}
