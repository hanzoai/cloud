package host

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
	"fmt"
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

// transcriptApp is the audio floor in front of a transcript endpoint.
func transcriptApp(t *testing.T) *zip.App {
	t.Helper()
	s := &transcripts{opener: map[string]string{}}
	prev := admittedOf
	SetAdmitted(s.admitted)
	t.Cleanup(func() { admittedOf = prev })

	app := zip.New(zip.Config{})
	app.Use(zip.Handler(newScopeRateLimiter(nil, nil).handler))
	app.Raw(http.MethodPost, transcriptRoute, func(c *zip.Ctx) error {
		s.mu.Lock()
		s.n++
		id := fmt.Sprintf("ats_test_%d", s.n)
		// Cloned: a header is a view into fasthttp's request buffer, which the next
		// request reuses, so a kept one would come to read as somebody else's.
		s.opener[id] = strings.Clone(c.Header("Authorization"))
		s.mu.Unlock()
		return c.JSON(http.StatusCreated, map[string]any{"id": id, "chunk_ms": 256})
	})
	held := func(c *zip.Ctx) (string, bool) {
		id := c.Param("id")
		s.mu.Lock()
		defer s.mu.Unlock()
		return id, s.opener[id] == c.Header("Authorization")
	}
	app.Raw(http.MethodPost, transcriptRoute+"/:id", func(c *zip.Ctx) error {
		if _, ok := held(c); !ok {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "no transcript"})
		}
		return c.JSON(http.StatusOK, map[string]any{"text": "", "seconds": 0})
	})
	app.Raw(http.MethodDelete, transcriptRoute+"/:id", func(c *zip.Ctx) error {
		id, ok := held(c)
		if !ok {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "no transcript"})
		}
		s.mu.Lock()
		delete(s.opener, id)
		s.mu.Unlock()
		return c.JSON(http.StatusOK, map[string]any{"text": "done"})
	})
	return app
}

// speaker is one principal: the org the gateway minted, the user, and the bearer
// the transcript handler resolves.
type speaker struct{ org, user, bearer string }

var (
	alice = speaker{"acme", "alice", "Bearer alice-token"}
	bob   = speaker{"acme", "bob", "Bearer bob-token"}
	eve   = speaker{"other", "eve", "Bearer eve-token"}
)

// TestALiveTranscriptIsOneRequest is the outage: 300 pushes — 75 s of dictation
// at 250 ms, five times the audio floor, inside one minute window that never
// resets to rescue them — all 200, and the close after them.
func TestALiveTranscriptIsOneRequest(t *testing.T) {
	app := transcriptApp(t)
	id := alice.open(t, app)
	for i := range 300 {
		if code := alice.call(t, app, http.MethodPost, transcriptRoute+"/"+id); code != http.StatusOK {
			t.Fatalf("push %d of 300 answered %d — a live transcript is spending the request ceiling", i+1, code)
		}
	}
	if code := alice.call(t, app, http.MethodDelete, transcriptRoute+"/"+id); code != http.StatusOK {
		t.Fatalf("the close answered %d, want 200", code)
	}
}

// TestSomeoneElsesSessionIsCounted. A push to an id another principal opened —
// a colleague in the same org, or another org — is refused by the session's
// handler AND counted, so the exemption cannot be borrowed by naming a session.
// The same holds for a session that has been closed.
func TestSomeoneElsesSessionIsCounted(t *testing.T) {
	rpm := audioRPM(t)
	for _, tc := range []struct {
		name     string
		stranger speaker
		spent    int // tokens already spent in the stranger's org bucket by the open
	}{
		{"a colleague in the same org", bob, 1},
		{"another org", eve, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := transcriptApp(t)
			id := alice.open(t, app)
			for i := range rpm - tc.spent {
				if code := tc.stranger.call(t, app, http.MethodPost, transcriptRoute+"/"+id); code != http.StatusNotFound {
					t.Fatalf("push %d to a session %s did not open answered %d, want 404", i+1, tc.stranger.user, code)
				}
			}
			if code := tc.stranger.call(t, app, http.MethodPost, transcriptRoute+"/"+id); code != http.StatusTooManyRequests {
				t.Fatalf("after %d pushes to alice's session, %s answered %d, want 429 — the exemption was borrowed",
					rpm-tc.spent, tc.stranger.user, code)
			}
			// The owner is not throttled by the stranger's spending: her pushes
			// were never in the bucket.
			if code := alice.call(t, app, http.MethodPost, transcriptRoute+"/"+id); code != http.StatusOK {
				t.Fatalf("alice's own push answered %d after a stranger spent the bucket, want 200", code)
			}
		})
	}

	t.Run("a closed session", func(t *testing.T) {
		app := transcriptApp(t)
		id := alice.open(t, app)
		if code := alice.call(t, app, http.MethodDelete, transcriptRoute+"/"+id); code != http.StatusOK {
			t.Fatalf("close answered %d", code)
		}
		for i := range rpm - 1 {
			if code := alice.call(t, app, http.MethodPost, transcriptRoute+"/"+id); code != http.StatusNotFound {
				t.Fatalf("push %d to a closed session answered %d, want 404", i+1, code)
			}
		}
		if code := alice.call(t, app, http.MethodPost, transcriptRoute+"/"+id); code != http.StatusTooManyRequests {
			t.Fatalf("pushes to a closed session are not counted: answered %d, want 429", code)
		}
	})
}

// TestAFreshOpenIsStillLimited. The open is the request a transcript is counted
// as, so opens are held to the floor — and a session already admitted keeps
// pushing when the opens are spent.
func TestAFreshOpenIsStillLimited(t *testing.T) {
	rpm := audioRPM(t)
	app := transcriptApp(t)
	first := alice.open(t, app)
	for range rpm - 1 {
		alice.open(t, app)
	}
	if code := alice.call(t, app, http.MethodPost, transcriptRoute); code != http.StatusTooManyRequests {
		t.Fatalf("open %d answered %d, want 429 — opening is no longer limited", rpm+1, code)
	}
	if code := alice.call(t, app, http.MethodPost, transcriptRoute+"/"+first); code != http.StatusOK {
		t.Fatalf("a push to an admitted session answered %d once the opens were spent, want 200", code)
	}
	// And the batch endpoint the streaming one is named after is counted as it was.
	if code := alice.call(t, app, http.MethodPost, "/v1/audio/transcriptions"); code != http.StatusTooManyRequests {
		t.Fatalf("/v1/audio/transcriptions answered %d with the floor spent, want 429", code)
	}
}

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
