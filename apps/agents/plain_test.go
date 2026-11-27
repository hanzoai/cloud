package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// A run's log line is a terminal's output. What it SAID is kept; the colour and
// the control characters are not, so every read of the session is JSON a strict
// reader takes — including a row an earlier release stored raw.
func TestASessionWithTerminalOutputReadsAsJSON(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		root := register(t, app, "acme", map[string]any{"agent": "dev"})
		line, _ := json.Marshal(map[string]any{"step": "dev", "message": "\x1b[1;31merror\x1b[0m: 3 tests\r\nfailed\x07"})
		if err := LogSessionEvent(context.Background(), "acme", root.ID, KindLog, "", line); err != nil {
			t.Fatal(err)
		}
		sto := storeOf(t, &mounted.State, "acme")
		if _, err := sto.db.Exec(`INSERT INTO agent_session_events (`+eventCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			"evt_raw", root.ID, "acme", 99, KindLog, "acme", "{\"message\":\"raw \x1b[32mok\"}", time.Now().Unix()); err != nil {
			t.Fatal(err)
		}

		code, b := do(t, app, http.MethodGet, "/v1/agent/sessions/"+root.ID, "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("detail: %d %s", code, b)
		}
		if !json.Valid(b) {
			t.Fatalf("the session is not JSON: %q", b)
		}
		for _, c := range b {
			if c < 0x20 {
				t.Fatalf("a raw control character reached the reader: %q", b)
			}
		}
		var det sessionDetail
		mustJSON(t, b, &det)
		var got struct{ Message string }
		mustJSON(t, det.RecentEvents[0].Payload, &got)
		if got.Message != "error: 3 tests\nfailed" {
			t.Fatalf("the line reads %q; the text must survive and the escapes must not", got.Message)
		}
		if s := string(det.RecentEvents[1].Payload); !strings.Contains(s, "raw ok") {
			t.Fatalf("the raw row reads %s", s)
		}

	})
}
