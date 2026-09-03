package exec

// exec_test.go — the code-interpreter contract, measured end to end.
//
// Every assertion here is a fact the CALLERS depend on, taken from what they
// actually do: @hanzochat/agents CodeExecutor for POST /exec, and hanzo.chat's
// api/server/services/Files/Code for upload, download and the session listing. The
// wire is theirs, so the tests are about their shapes and not about ours.

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// servePeer is the shared sandboxes peer (internal/planetest): the five ops
// apps/sandbox publishes, on a real socket, with a map where the pod would be. Every
// assertion below therefore goes through the actual composition — cloud.Ask resolves
// the app, zip dispatches the op, the reply decodes into the declared type — so an
// op renamed or a field moved fails here rather than in production.
func servePeer(t *testing.T) *planetest.Sandboxes { return planetest.ServeSandboxes(t) }

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	t.Setenv("CLOUD_BRAND", "hanzo")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

func call(t *testing.T, app *zip.App, method, path, ctype string, body io.Reader) *http.Response {
	t.Helper()
	rq := httptest.NewRequest(method, "http://api.hanzo.ai"+path, body)
	// A validated principal — the user claim is what makes the org trusted. There
	// is no service key to present any more; IAM is the only way in.
	rq.Header.Set("X-User-Id", "u_alice")
	rq.Header.Set("X-Org-Id", "hanzo")
	if ctype != "" {
		rq.Header.Set("Content-Type", ctype)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func post(t *testing.T, app *zip.App, path string, v any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(v)
	return call(t, app, http.MethodPost, path, "application/json", bytes.NewReader(b))
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var out T
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return out
}

// TestExecRunsInASandboxAndAnswersTheContract is the whole subsystem in one call:
// a snippet goes in, the program runs in a leased sandbox, and what comes back is
// the four fields the CodeExecutor tool reads — session_id, stdout, stderr, files.
func TestExecRunsInASandboxAndAnswersTheContract(t *testing.T) {
	p := servePeer(t)
	p.Run = func(id string, argv []string) (string, string, int, map[string][]byte) {
		return "hello\n", "", 0, map[string][]byte{"plot.png": []byte("\x89PNG")}
	}
	app := mount(t)

	res := decode[CodeResult](t, post(t, app, Path, CodeRun{Lang: "py", Code: "print('hello')"}))
	if res.SessionID == "" {
		t.Fatal("no session_id — the client keys every later upload, download and listing on it")
	}
	if res.Stdout != "hello\n" || res.Stderr != "" {
		t.Errorf("stdout/stderr = %q/%q, want the program's own output", res.Stdout, res.Stderr)
	}
	if len(res.Files) != 1 || res.Files[0].ID != "plot.png" || res.Files[0].Name != "plot.png" {
		t.Fatalf("files = %+v, want the one artifact the run wrote", res.Files)
	}

	// The program was written into the session before it ran, under the name the
	// language table gives it — the sandbox is where the code IS, not just where it
	// executes.
	if pod := p.Pod(res.SessionID); pod == nil {
		t.Fatal("no sandbox was leased")
	} else if string(pod.Files["main.py"]) != "print('hello')" {
		t.Errorf("main.py = %q, want the submitted code", pod.Files["main.py"])
	}
}

// TestTheSourceFileIsNotReportedAsAnArtifact is the reason the marker is stamped
// INSIDE the run command rather than before it. Write the program, stamp, run: the
// program is older than the mark, so the sweep does not report the code back to the
// caller as a file its own run produced.
func TestTheSourceFileIsNotReportedAsAnArtifact(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) {
		return "", "", 0, nil
	}
	app := mount(t)
	res := decode[CodeResult](t, post(t, app, Path, CodeRun{Lang: "py", Code: "pass"}))
	for _, f := range res.Files {
		if f.ID == "main.py" {
			t.Fatalf("the source file came back as an artifact: %+v", res.Files)
		}
	}
	if len(res.Files) != 0 {
		t.Errorf("files = %+v, want none — this run wrote nothing", res.Files)
	}
	// And the ordering that makes it true is visible: the marker line is the run,
	// and the sweep comes after it.
	lines := p.Lines()
	if len(lines) < 2 || !strings.HasPrefix(lines[0], ": > "+marker) ||
		!strings.Contains(lines[1], "-newer "+marker) {
		t.Errorf("ran %v, want the marker+program line then the -newer sweep", lines)
	}
}

// TestSessionIsResumed: the session_id the client sends back names the SAME sandbox,
// which is what makes a conversation's files still be there on the next turn.
func TestSessionIsResumed(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "1", "", 0, nil }
	app := mount(t)

	first := decode[CodeResult](t, post(t, app, Path, CodeRun{Lang: "py", Code: "x=1"}))
	second := decode[CodeResult](t, post(t, app, Path,
		CodeRun{Lang: "py", Code: "x=2", SessionID: first.SessionID}))
	if second.SessionID != first.SessionID {
		t.Fatalf("session %q became %q — a resumed session must be the same sandbox",
			first.SessionID, second.SessionID)
	}
}

// TestArgsReachTheProgramAndNotTheCompiler pins the one thing the language table
// would get wrong if it appended `"$@"` instead of placing it: for a compiled
// language the arguments belong to the produced binary, not to the compiler.
func TestArgsReachTheProgramAndNotTheCompiler(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "", "", 0, nil }
	app := mount(t)

	post(t, app, Path, CodeRun{Lang: "c", Code: "int main(){}", Args: []string{"alpha", "beta"}})
	if got := p.Args(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("args reached the shell as %v, want [alpha beta]", got)
	}
	line := p.Lines()[0]
	if !strings.Contains(line, `./main "$@"`) {
		t.Errorf("c runs %q — the arguments must be applied to ./main, never to cc", line)
	}
}

// TestUnsupportedLanguageIsRefused: the tool schema advertises a closed set, so a
// lang outside it is a 400 that names the set — not a shell line that fails later
// with a message about a missing binary.
func TestUnsupportedLanguageIsRefused(t *testing.T) {
	servePeer(t)
	app := mount(t)
	resp := post(t, app, Path, CodeRun{Lang: "brainfuck", Code: "+"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), "py") {
		t.Errorf("body %q does not name the supported set", b)
	}
}

// TestNonZeroExitIsA200: "the code threw" and "the interpreter is down" are
// different facts. The tool renders stderr; it never sees a 5xx for a program that
// merely failed.
func TestNonZeroExitIsA200(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) {
		return "", "Traceback...\nZeroDivisionError\n", 1, nil
	}
	app := mount(t)
	resp := post(t, app, Path, CodeRun{Lang: "py", Code: "1/0"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a failed program is a successful call", resp.StatusCode)
	}
	if res := decode[CodeResult](t, resp); !strings.Contains(res.Stderr, "ZeroDivisionError") {
		t.Errorf("stderr = %q, want the program's traceback", res.Stderr)
	}
}

// ---- the file surface ------------------------------------------------------

func uploadFile(t *testing.T, app *zip.App, session, name, content string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if session != "" {
		_ = w.WriteField("session_id", session)
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(content))
	_ = w.Close()
	return call(t, app, http.MethodPost, Path+"/upload", w.FormDataContentType(), &body)
}

// TestUploadAnswersTheShapeTheClientChecks. crud.js reads `message` FIRST and
// throws unless it is the literal "success", then builds `${session_id}/${fileId}`.
// Both facts are load-bearing and neither is inferable from the other.
func TestUploadAnswersTheShapeTheClientChecks(t *testing.T) {
	servePeer(t)
	app := mount(t)
	res := decode[uploaded](t, uploadFile(t, app, "", "data.csv", "id,v\n1,2\n"))
	if res.Message != "success" {
		t.Errorf("message = %q, want the literal \"success\" — the client throws on anything else", res.Message)
	}
	if res.SessionID == "" || len(res.Files) != 1 || res.Files[0].FileID == "" ||
		res.Files[0].Filename != "data.csv" {
		t.Fatalf("upload answered %+v, want a session and one {fileId, filename}", res)
	}
}

// TestUploadThenExecSeesTheFile is the whole reason a session is a sandbox: the
// bytes are already where the next run will look for them, with nothing copied.
func TestUploadThenExecSeesTheFile(t *testing.T) {
	p := servePeer(t)
	app := mount(t)
	up := decode[uploaded](t, uploadFile(t, app, "", "data.csv", "id,v\n1,2\n"))

	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "read\n", "", 0, nil }
	res := decode[CodeResult](t, post(t, app, Path,
		CodeRun{Lang: "py", Code: "open('data.csv')", SessionID: up.SessionID}))
	if res.SessionID != up.SessionID {
		t.Fatalf("exec ran in %q, not the uploaded session %q", res.SessionID, up.SessionID)
	}
	if got := string(p.Pod(res.SessionID).Files["data.csv"]); got != "id,v\n1,2\n" {
		t.Errorf("data.csv in the run's sandbox = %q, want the uploaded bytes", got)
	}
}

// TestDownloadIsTwoSegmentsAndAnswersBytes. The path is {session_id}/{fileId}
// (crud.js getCodeOutputDownloadStream, process.js), and the body is the artifact
// itself — not JSON, and not a base64 field.
func TestDownloadIsTwoSegmentsAndAnswersBytes(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) {
		return "", "", 0, map[string][]byte{"plot.png": []byte("\x89PNG\r\n\x1a\n")}
	}
	app := mount(t)
	res := decode[CodeResult](t, post(t, app, Path, CodeRun{Lang: "py", Code: "savefig"}))

	resp := call(t, app, http.MethodGet, Path+"/download/"+res.SessionID+"/"+res.Files[0].ID, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "\x89PNG\r\n\x1a\n" {
		t.Errorf("body = %q, want the artifact's bytes verbatim", b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("Content-Type = %q, want it derived from the name", ct)
	}
	// A one-segment path is not this contract and must not be guessed at.
	if r := call(t, app, http.MethodGet, Path+"/download/"+res.Files[0].ID, "", nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("one-segment download = %d, want 400", r.StatusCode)
	}
}

// TestFilesAnswersABareArrayKeyedByTheDownloadIdentifier. getSessionInfo does
// `response.data.find((f) => f.name.startsWith(path))` where `path` is
// "{session}/{fileId}" — so the body is an ARRAY and `name` is that whole
// identifier, not the bare filename. An object wrapper or a short name breaks it
// silently: `.find` returns undefined and the caller reads it as "no such file".
func TestFilesAnswersABareArrayKeyedByTheDownloadIdentifier(t *testing.T) {
	servePeer(t)
	app := mount(t)
	up := decode[uploaded](t, uploadFile(t, app, "", "data.csv", "x"))

	resp := call(t, app, http.MethodGet, Path+"/files/"+up.SessionID, "", nil)
	raw, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		t.Fatalf("body = %s, want a BARE JSON array", raw)
	}
	var rows []listing
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	want := up.SessionID + "/data.csv"
	for _, r := range rows {
		if r.Name == want {
			return
		}
	}
	t.Fatalf("listing %+v has no row named %q — the client matches on that prefix", rows, want)
}

// ---- auth ------------------------------------------------------------------

// TestNoPrincipalIsRefusedOnEveryPath: with no service key left, a caller who
// presents no validated principal has nothing else to present, on any path.
func TestNoPrincipalIsRefusedOnEveryPath(t *testing.T) {
	servePeer(t)
	app := mount(t)
	for _, p := range []string{Path, Path + "/upload", Path + "/download/s/f", Path + "/files/s"} {
		rq := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai"+p, nil)
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s with no principal = 200, want a refusal", p)
		}
		_ = resp.Body.Close()
	}
}

// TestAnOrgHeaderAloneIsNotAPrincipal, including the TYPED op. An org header with
// no user claim is a tenant the caller chose for itself; principal.OrgOf is what
// makes the difference, and every path reads the same answer because tenancy is a
// property of the request rather than of the route.
func TestAnOrgHeaderAloneIsNotAPrincipal(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "ran", "", 0, nil }
	app := mount(t)
	for _, path := range []string{Path, Path + "/upload", Path + "/download/s/f", Path + "/files/s"} {
		rq := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai"+path,
			strings.NewReader(`{"lang":"py","code":"x=1"}`))
		rq.Header.Set("X-Org-Id", "acme")
		rq.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s with a bare org header = 200, want a refusal", path)
		}
		_ = resp.Body.Close()
	}
	if n := p.Ran(); n != 0 {
		t.Fatalf("%d program(s) ran for a caller that never presented a principal", n)
	}
}

// TestMountRejectsBadInputs.
func TestMountRejectsBadInputs(t *testing.T) {
	if err := Use(nil, cloud.Deps{}); err == nil {
		t.Fatal("Use(nil app) should error")
	}
}

// TestProgrammaticRefusesInTheOpen. /exec/programmatic is a different protocol —
// a run suspended on each tool call and resumed from a continuation token — so it
// answers 501 rather than being routed into the plain interpreter, which would hand
// the caller a body its parser cannot read.
func TestProgrammaticRefusesInTheOpen(t *testing.T) {
	servePeer(t)
	app := mount(t)
	resp := post(t, app, Path+"/programmatic", map[string]any{"code": "x=1"})
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), "continuation token") {
		t.Errorf("body %q does not say what would be needed to serve it", b)
	}
}

// TestAttachedFilesAreNotSilentlyDropped. hanzo.chat primes a user's attachment as
// {id, session_id, name} (Files/Code/process.js pushFile) while @hanzochat/agents
// spells the same field `storage_session_id` (tools.d.ts FileRef). Reading only the
// second meant every chat-attached file arrived with an empty session, was skipped
// by the copy loop, AND was skipped by the "not available" note — so a user's CSV
// was invisible to the program with nothing anywhere saying why.
func TestAttachedFilesAreNotSilentlyDropped(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "", "", 0, nil }
	app := mount(t)

	// A file uploaded in one session, then attached to a run in another.
	up := decode[uploaded](t, uploadFile(t, app, "", "data.csv", "id,v\n1,2\n"))
	res := decode[CodeResult](t, post(t, app, Path, CodeRun{
		Lang: "py", Code: "open('data.csv')",
		// The chat's spelling, NOT the agents one.
		Files: []CodeFile{{ID: "data.csv", Name: "data.csv", SessionID: up.SessionID}},
	}))
	got := p.Pod(res.SessionID)
	if got == nil {
		t.Fatal("no sandbox was leased")
	}
	if string(got.Files["data.csv"]) != "id,v\n1,2\n" {
		t.Fatalf("the attached file did not reach the run's sandbox (files: %v) — `session_id` "+
			"is the spelling hanzo.chat actually sends", sortedKeys(got.Files))
	}
}

// TestAFileWithNoSessionSaysSo. The other half of the same silence: a ref naming
// bytes this deployment cannot find must be reported, not skipped.
func TestAFileWithNoSessionSaysSo(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "", "", 0, nil }
	app := mount(t)

	res := decode[CodeResult](t, post(t, app, Path, CodeRun{
		Lang: "py", Code: "pass",
		Files: []CodeFile{{ID: "ghost.csv", Name: "ghost.csv"}}, // no session, either spelling
	}))
	if !strings.Contains(res.Stderr, "ghost.csv") {
		t.Fatalf("stderr = %q, want it to name the input it could not provide — a run that "+
			"silently cannot see its own input reads as a bug in the model's code", res.Stderr)
	}
}

// TestNestedArtifactsAreListed. Artifacts were COLLECTED recursively (`find`) and
// LISTED top-level only (`ls -1A`), so a run that wrote out/plot.png reported it in
// the reply and then omitted it from the session listing — and the client's
// `name.startsWith(session/id)` found nothing and read the file as expired. Two
// traversals of one directory is two answers about what a session holds.
func TestNestedArtifactsAreListed(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) {
		return "", "", 0, map[string][]byte{"out/plot.png": []byte("\x89PNG"), "top.csv": []byte("a,b")}
	}
	app := mount(t)
	res := decode[CodeResult](t, post(t, app, Path, CodeRun{Lang: "py", Code: "savefig"}))

	var reported []string
	for _, f := range res.Files {
		reported = append(reported, f.ID)
	}
	if len(reported) != 2 {
		t.Fatalf("the run reported %v, want both the nested and the top-level artifact", reported)
	}

	resp := call(t, app, http.MethodGet, Path+"/files/"+res.SessionID, "", nil)
	raw, _ := io.ReadAll(resp.Body)
	var rows []listing
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	// EVERY id the run reported must be findable by the identifier the client
	// downloads with, or that artifact reads as expired.
	for _, id := range reported {
		want := res.SessionID + "/" + id
		found := false
		for _, r := range rows {
			if r.Name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the listing %+v has no row named %q — the run reported that artifact, so "+
				"the client will ask for it and be told it is gone", rows, want)
		}
	}
}

func sortedKeys(m map[string][]byte) []string { return slices.Sorted(maps.Keys(m)) }
