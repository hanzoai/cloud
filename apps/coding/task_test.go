package coding

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These pin coding's wire contract with the runtime through the REAL transport
// (apps/bots' transport) against a stub server — so the credential-custody and
// fail-closed properties are proven end to end over the seam, not against a fake
// of it.

// ndjsonServer streams the given lines as application/x-ndjson and records the
// request it received, so a test can assert the wire contract (path, headers,
// body) AND that the credential travels only in the body.
func ndjsonServer(t *testing.T, lines []string, capture *http.Request, capBody *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			*capture = *r
		}
		if capBody != nil {
			b, _ := io.ReadAll(r.Body)
			*capBody = b
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		for _, ln := range lines {
			_, _ = io.WriteString(w, ln+"\n")
		}
	}))
}

func TestTask_StreamsStepsAndResult(t *testing.T) {
	lines := []string{
		`{"type":"step","step":"clone","status":"ok"}`,
		`{"type":"log","message":"editing handler.go"}`,
		`{"type":"step","step":"push","status":"ok"}`,
		`{"type":"result","branch":"agent/x","commitSha":"deadbeef","diffstat":"1 file changed","changed":true,"ok":true,"logTail":"done"}`,
	}
	var gotReq http.Request
	var gotBody []byte
	srv := ndjsonServer(t, lines, &gotReq, &gotBody)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	t.Setenv("BOT_GATEWAY_ALLOW_PLAINTEXT", "1") // httptest is http://; assert-plaintext guard tested separately
	t.Setenv("BOT_GATEWAY_TOKEN", "svc-token-xyz")

	var steps []Step
	res, err := runner{}.Run(context.Background(), "acme", "u-1", RunRequest{
		CloneURL: "https://git.test/v1/git/acme/api.git", Branch: "agent/x", Prompt: "fix",
		CredUser: "x-access-token", CredToken: "sk-SECRETtoken",
	}, func(s Step) { steps = append(steps, s) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Result parsed.
	if !res.OK || !res.Changed || res.Branch != "agent/x" || res.CommitSha != "deadbeef" {
		t.Fatalf("result wrong: %+v", res)
	}
	// Steps mirrored (2 steps + 1 log; result/terminal not delivered as a step).
	if len(steps) != 3 {
		t.Fatalf("want 3 progress steps, got %d: %+v", len(steps), steps)
	}
	if steps[0].Type != "step" || steps[0].Step != "clone" || steps[1].Type != "log" {
		t.Fatalf("step vocabulary wrong: %+v", steps)
	}

	// Wire contract: POST /v1/coding-tasks, identity + bearer headers set.
	if gotReq.Method != http.MethodPost || !strings.HasSuffix(gotReq.URL.Path, "/v1/coding-tasks") {
		t.Fatalf("bad request line: %s %s", gotReq.Method, gotReq.URL.Path)
	}
	if gotReq.Header.Get("X-Org-Id") != "acme" || gotReq.Header.Get("X-User-Id") != "u-1" {
		t.Fatalf("identity headers wrong: %v", gotReq.Header)
	}
	if gotReq.Header.Get("Authorization") != "Bearer svc-token-xyz" {
		t.Fatalf("service bearer missing/wrong: %q", gotReq.Header.Get("Authorization"))
	}
	// The credential travels in the BODY only (never a URL/header).
	if strings.Contains(gotReq.URL.RawQuery, "SECRETtoken") || strings.Contains(gotReq.Header.Get("Authorization"), "SECRETtoken") {
		t.Fatal("credential leaked onto URL/header")
	}
	var body taskRequest
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body decode: %v", err)
	}
	if body.Credential.Token != "sk-SECRETtoken" || body.Credential.Username != "x-access-token" {
		t.Fatalf("credential not carried in body: %+v", body.Credential)
	}
}

func TestTask_ErrorLine(t *testing.T) {
	srv := ndjsonServer(t, []string{`{"type":"error","message":"dev exec failed","logTail":"boom"}`}, nil, nil)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	t.Setenv("BOT_GATEWAY_ALLOW_PLAINTEXT", "1") // httptest is http://; assert-plaintext guard tested separately

	res, err := runner{}.Run(context.Background(), "acme", "u", RunRequest{}, nil)
	if err != nil {
		t.Fatalf("a clean error line is a terminal result, not a transport error: %v", err)
	}
	if res.OK || res.Error != "dev exec failed" || res.LogTail != "boom" {
		t.Fatalf("error result wrong: %+v", res)
	}
}

func TestTask_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	t.Setenv("BOT_GATEWAY_ALLOW_PLAINTEXT", "1") // httptest is http://; assert-plaintext guard tested separately
	if _, err := (runner{}).Run(context.Background(), "acme", "u", RunRequest{}, nil); err == nil {
		t.Fatal("a 401 must be an error")
	}
}

func TestTask_NoTerminalIsError(t *testing.T) {
	srv := ndjsonServer(t, []string{`{"type":"step","step":"clone"}`}, nil, nil)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	t.Setenv("BOT_GATEWAY_ALLOW_PLAINTEXT", "1") // httptest is http://; assert-plaintext guard tested separately
	if _, err := (runner{}).Run(context.Background(), "acme", "u", RunRequest{}, nil); err == nil {
		t.Fatal("a stream with no result/error line must be an error (no fabricated success)")
	}
}

func TestTask_RefusesCleartextByDefault(t *testing.T) {
	// The credential-bearing POST must not go cleartext without an explicit mesh
	// opt-in: an http target with BOT_GATEWAY_ALLOW_PLAINTEXT unset fails closed and
	// never dials.
	srv := ndjsonServer(t, []string{`{"type":"result","ok":true}`}, nil, nil)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL) // http://
	t.Setenv("BOT_GATEWAY_ALLOW_PLAINTEXT", "")
	// The repo is what makes this POST credential-bearing: with no CloneURL the
	// credential never reaches the wire, so there would be no secret for the
	// cleartext guard to protect and nothing for this test to prove.
	_, err := runner{}.Run(context.Background(), "acme", "u", RunRequest{
		CloneURL: "https://git.hanzo.ai/v1/git/acme/api.git", Branch: "agent/abc123",
		CredUser: "x", CredToken: "sk-SECRET",
	}, nil)
	if err == nil {
		t.Fatal("cleartext coding POST must fail closed by default")
	}
	if strings.Contains(err.Error(), "sk-SECRET") {
		t.Fatalf("error must not leak the credential: %v", err)
	}
}

// A run with NO repo carries no credential, so it is not a "secret" call and the
// cleartext guard must not refuse it — otherwise every research and bare-exec run
// is blocked by a rule written to protect a git token that is not there.
func TestTask_NoRepoRunIsNotSecret(t *testing.T) {
	var raw []byte
	srv := ndjsonServer(t, []string{`{"type":"result","ok":true}`}, nil, &raw)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL) // http://
	t.Setenv("BOT_GATEWAY_ALLOW_PLAINTEXT", "")
	res, err := runner{}.Run(context.Background(), "acme", "u", RunRequest{
		Prompt: "read the docs and summarise", Tool: "python", Desktop: true,
	}, nil)
	if err != nil {
		t.Fatalf("a repo-less run must not be refused as cleartext-secret: %v", err)
	}
	if !res.OK {
		t.Fatal("expected the terminal result to carry through")
	}
	// The wire says what the run is, and says nothing about git.
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("body was not JSON: %v", err)
	}
	if got["tool"] != "python" || got["desktop"] != true {
		t.Fatalf("tool/desktop did not reach the runtime: %#v", got)
	}
	for _, k := range []string{"credential", "cloneUrl", "branch", "baseBranch"} {
		if _, ok := got[k]; ok {
			t.Fatalf("a repo-less run must not put %q on the wire: %#v", k, got)
		}
	}
}

// A credential with no repo is a caller bug, and it is refused at the door rather
// than trimmed in silence.
func TestTask_RefusesCredentialWithoutRepo(t *testing.T) {
	srv := ndjsonServer(t, []string{`{"type":"result","ok":true}`}, nil, nil)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", strings.Replace(srv.URL, "http://", "https://", 1))
	_, err := runner{}.Run(context.Background(), "acme", "u", RunRequest{
		Prompt: "x", CredUser: "x", CredToken: "sk-SECRET",
	}, nil)
	if err == nil {
		t.Fatal("a credential with no repo must be refused")
	}
	if strings.Contains(err.Error(), "sk-SECRET") {
		t.Fatalf("error must not leak the credential: %v", err)
	}
}
