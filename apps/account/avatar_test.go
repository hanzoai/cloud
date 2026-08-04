package account

// The profile-photo surface, end to end on a real mounted app.
//
// The bug these cover is an ABSENCE — there was no way to set a photo at all, and
// production said so (/v1/avatar 404 while /v1/keys 403). So the first test is
// simply that a user can now set one and get it back, and the rest hold the two
// properties that make it safe to serve an uploaded file back from an API origin
// with no credentials: the format is decided by the BYTES, and the address is the
// CONTENT.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
)

// ── fakes ────────────────────────────────────────────────────────────────────

// memVFS is deps.VFS in a map. failPut makes the object store refuse writes, which
// is the only way to reach the "stored nothing, said so" branch.
type memVFS struct {
	mu      sync.Mutex
	obj     map[string][]byte
	failPut bool
	failGet bool
}

func newMemVFS() *memVFS { return &memVFS{obj: map[string][]byte{}} }

func (m *memVFS) Put(_ context.Context, key string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut {
		return fmt.Errorf("object store down")
	}
	m.obj[key] = append([]byte(nil), payload...)
	return nil
}

func (m *memVFS) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet {
		return nil, fmt.Errorf("object store down")
	}
	b, ok := m.obj[key]
	if !ok {
		return nil, types.ErrBlobNotFound
	}
	return b, nil
}

func (m *memVFS) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.obj, key)
	return nil
}

func (m *memVFS) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.obj))
	for k := range m.obj {
		out = append(out, k)
	}
	return out
}

// lastRow is the whole row update-user was last asked to write. The photo must
// reach the system of record, not only the blob store — a row that never arrived
// means the bytes exist somewhere no surface reads.
func lastRow(t *testing.T, f *fakeIAM) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rows) == 0 {
		t.Fatal("IAM was never asked to update the user row — the photo would exist in the blob store and nowhere a surface reads")
	}
	return f.rows[len(f.rows)-1]
}

// ── harness ──────────────────────────────────────────────────────────────────

// mountAvatar builds the app with a real object store behind it, on the SAME fake
// IAM every other test in this package uses. The user row carries fields this
// package does not own, so a test can prove the whole-row re-submit preserves them.
func mountAvatar(t *testing.T) (*zip.App, *memVFS, *fakeIAM) {
	t.Helper()
	f := newFakeIAM()
	f.user["hanzo/u-antje"] = map[string]any{
		"owner": "hanzo", "name": "u-antje", "password": "$2a$hashed", "displayName": "Antje",
	}
	t.Setenv("IAM_URL", f.server(t).URL)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", "s3cr3t")

	vfs := newMemVFS()
	// The edge body limit PRODUCTION runs (config.go: GATEWAY_BODY_LIMIT, 16 MiB).
	// Left at zip's 4 MiB default this app would refuse an oversize upload at the
	// framework layer, and the handler's own 413 — the one a person reads — would be
	// unreachable and untested. That is the shape of the bug where studio's 4K
	// sources could not enqueue: a framework cap below the app's, surfacing as an
	// opaque error nobody could act on.
	app := zip.New(zip.Config{Logger: luxlog.New("test"), BodyLimit: edgeBodyLimit})
	compose(app)
	deps := cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo", Domain: "api.hanzo.ai", VFS: vfs}
	if err := MountAccount(app, deps); err != nil {
		t.Fatalf("MountAccount: %v", err)
	}
	return app, vfs, f
}

// edgeBodyLimit mirrors config.go's GATEWAY_BODY_LIMIT default.
const edgeBodyLimit = 16 << 20

// The photo cap must sit BELOW the edge body limit, or the framework refuses the
// request first and the caller gets an opaque error instead of "photo too large".
func TestPhotoCapIsReachableBeneathTheEdgeLimit(t *testing.T) {
	if maxAvatarSize >= edgeBodyLimit {
		t.Fatalf("maxAvatarSize (%d) >= edge body limit (%d): the handler's 413 can never fire, "+
			"so an oversize photo fails as a framework error nobody can act on", maxAvatarSize, edgeBodyLimit)
	}
}

// onePNG is the smallest thing that is genuinely a PNG by signature.
func onePNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("one-pixel")...)
}

// upload POSTs a multipart form exactly as a browser does.
func upload(t *testing.T, app *zip.App, user, org, filename string, data []byte) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if filename != "" {
		part, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("write part: %v", err)
		}
	} else {
		_ = mw.WriteField("notafile", "x")
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/avatar", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test POST /v1/avatar: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// fetch drives the read route with NO credentials, which is how an <img> loads it.
func fetch(t *testing.T, app *zip.App, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func photoURL(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Avatar string `json:"avatar"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if out.Avatar == "" {
		t.Fatalf("response carried no avatar url: %s", body)
	}
	return out.Avatar
}

// path strips the origin so the served app can be asked for it.
func path(url string) string {
	if i := strings.Index(url, "/v1/"); i >= 0 {
		return url[i:]
	}
	return url
}

// ── the feature ──────────────────────────────────────────────────────────────

// The whole point: a signed-in user sets a photo and it comes back. Before this
// existed the console offered "Edit in IAM" and IAM had no way to do it either.
func TestSetAndFetchProfilePhoto(t *testing.T) {
	app, vfs, iam := mountAvatar(t)
	png := onePNG()

	code, body := upload(t, app, "u-antje", "hanzo", "me.png", png)
	if code != http.StatusOK {
		t.Fatalf("upload = %d, want 200: %s", code, body)
	}
	url := photoURL(t, body)

	// The URL is ABSOLUTE and on the deployment's own public host — it is rendered
	// by an <img> on console.hanzo.ai, where a relative path would resolve against
	// the wrong origin.
	sum := sha256.Sum256(png)
	want := "https://api.hanzo.ai/v1/avatar/hanzo/u-antje/" + hex.EncodeToString(sum[:])
	if url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}

	// It is readable with NO credentials, and under its true type.
	resp, got := fetch(t, app, path(url))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch = %d, want 200 — an <img> sends no credentials", resp.StatusCode)
	}
	if !bytes.Equal(got, png) {
		t.Fatalf("fetched %d bytes, want the %d uploaded", len(got), len(png))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("every response must carry nosniff so the browser cannot re-sniff the type")
	}

	// It reached the system of record, and the re-submit did not blank the row.
	row := lastRow(t, iam)
	if row["avatar"] != url {
		t.Fatalf("IAM avatar = %v, want %q", row["avatar"], url)
	}
	if row["password"] != "$2a$hashed" {
		t.Fatalf("the whole-row re-submit dropped the password hash (%v) — that locks the user out", row["password"])
	}
	if row["displayName"] != "Antje" {
		t.Fatal("the re-submit dropped a field it does not own")
	}
	if len(vfs.keys()) != 1 {
		t.Fatalf("stored %d objects, want 1: %v", len(vfs.keys()), vfs.keys())
	}
}

// The address is the CONTENT, which is what makes replacing a photo safe: a new
// face is a new URL, so no cache anywhere can still be serving the old one.
func TestPhotoAddressIsItsContent(t *testing.T) {
	app, _, _ := mountAvatar(t)

	_, b1 := upload(t, app, "u-antje", "hanzo", "a.png", onePNG())
	_, b2 := upload(t, app, "u-antje", "hanzo", "different-name.png", onePNG())
	if photoURL(t, b1) != photoURL(t, b2) {
		t.Fatal("the same bytes must have the same address — the filename must not enter it")
	}

	other := append(onePNG(), 'x')
	_, b3 := upload(t, app, "u-antje", "hanzo", "a.png", other)
	if photoURL(t, b3) == photoURL(t, b1) {
		t.Fatal("different bytes must have a different address, or a replaced photo is a stale cache")
	}

	// Both remain fetchable: replacing does not delete, deliberately (the old URL is
	// already inside issued tokens and rendered pages).
	if resp, _ := fetch(t, app, path(photoURL(t, b1))); resp.StatusCode != http.StatusOK {
		t.Fatal("replacing a photo must not break the previous address")
	}
}

// Two users uploading the SAME image get different keys: the key is org- and
// user-scoped, so one person's photo is never addressed by another's identity.
func TestPhotoIsScopedToItsOwner(t *testing.T) {
	app, vfs, _ := mountAvatar(t)
	png := onePNG()

	_, b1 := upload(t, app, "u-antje", "hanzo", "me.png", png)
	_, b2 := upload(t, app, "u-other", "zoo", "me.png", png)
	if photoURL(t, b1) == photoURL(t, b2) {
		t.Fatal("two users' photos must not share an address")
	}
	if len(vfs.keys()) != 2 {
		t.Fatalf("stored %d objects, want 2: %v", len(vfs.keys()), vfs.keys())
	}
	for _, k := range vfs.keys() {
		if !strings.HasPrefix(k, "account/avatars/") {
			t.Fatalf("key %q escaped this subsystem's prefix in the shared bucket", k)
		}
	}
}

// ── the safety properties ────────────────────────────────────────────────────

// The format is decided by the BYTES. An SVG is a program, and one stored as a
// picture and later served under the type its NAME claimed is script running in
// this origin.
func TestOnlyRasterImagesAreAccepted(t *testing.T) {
	for name, data := range map[string]string{
		"svg":  `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"html": `<!doctype html><script>alert(1)</script>`,
		"pdf":  "%PDF-1.7\n",
		"text": "just some text",
	} {
		t.Run(name, func(t *testing.T) {
			app, vfs, _ := mountAvatar(t)
			// The NAME claims png; only the bytes are consulted.
			code, body := upload(t, app, "u-antje", "hanzo", "innocent.png", []byte(data))
			if code != http.StatusUnsupportedMediaType {
				t.Fatalf("upload = %d, want 415: %s", code, body)
			}
			if len(vfs.keys()) != 0 {
				t.Fatalf("a refused upload must store nothing, stored: %v", vfs.keys())
			}
		})
	}
}

// Defense in depth on the read: an object under an avatar key that is not an image
// is a 404, never bytes served inline. The upload already refuses these, so this
// can only fire on something written by another path — which is exactly when a
// guessed Content-Type would be an XSS.
func TestReadNeverServesNonImageBytes(t *testing.T) {
	app, vfs, _ := mountAvatar(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	sum := sha256.Sum256(svg)
	dg := hex.EncodeToString(sum[:])
	if err := vfs.Put(context.Background(), avatarKey("hanzo", "u-antje", dg), svg); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _ := fetch(t, app, "/v1/avatar/hanzo/u-antje/"+dg)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("fetch = %d, want 404 — a stored non-image must never be served", resp.StatusCode)
	}
}

// A malformed address is refused before the store is touched, and every refusal is
// the same 404 so a probe learns nothing.
func TestReadRefusesAnythingThatIsNotAPhotoAddress(t *testing.T) {
	app, _, _ := mountAvatar(t)
	good := hex.EncodeToString(func() []byte { s := sha256.Sum256(onePNG()); return s[:] }())

	for name, p := range map[string]string{
		"digest is not hex":        "/v1/avatar/hanzo/u-antje/" + strings.Repeat("z", 64),
		"digest is the wrong size": "/v1/avatar/hanzo/u-antje/abcd",
		"traversal in the org":     "/v1/avatar/..%2f..%2fetc/u-antje/" + good,
		"traversal in the user":    "/v1/avatar/hanzo/..%2f..%2fpasswd/" + good,
		"never uploaded":           "/v1/avatar/hanzo/u-nobody/" + good,
	} {
		t.Run(name, func(t *testing.T) {
			resp, _ := fetch(t, app, p)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("fetch = %d, want 404", resp.StatusCode)
			}
		})
	}
}

// A path component is REFUSED, not folded. Sanitizing maps "a/b" and "a_b" onto one
// key, and in a tenancy key that is two identities sharing an address.
func TestKeyComponentsAreRefusedNotFolded(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "a\\b", "a b", "a\x00b", strings.Repeat("a", 129)} {
		if safe(bad) {
			t.Fatalf("safe(%q) = true, want false", bad)
		}
	}
	for _, ok := range []string{"hanzo", "u-antje", "a.b_c-d", "0"} {
		if !safe(ok) {
			t.Fatalf("safe(%q) = false, want true", ok)
		}
	}
	// "a/b" and "a_b" must not become one key — the fold this refusal prevents.
	if avatarKey("a_b", "u", "d") == avatarKey("a/b", "u", "d") {
		t.Fatal("two distinct orgs collided onto one key")
	}
}

// ── the honest failures ──────────────────────────────────────────────────────

// No validated identity → refused. The subject is ALWAYS the caller's own claims,
// so there is no request value that could name someone else's photo.
func TestUnauthenticatedCannotSetAPhoto(t *testing.T) {
	app, vfs, _ := mountAvatar(t)
	code, _ := upload(t, app, "", "", "me.png", onePNG())
	if code != http.StatusUnauthorized {
		t.Fatalf("upload = %d, want 401", code)
	}
	// A user with no organization yet cannot either: the key is org-scoped.
	code, _ = upload(t, app, "u-antje", "", "me.png", onePNG())
	if code != http.StatusUnauthorized {
		t.Fatalf("org-less upload = %d, want 401", code)
	}
	if len(vfs.keys()) != 0 {
		t.Fatalf("a refused upload must store nothing, stored: %v", vfs.keys())
	}
}

// A dead object store is a 502, and the profile is NOT updated — the record must
// never point at bytes that were not written.
func TestStoreFailureIsHonestAndLeavesTheProfileAlone(t *testing.T) {
	app, vfs, iam := mountAvatar(t)
	vfs.failPut = true

	code, body := upload(t, app, "u-antje", "hanzo", "me.png", onePNG())
	if code != http.StatusBadGateway {
		t.Fatalf("upload = %d, want 502: %s", code, body)
	}
	iam.mu.Lock()
	defer iam.mu.Unlock()
	if len(iam.rows) != 0 {
		t.Fatal("the profile was pointed at a photo the store refused to write")
	}
}

// The bytes landed but the record did not: the photo exists and is not shown, so
// say that rather than reporting a success the user cannot see.
func TestPhotoStoredButProfileNotUpdatedSaysSo(t *testing.T) {
	app, _, iam := mountAvatar(t)
	// An IAM that cannot return the row: the whole-row re-submit has nothing to
	// re-submit, so the profile write fails after the bytes have landed.
	iam.mu.Lock()
	delete(iam.user, "hanzo/u-antje")
	iam.failUpdateUser = true
	iam.mu.Unlock()

	code, body := upload(t, app, "u-antje", "hanzo", "me.png", onePNG())
	if code == http.StatusOK {
		t.Fatal("a failed profile write must not report success")
	}
	if !strings.Contains(strings.ToLower(string(body)), "profile") {
		t.Fatalf("the error should name what failed, got: %s", body)
	}
}

// The two shapes a form can be wrong in.
func TestMalformedUploads(t *testing.T) {
	app, _, _ := mountAvatar(t)

	if code, _ := upload(t, app, "u-antje", "hanzo", "", nil); code != http.StatusBadRequest {
		t.Fatalf("form with no file part = %d, want 400", code)
	}
	if code, _ := upload(t, app, "u-antje", "hanzo", "empty.png", []byte{}); code != http.StatusBadRequest {
		t.Fatalf("empty file = %d, want 400", code)
	}
	big := make([]byte, maxAvatarSize+1)
	copy(big, onePNG())
	if code, _ := upload(t, app, "u-antje", "hanzo", "big.png", big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d, want 413", code)
	}
}

// avatarFor is the address any caller can name a stored photo by; it must agree
// with what the upload answered, or the two spellings drift.
func TestAvatarForMatchesWhatTheUploadAnswers(t *testing.T) {
	app, _, _ := mountAvatar(t)
	_, body := upload(t, app, "u-antje", "hanzo", "me.png", onePNG())
	sum := sha256.Sum256(onePNG())
	if got, want := photoURL(t, body), avatarFor("api.hanzo.ai", "hanzo", "u-antje", hex.EncodeToString(sum[:])); got != want {
		t.Fatalf("upload answered %q, avatarFor says %q", got, want)
	}
}
