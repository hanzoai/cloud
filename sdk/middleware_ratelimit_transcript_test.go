package cloud

// A live transcript is one request.
//
// Dictation opens POST /v1/audio/transcript, then pushes pcm16 to
// /v1/audio/transcript/:id every 256 ms — about four requests a second. The audio
// floor is sixty a minute, and it counted every push: on api.hanzo.ai pushes 56–73
// of a 90-push probe answered 429, and the client read that as the service being
// gone. The open is counted; the pushes and the close are bounded by the session
// itself, and only a session the caller's own credential opened earns that.
//
// The session table here stands in for hanzoai/ai's (controllers.TranscriptAdmitted,
// whose ownership and idle rules have their own tests there). What is under test is
// the ceiling's half: what it counts, and that the exemption is the session
// owner's to grant.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// transcripts is the session table: which credential opened each live id.
type transcripts struct {
	mu     sync.Mutex
	opener map[string]string
	n      int
}

const transcriptRoute = "/v1/audio/transcript"

// admitted is the answer controllers.TranscriptAdmitted gives: a push or close on
// an id the same credential opened.
func (s *transcripts) admitted(method, path, auth string) bool {
	tid, ok := strings.CutPrefix(path, transcriptRoute+"/")
	if !ok || tid == "" || (method != http.MethodPost && method != http.MethodDelete) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return auth != "" && s.opener[tid] == auth
}

// speaker is one principal: the org the gateway minted, the user, and the bearer
// the transcript handler resolves.
type speaker struct{ org, user, bearer string }

var (
	alice = speaker{"acme", "alice", "Bearer alice-token"}
	bob   = speaker{"acme", "bob", "Bearer bob-token"}
	eve   = speaker{"other", "eve", "Bearer eve-token"}
)

func (p speaker) call(t *testing.T, app *zip.App, method, path string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(strings.Repeat("\x00", 64)))
	req.Header.Set("X-Org-Id", p.org)
	req.Header.Set("X-User-Id", p.user)
	req.Header.Set("Authorization", p.bearer)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp.StatusCode
}

func (p speaker) open(t *testing.T, app *zip.App) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, transcriptRoute, strings.NewReader(`{"model":"whisper-1"}`))
	req.Header.Set("X-Org-Id", p.org)
	req.Header.Set("X-User-Id", p.user)
	req.Header.Set("Authorization", p.bearer)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open answered %d, want 201", resp.StatusCode)
	}
	var body struct{ ID string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.ID == "" {
		t.Fatalf("open named no session: %v", err)
	}
	return body.ID
}
