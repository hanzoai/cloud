package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

const tabsURL = "https://t.example.dev/abc"

// machineTerminal reads the terminal a machine lists as, through the route a
// caller lists machines by.
func machineTerminal(t *testing.T, list func() []byte, id string) (string, bool) {
	t.Helper()
	var out struct {
		Targets []map[string]any `json:"targets"`
	}
	if err := json.Unmarshal(list(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, m := range out.Targets {
		if m["id"] == id {
			s, _ := m["terminal"].(string)
			return s, true
		}
	}
	return "", false
}

func TestLiveTerminal(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	for _, c := range []struct {
		name string
		t    Target
		want string
	}{
		{"fresh terminal on an online machine", Target{Status: TargetOnline, MetricsAt: ago(10 * time.Second), Terminal: tabsURL, TerminalAt: ago(10 * time.Second)}, tabsURL},
		{"terminal older than the window, machine beat just now", Target{Status: TargetOnline, MetricsAt: ago(5 * time.Second), Terminal: tabsURL, TerminalAt: ago(LiveWindow + time.Second)}, ""},
		{"offline by the heartbeat", Target{Status: TargetOnline, MetricsAt: ago(LiveWindow + time.Second), Terminal: tabsURL, TerminalAt: ago(5 * time.Second)}, ""},
		{"offline by intent", Target{Status: TargetOffline, MetricsAt: ago(5 * time.Second), Terminal: tabsURL, TerminalAt: ago(5 * time.Second)}, ""},
		{"empty terminal", Target{Status: TargetOnline, MetricsAt: ago(5 * time.Second), TerminalAt: ago(5 * time.Second)}, ""},
		{"never stamped", Target{Status: TargetOnline, MetricsAt: ago(5 * time.Second), Terminal: tabsURL}, ""},
	} {
		if got := c.t.LiveTerminal(now); got != c.want {
			t.Errorf("%s: LiveTerminal = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTargetTerminalRoundTrips(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testSessionStore(t)
		ctx := context.Background()
		x := Target{ID: "t1", Org: "acme", Label: "mac", Kind: TargetLaptop, Status: TargetOnline,
			Terminal: tabsURL, TerminalAt: 1234, CreatedAt: 1, UpdatedAt: 1}
		if err := s.CreateTarget(ctx, x); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := s.GetTarget(ctx, "acme", "t1")
		if err != nil || got.Terminal != tabsURL || got.TerminalAt != 1234 {
			t.Fatalf("create did not round-trip: %+v %v", got, err)
		}
		got.Terminal, got.TerminalAt = "", 0
		if err := s.UpdateTarget(ctx, got); err != nil {
			t.Fatalf("update: %v", err)
		}
		if got, _ = s.GetTarget(ctx, "acme", "t1"); got.Terminal != "" || got.TerminalAt != 0 {
			t.Fatalf("update did not clear: %+v", got)
		}
	})
}

func TestHTTPMachineTerminal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		listAs := func(user string) []byte {
			_, b := reqAs(t, app, http.MethodGet, "/v1/agent/targets", "acme", user, false, "", nil)
			return b
		}
		terminalFor := func(user, id string) string {
			s, ok := machineTerminal(t, func() []byte { return listAs(user) }, id)
			if !ok {
				t.Fatalf("%s does not list %s", user, id)
			}
			return s
		}
		patch := func(id string, body map[string]any) int {
			code, _ := reqAs(t, app, http.MethodPatch, "/v1/agent/targets/"+id, "acme", "ada", false, "", body)
			return code
		}

		// Register with a terminal: the owner lists it, a colleague does not.
		code, b := reqAs(t, app, http.MethodPost, "/v1/agent/targets", "acme", "ada", false, "",
			map[string]any{"label": "mac", "host": "mac", "metrics": map[string]any{"load1": 0.5}, "terminal": tabsURL})
		var made struct {
			ID       string `json:"id"`
			Terminal string `json:"terminal"`
		}
		if code != http.StatusCreated || json.Unmarshal(b, &made) != nil || made.ID == "" {
			t.Fatalf("register want 201, got %d (%s)", code, b)
		}
		if made.Terminal != tabsURL {
			t.Fatalf("register answered terminal %q, want %q", made.Terminal, tabsURL)
		}
		if got := terminalFor("ada", made.ID); got != tabsURL {
			t.Fatalf("owner lists terminal %q, want %q", got, tabsURL)
		}
		if got := terminalFor("grace", made.ID); got != "" {
			t.Fatalf("a colleague sees the terminal %q", got)
		}

		// A beat that states no terminal leaves it.
		if code := patch(made.ID, map[string]any{"metrics": map[string]any{"load1": 0.7}}); code != http.StatusOK {
			t.Fatalf("beat want 200, got %d", code)
		}
		if got := terminalFor("ada", made.ID); got != tabsURL {
			t.Fatalf("a beat with no terminal wiped it: %q", got)
		}

		// A non-https terminal is refused as the session field refuses it, on both writes.
		if code := patch(made.ID, map[string]any{"terminal": "http://x.example/t"}); code != http.StatusBadRequest {
			t.Fatalf("http terminal on patch want 400, got %d", code)
		}
		if code, _ := reqAs(t, app, http.MethodPost, "/v1/agent/targets", "acme", "ada", false, "",
			map[string]any{"label": "x", "terminal": "javascript:alert(1)"}); code != http.StatusBadRequest {
			t.Fatalf("javascript terminal on register want 400, got %d", code)
		}
		if got := terminalFor("ada", made.ID); got != tabsURL {
			t.Fatalf("a refused terminal changed the stored one: %q", got)
		}

		// "" clears it: the link is ending.
		if code := patch(made.ID, map[string]any{"terminal": ""}); code != http.StatusOK {
			t.Fatalf("clear want 200, got %d", code)
		}
		if got := terminalFor("ada", made.ID); got != "" {
			t.Fatalf("an empty terminal did not clear: %q", got)
		}
	})
}

// TestAReapedLinkSessionLeavesTheMachineItsTerminal is the production report of
// 2026-11-26: hanzo link registers a session that never appends an event, the
// reaper ends it after an hour, and the machine's terminal vanished with it. The
// terminal is the machine's now, so the reap changes nothing about it.
func TestAReapedLinkSessionLeavesTheMachineItsTerminal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		code, b := do(t, app, http.MethodPost, "/v1/agent/targets", "acme",
			map[string]any{"label": "mac", "host": "mac", "metrics": map[string]any{"load1": 0.5}, "terminal": tabsURL})
		var made struct {
			ID string `json:"id"`
		}
		if code != http.StatusCreated || json.Unmarshal(b, &made) != nil {
			t.Fatalf("register want 201, got %d (%s)", code, b)
		}

		// The link's session: agent tmux, a host, no target, an hour and more old.
		sto := storeOf(t, &mounted.State, "acme")
		at := time.Now().Add(-3 * time.Hour).Unix()
		link := Session{ID: "sess_link", Org: "acme", Agent: "tmux", Actor: "acme/u", Status: StatusRunning,
			Host: "mac", Terminal: tabsURL, RootID: "sess_link", StartedAt: at, CreatedAt: at, UpdatedAt: at, Payer: "acme", MeteredAt: at}
		if err := sto.CreateSession(context.Background(), link); err != nil {
			t.Fatalf("plant link session: %v", err)
		}

		reap(context.Background(), &mounted.State, &record{}, testClocks)
		if x := reload(t, sto, "sess_link"); x.EndedAt == 0 || !isTerminalStatus(x.Status) {
			t.Fatalf("the reaper left the link session %q live; the premise of this test is gone", x.Status)
		}

		_, body := do(t, app, http.MethodGet, "/v1/agent/targets", "acme", nil)
		if got, ok := machineTerminal(t, func() []byte { return body }, made.ID); !ok || got != tabsURL {
			t.Fatalf("after the session was reaped the machine lists terminal %q (listed: %v), want %q", got, ok, tabsURL)
		}
	})
}
