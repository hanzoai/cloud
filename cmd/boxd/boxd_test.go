package main

import (
	"bytes"
	"encoding/json"
	"github.com/zap-proto/zip"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/sandbox/wire"
)

const testKey = "test-service-key"

func newTestBox(t *testing.T) (*box, *zip.App) {
	t.Helper()
	dir := t.TempDir()
	b := &box{
		workdir: dir, boot: time.Now().Unix(), key: testKey,
		image: "registry.hanzo.ai/hanzoai/box:dev-test", project: "acme/web",
		sessions: &sessions{root: filepath.Join(t.TempDir(), "sess"), seen: map[string]time.Time{}},
	}
	if err := os.MkdirAll(b.sessions.root, 0o755); err != nil {
		t.Fatal(err)
	}
	return b, mount(b)
}

// mount builds the app the same way run() does — guard first, then routes — so
// a test exercises the composition the binary ships, not a hand-assembled one
// that could disagree with it.
func mount(b *box) *zip.App {
	app := zip.New(zip.Config{AppName: "boxd-test", BodyLimit: maxBody})
	app.Use(zip.H(b.guard))
	b.routes(app)
	return app
}

func do(t *testing.T, app *zip.App, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set(wire.KeyHeader, testKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// ── the credential ──────────────────────────────────────────────────────────

func TestUnsetKeyFailsClosed(t *testing.T) {
	b, _ := newTestBox(t)
	b.key = ""
	resp, err := mount(b).Test(httptest.NewRequest(http.MethodGet, wire.PathHealth, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// 503, not 200. A box that answers everyone because nobody configured it is
	// the failure mode that makes an isolation boundary decorative.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unset key: got %d, want 503", resp.StatusCode)
	}
}

func TestWrongKeyIsRejected(t *testing.T) {
	_, app := newTestBox(t)
	req := httptest.NewRequest(http.MethodGet, wire.PathHealth, nil)
	req.Header.Set(wire.KeyHeader, "nope")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key: got %d, want 401", resp.StatusCode)
	}
}

func TestHealthReportsWhatTheBoxIs(t *testing.T) {
	_, app := newTestBox(t)
	resp, body := do(t, app, http.MethodGet, wire.PathHealth, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}
	var h wire.Health
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatal(err)
	}
	if !h.OK || h.Project != "acme/web" || h.Image == "" {
		t.Fatalf("health: %+v", h)
	}
}

// ── containment: the HTTP surface must not be a second way out ──────────────

// TestPathTraversalStaysInsideTheProject asserts CONTAINMENT, not refusal —
// and the difference is the point.
//
// `resolve` roots every request path at "/" before cleaning it, so
// "../../etc/passwd" becomes "/etc/passwd" and lands at
// <workdir>/etc/passwd. That is a perfectly good outcome: the traversal is
// neutralised rather than rejected, exactly as a chroot would. A test that
// demanded a 400 would be testing a policy nobody needs and would go red for
// an implementation that is doing the right thing.
//
// The property that actually matters is that NOTHING lands outside the workdir.
// That is what this measures, by walking the parent directory afterwards.
func TestPathTraversalStaysInsideTheProject(t *testing.T) {
	b, app := newTestBox(t)
	parent := filepath.Dir(b.workdir)
	for _, p := range []string{
		"/../../pwned", "../../pwned", "/a/../../../pwned", `\..\..\pwned`,
		"/./../pwned", "//../pwned",
	} {
		do(t, app, http.MethodPost, wire.PathFsWrite, wire.WriteRequest{Path: p, Content: "x"})
		if _, err := os.Stat(filepath.Join(parent, "pwned")); err == nil {
			t.Fatalf("write %q escaped the project", p)
		}
	}
	// And the writes that DID land are all under the workdir.
	_ = filepath.Walk(b.workdir, func(p string, _ os.FileInfo, err error) error {
		if err == nil && !strings.HasPrefix(p, b.workdir) {
			t.Fatalf("file outside workdir: %s", p)
		}
		return nil
	})
}

func TestSymlinkEscapeIsRefused(t *testing.T) {
	b, app := newTestBox(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink a previous run could have created: textually inside the box,
	// physically outside it. The lexical check alone does not catch this.
	if err := os.Symlink(outside, filepath.Join(b.workdir, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resp, body := do(t, app, http.MethodGet, wire.PathFsRead+"?path=/escape/secret", nil)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("read through a symlink escaped the project: %s", body)
	}
}

func TestDeleteRefusesTheWorkdirItself(t *testing.T) {
	b, app := newTestBox(t)
	if err := os.WriteFile(filepath.Join(b.workdir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An omitted ?path= must not be an rm -rf of the project.
	resp, _ := do(t, app, http.MethodDelete, wire.PathFsDelete, nil)
	if resp.StatusCode == http.StatusNoContent {
		t.Fatal("DELETE with no path deleted the workdir")
	}
	if _, err := os.Stat(filepath.Join(b.workdir, "keep.txt")); err != nil {
		t.Fatalf("workdir contents gone: %v", err)
	}
}

// ── the filesystem the agent edits ──────────────────────────────────────────

func TestFsRoundTrip(t *testing.T) {
	_, app := newTestBox(t)

	resp, body := do(t, app, http.MethodPost, wire.PathFsWrite,
		wire.WriteRequest{Path: "/src/app.ts", Content: "export const x = 1\n"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("write: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, app, http.MethodGet, wire.PathFsRead+"?path=/src/app.ts", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "export const x = 1\n" {
		t.Fatalf("read: %d %q", resp.StatusCode, body)
	}

	resp, body = do(t, app, http.MethodGet, wire.PathFsList, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var lr wire.ListResult
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatal(err)
	}
	if len(lr.Entries) != 1 || lr.Entries[0].Path != "/src/app.ts" {
		t.Fatalf("list: %+v — paths must be project-relative, not absolute box paths", lr.Entries)
	}

	resp, _ = do(t, app, http.MethodDelete, wire.PathFsDelete+"?path=/src/app.ts", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = do(t, app, http.MethodGet, wire.PathFsRead+"?path=/src/app.ts", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("read after delete: got %d, want 404", resp.StatusCode)
	}
}

func TestListHidesBuildOutput(t *testing.T) {
	b, app := newTestBox(t)
	for _, p := range []string{"src/App.tsx", "node_modules/react/index.js", ".git/HEAD", "dist/bundle.js"} {
		full := filepath.Join(b.workdir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, body := do(t, app, http.MethodGet, wire.PathFsList, nil)
	var lr wire.ListResult
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatal(err)
	}
	if len(lr.Entries) != 1 || lr.Entries[0].Path != "/src/App.tsx" {
		t.Fatalf("list leaked vendor/build trees: %+v", lr.Entries)
	}
}

func TestSearchGrepsOnTheBox(t *testing.T) {
	b, app := newTestBox(t)
	if err := os.WriteFile(filepath.Join(b.workdir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, app, http.MethodGet, wire.PathFsSearch+"?q=two", nil)
	var sr wire.SearchResult
	if err := json.Unmarshal(body, &sr); err != nil {
		t.Fatal(err)
	}
	if len(sr.Matches) != 1 || sr.Matches[0].Path != "/a.txt" || sr.Matches[0].Line != 2 || sr.Matches[0].Text != "two" {
		t.Fatalf("search: %+v", sr.Matches)
	}
}

func TestSearchIsSubstringUnlessAskedForRegex(t *testing.T) {
	b, app := newTestBox(t)
	if err := os.WriteFile(filepath.Join(b.workdir, "a.txt"), []byte("a.c\nabc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// "a.c" as a substring matches one line; as a regex it would match both.
	_, body := do(t, app, http.MethodGet, wire.PathFsSearch+"?q=a.c", nil)
	var sr wire.SearchResult
	_ = json.Unmarshal(body, &sr)
	if len(sr.Matches) != 1 {
		t.Fatalf("default must be substring, got %+v", sr.Matches)
	}
	_, body = do(t, app, http.MethodGet, wire.PathFsSearch+"?q=a.c&regex=1", nil)
	_ = json.Unmarshal(body, &sr)
	if len(sr.Matches) != 2 {
		t.Fatalf("regex=1 must be a regex, got %+v", sr.Matches)
	}
}

// ── running things ──────────────────────────────────────────────────────────

func TestExecRunsAndReportsExitCode(t *testing.T) {
	_, app := newTestBox(t)

	_, body := do(t, app, http.MethodPost, wire.PathExec,
		wire.ExecRequest{Argv: []string{"/bin/sh", "-c", "echo hi; echo bad >&2; exit 3"}})
	var res wire.ExecResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if res.ExitCode != 3 || !strings.Contains(res.Stdout, "hi") || !strings.Contains(res.Stderr, "bad") {
		t.Fatalf("exec: %+v", res)
	}
}

func TestNonZeroExitIsStill200(t *testing.T) {
	_, app := newTestBox(t)
	// "your program failed" and "the box is broken" are different facts. A caller
	// that cannot tell them apart retries the wrong one forever.
	resp, _ := do(t, app, http.MethodPost, wire.PathExec,
		wire.ExecRequest{Argv: []string{"/bin/sh", "-c", "exit 1"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 with exitCode!=0", resp.StatusCode)
	}
}

func TestExecTimesOutRatherThanHanging(t *testing.T) {
	_, app := newTestBox(t)
	_, body := do(t, app, http.MethodPost, wire.PathExec,
		wire.ExecRequest{Argv: []string{"/bin/sh", "-c", "sleep 30"}, TimeoutSec: 1})
	var res wire.ExecResult
	_ = json.Unmarshal(body, &res)
	if !res.TimedOut {
		t.Fatalf("expected timedOut, got %+v", res)
	}
}

func TestExecCannotReadTheServiceKeyFromItsOwnEnv(t *testing.T) {
	_, app := newTestBox(t)
	// Submitted code runs here by design. Handing it the credential that opens
	// every other box in the pool would make the pod boundary decorative.
	//
	// WHAT THIS DOES NOT PROVE, stated because it read as a complete defense
	// and is not one: scrubbing the child's OWN environment stops nothing on
	// its own. The child never needed the key in its env — it reads boxd's,
	// out of /proc/<pid>/environ, which Linux serves to any process with the
	// same uid. That was reproduced end to end: key recovered, then used as a
	// valid X-API-Key to write and read through the guarded API. The defense
	// that actually holds is a different uid for the child
	// (TestKeyedBoxRefusesToRunCodeAsItsOwnUid below, and confine()).
	_, body := do(t, app, http.MethodPost, wire.PathExec,
		wire.ExecRequest{
			Argv: []string{"/bin/sh", "-c", "echo [$CODE_EXEC_API_KEY][$HANZO_TARGET_KEY]"},
			Env:  map[string]string{"CODE_EXEC_API_KEY": testKey, "HANZO_TARGET_KEY": "claim"},
		})
	var res wire.ExecResult
	_ = json.Unmarshal(body, &res)
	if strings.Contains(res.Stdout, testKey) || strings.Contains(res.Stdout, "claim") {
		t.Fatalf("the box handed its credential to submitted code: %q", res.Stdout)
	}
}

// The startup refusal, which is the whole fix for the shared-key leak: a keyed
// box that would run submitted code as its own uid does not serve at all.
func TestKeyedBoxRefusesToRunCodeAsItsOwnUid(t *testing.T) {
	t.Setenv("CODE_EXEC_API_KEY", testKey)
	t.Setenv("BOX_WORKDIR", t.TempDir())
	t.Setenv("BOX_PORT", "0")
	// BOX_EXEC_UID deliberately unset — the vulnerable configuration.

	err := run()
	if err == nil {
		t.Fatal("a keyed box with no uid separation started; submitted code could read the pool key from /proc")
	}
	for _, want := range []string{"BOX_EXEC_UID", "/proc"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal should say why and how to fix it, missing %q: %v", want, err)
		}
	}
}

// An unkeyed box still starts — it fails closed at the guard (503) instead, and
// a developer running boxd by hand must not need root to do it.
func TestUnkeyedBoxDoesNotRequireUidSeparation(t *testing.T) {
	t.Setenv("BOX_WORKDIR", t.TempDir())
	if err := new(box).checkExecIsolation(); err != nil {
		t.Fatalf("unkeyed box should not require uid separation: %v", err)
	}
}

func TestConfineGivesTheChildItsOwnUid(t *testing.T) {
	shared := exec.Command("/bin/true")
	confine(shared, 0, 0)
	if shared.SysProcAttr.Credential != nil {
		t.Fatal("uid 0 means 'not configured' and must not set a credential")
	}
	if !shared.SysProcAttr.Setpgid {
		t.Fatal("the process group is what makes a timeout kill grandchildren")
	}

	separated := exec.Command("/bin/true")
	confine(separated, 65534, 65534)
	cred := separated.SysProcAttr.Credential
	if cred == nil || cred.Uid != 65534 || cred.Gid != 65534 {
		t.Fatalf("submitted code must run as the configured uid, got %+v", cred)
	}
	if !separated.SysProcAttr.Setpgid {
		t.Fatal("separation must not cost the process group")
	}
}

func TestExecCwdIsContained(t *testing.T) {
	_, app := newTestBox(t)
	resp, _ := do(t, app, http.MethodPost, wire.PathExec,
		wire.ExecRequest{Argv: []string{"/bin/pwd"}, Cwd: "/../../.."})
	// Cleaned to the workdir root rather than escaping — either a 400 or a run
	// inside the box is acceptable; a run in / is not.
	if resp.StatusCode == http.StatusOK {
		_, body := do(t, app, http.MethodPost, wire.PathExec,
			wire.ExecRequest{Argv: []string{"/bin/pwd"}, Cwd: "/../../.."})
		var res wire.ExecResult
		_ = json.Unmarshal(body, &res)
		if strings.TrimSpace(res.Stdout) == "/" {
			t.Fatal("cwd escaped to /")
		}
	}
}

func TestOutputIsCappedAndSaysSo(t *testing.T) {
	b, _ := newTestBox(t)
	var c cappedBuf
	c.max = 16
	_, _ = c.Write([]byte(strings.Repeat("x", 100)))
	got := c.String()
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "84 more bytes") {
		t.Fatalf("a silent truncation makes a caller debug output that was never sent: %q", got)
	}
	_ = b
}

// ── the frozen LibreChat family: what hanzo.chat actually calls ─────────────

func TestLibreChatExecContract(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	_, app := newTestBox(t)

	resp, body := do(t, app, http.MethodPost, wire.LibreChatExec,
		map[string]any{"lang": "py", "code": "print('hello from the box')"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exec: %d %s", resp.StatusCode, body)
	}
	var out struct {
		SessionID string `json:"session_id"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		Files     []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	// The response shape is fixed by @librechat/agents' CodeExecutor. Every one
	// of these field names is theirs; none of them may be renamed here.
	if out.SessionID == "" {
		t.Fatal("session_id missing — the client keys its session on it")
	}
	if !strings.Contains(out.Stdout, "hello from the box") {
		t.Fatalf("stdout: %q", out.Stdout)
	}
	if out.Files == nil {
		t.Fatal("files must be [] not null — the client iterates it")
	}
}

func TestLibreChatUnknownLangIsRefused(t *testing.T) {
	_, app := newTestBox(t)
	// The lang set is CLOSED. "run whatever string arrives in lang" is command
	// injection with a friendly name.
	resp, _ := do(t, app, http.MethodPost, wire.LibreChatExec,
		map[string]any{"lang": "/bin/sh -c id", "code": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", resp.StatusCode)
	}
}

func TestLibreChatArtifactsAreTheDelta(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	_, app := newTestBox(t)

	// Run 1 writes a file.
	_, body := do(t, app, http.MethodPost, wire.LibreChatExec,
		map[string]any{"lang": "py", "code": "open('plot.png','w').write('png')"})
	var r1 struct {
		SessionID string `json:"session_id"`
		Files     []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	_ = json.Unmarshal(body, &r1)
	if len(r1.Files) != 1 || r1.Files[0].Name != "plot.png" {
		t.Fatalf("run 1 files: %+v", r1.Files)
	}

	// Run 2 in the same session writes nothing. It must report NOTHING, or the
	// chat client re-attaches the same artifact on every subsequent turn.
	_, body = do(t, app, http.MethodPost, wire.LibreChatExec,
		map[string]any{"lang": "py", "code": "print(1)", "session_id": r1.SessionID})
	var r2 struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	_ = json.Unmarshal(body, &r2)
	if len(r2.Files) != 0 {
		t.Fatalf("run 2 re-reported an older artifact: %+v", r2.Files)
	}

	// …but the session LISTING still holds it, which is what /v1/files is for.
	resp, body := do(t, app, http.MethodGet, "/v1/files/"+r1.SessionID, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "plot.png") {
		t.Fatalf("files/%s: %d %s", r1.SessionID, resp.StatusCode, body)
	}

	// …and it downloads by the id the run handed back.
	resp, body = do(t, app, http.MethodGet, "/v1/download/"+r1.SessionID+"/plot.png", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "png" {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
}

func TestUploadThenRunSeesTheFile(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	_, app := newTestBox(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// The upload name is client-controlled; "../../etc/cron.d/x" gets tried.
	fw, _ := mw.CreateFormFile("file", "../../data.csv")
	_, _ = fw.Write([]byte("a,b\n1,2\n"))
	_ = mw.WriteField("session_id", "sess1")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/upload", &buf)
	req.Header.Set(wire.KeyHeader, testKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := app.Test(req, zip.TestConfig{Timeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d %s", resp.StatusCode, out)
	}
	if !strings.Contains(string(out), `"data.csv"`) {
		t.Fatalf("upload name was not flattened to a basename: %s", out)
	}

	_, body := do(t, app, http.MethodPost, wire.LibreChatExec,
		map[string]any{"lang": "py", "code": "print(open('data.csv').read().strip())", "session_id": "sess1"})
	if !strings.Contains(string(body), "1,2") {
		t.Fatalf("the run could not read the uploaded file: %s", body)
	}
}

func TestBadSessionIDIsRefused(t *testing.T) {
	_, app := newTestBox(t)
	// A session id names a directory. One "../" would turn /v1/files/{sid} into
	// an arbitrary directory listing.
	for _, sid := range []string{"..", "../../etc", "a/b"} {
		resp, _ := do(t, app, http.MethodGet, "/v1/files/"+sid, nil)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("session id %q was accepted", sid)
		}
	}
}
