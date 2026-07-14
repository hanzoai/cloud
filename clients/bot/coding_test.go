package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestRunCodingTask_StreamsStepsAndResult(t *testing.T) {
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
	t.Setenv("BOT_GATEWAY_TOKEN", "svc-token-xyz")

	var steps []CodingStep
	res, err := RunCodingTask(context.Background(), "acme", "u-1", CodingTaskRequest{
		CloneURL: "https://git.test/v1/git/acme/api.git", Branch: "agent/x", Prompt: "fix",
		Credential: Credential{Username: "x-access-token", Token: "hk-SECRETtoken"},
	}, func(s CodingStep) { steps = append(steps, s) })
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
	var body CodingTaskRequest
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body decode: %v", err)
	}
	if body.Credential.Token != "hk-SECRETtoken" || body.Credential.Username != "x-access-token" {
		t.Fatalf("credential not carried in body: %+v", body.Credential)
	}
}

func TestRunCodingTask_ErrorLine(t *testing.T) {
	srv := ndjsonServer(t, []string{`{"type":"error","message":"dev exec failed","logTail":"boom"}`}, nil, nil)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL)

	res, err := RunCodingTask(context.Background(), "acme", "u", CodingTaskRequest{}, nil)
	if err != nil {
		t.Fatalf("a clean error line is a terminal result, not a transport error: %v", err)
	}
	if res.OK || res.Error != "dev exec failed" || res.LogTail != "boom" {
		t.Fatalf("error result wrong: %+v", res)
	}
}

func TestRunCodingTask_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	if _, err := RunCodingTask(context.Background(), "acme", "u", CodingTaskRequest{}, nil); err == nil {
		t.Fatal("a 401 must be an error")
	}
}

func TestRunCodingTask_NoTerminalIsError(t *testing.T) {
	srv := ndjsonServer(t, []string{`{"type":"step","step":"clone"}`}, nil, nil)
	defer srv.Close()
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	if _, err := RunCodingTask(context.Background(), "acme", "u", CodingTaskRequest{}, nil); err == nil {
		t.Fatal("a stream with no result/error line must be an error (no fabricated success)")
	}
}
