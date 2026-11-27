package manifest

import (
	"strconv"
	"testing"
)

// turnPath is every app a Slack turn or a coding run waits on, with where the
// wait comes from. An unpinned app here costs the person asking a cold start,
// and a plane-only one (the host never sees its calls) is stopped mid-use
// besides — see App.Turn.
var turnPath = []struct{ app, why string }{
	{"provider", "Slack's webhook lands here (slack_events.go); every turn resolves its asker and sends its reply through it (ingress.go)"},
	{"transport", "transport_ingest runs the turn (apps/transport/ingest.go, turn.go)"},
	{"agent", "agents_turn_start/events run the model and tool loop; the coding engine runs in this process (plugin/agent)"},
	{"ai", "every completion the loop makes (ai_socket.go: the host's loopback /v1)"},
	{"iam", "the key on each completion resolves here (auth_apikey.go)"},
	{"commerce", "the balance gate before every completion (resource_billing.go, apps/agents/budget.go)"},
	{"allowance", "ai's free-lane read and count on every completion (apps/ai/ai.go)"},
	{"flag", "allowance's and ai's ceilings (apps/allowance/allowance.go limits, apps/ai/cap.go)"},
	{"kms", "the org's Slack bot token and the user link (apps/provider TokenFor, channel.go)"},
	{"websearch", "the web search tool"},
	{"crawl", "the page read tool"},
	{"sandbox", "a coding run leases, runs in and ends its machine here (apps/coding/sandboxrunner.go)"},
	{"environment", "a sandbox coding run reads its codebase's scripts and secrets here before its agent starts (apps/coding/environment.go)"},
	{"settings", "a sandbox lease reads its runtime (apps/sandbox/fleet.go)"},
	{"task", "a coding run files and moves its issue (apps/coding/adapters.go)"},
	{"tool", "a coding run reads its org's active skills and MCP servers here before its harness starts (apps/coding/kit.go)"},
	{"o11y", "every span the turn emits"},
}

// Production states no reservation and is sized at the default, and there every
// app a turn waits on is pinned.
func TestTheTurnPathIsPinned(t *testing.T) {
	t.Setenv(memoryRequestEnv, "")
	byName := map[string]App{}
	for _, a := range Apps {
		byName[a.Name] = a
	}
	for _, c := range turnPath {
		a, ok := byName[c.app]
		switch {
		case !ok:
			t.Errorf("%s is on the turn path (%s) and has no row", c.app, c.why)
		case a.Coresident:
			t.Errorf("%s is on the turn path but co-resident: the host starts nothing for it", c.app)
		case !a.Eager && !a.Turn:
			t.Errorf("%s is on the turn path (%s) and neither Eager nor Turn: every turn after it was stopped pays its cold start", c.app, c.why)
		case !a.Pinned():
			t.Errorf("%s is on the turn path (%s) and not pinned at the default reservation", c.app, c.why)
		}
	}
}

// count is how many children the fleet holds for pred.
func count(pred func(App) bool) int {
	n := 0
	for _, a := range Apps {
		if !a.Coresident && pred(a) {
			n++
		}
	}
	return n
}

// A pinned app is never evicted, yet it counts against Warm, so the lazy apps
// share whatever it leaves. At or past Warm, every lazy start evicts every other
// lazy child. The default reservation is what production runs on.
func TestPinsLeaveRoomUnderWarm(t *testing.T) {
	t.Setenv(memoryRequestEnv, "")
	pinned, warm := count(App.Pinned), Warm()
	if pinned >= warm {
		t.Fatalf("%d pinned against a ceiling of %d: no lazy app can start without evicting another", pinned, warm)
	}
	t.Logf("%d pinned, ceiling %d: %d processes left for lazy apps", pinned, warm, warm-pinned)
}

// The reservation `make dev` states (e2e/run.sh, BOOT_ONLY) starts only the Eager
// and Vital apps with the host, and its ceiling holds them and a whole turn at once.
func TestALaptopStartsTheTurnPathOnFirstUse(t *testing.T) {
	const laptop = 4720 // 4 GiB held 23; workflow's cron pinned with the turn path makes 24, admin's trail 25, notify 26, auto's clock 27
	t.Setenv(memoryRequestEnv, strconv.Itoa(laptop))
	if TurnPinned() {
		t.Fatalf("a %d MiB reservation pins the turn path", laptop)
	}
	for _, a := range Apps {
		if a.Pinned() != (a.Eager || a.Vital) {
			t.Errorf("%s: Pinned=%v at %d MiB, Eager=%v, Vital=%v", a.Name, a.Pinned(), laptop, a.Eager, a.Vital)
		}
		if p := a.Plugin(); p.Lazy == a.Pinned() {
			t.Errorf("%s resolves Lazy=%v while Pinned=%v", a.Name, p.Lazy, a.Pinned())
		}
	}
	held := count(func(a App) bool { return a.Eager || a.Turn })
	if warm := Warm(); held > warm {
		t.Fatalf("%d Eager and Turn apps against a ceiling of %d at %d MiB: a turn evicts what it is using", held, warm, laptop)
	}
}

// A coding run parked for a refusal is woken by a timer, and by credits added, in the
// agent process (apps/coding park.go): the process starts with the host and is never stopped
// for idling, at any reservation a deployment states, or the wake is lost with it.
//
// MUTATION: make agent Turn alone and a laptop's host stops it fifteen minutes after its last
// call, and a run parked for the night is not woken.
func TestAParkedRunsWakeLivesInAProcessThatStaysUp(t *testing.T) {
	for _, mib := range []string{"", "512", "4720", "6144"} {
		t.Setenv(memoryRequestEnv, mib)
		for _, a := range Apps {
			if a.Name != "agent" {
				continue
			}
			if !a.Eager || !a.Pinned() || a.Plugin().Lazy {
				t.Fatalf("agent at %q MiB: Eager=%v Pinned=%v Lazy=%v: a parked run's timer would die with an idle process", mib, a.Eager, a.Pinned(), a.Plugin().Lazy)
			}
		}
	}
}

// /readyz answers 503 for a Vital app the boot could not start, so a Vital app
// the boot does not start leaves the gate checking nothing: a rollout whose ai
// cannot start would go Ready and answer 200 until a request reached /v1.
func TestAVitalAppStartsWithTheHostAtAnyReservation(t *testing.T) {
	for _, mib := range []string{"", "512", "4096", "6144"} {
		t.Setenv(memoryRequestEnv, mib)
		for _, a := range Apps {
			if a.Vital && !a.Pinned() {
				t.Errorf("%s is Vital and not pinned at %q MiB", a.Name, mib)
			}
		}
	}
}

// admin holds the one audit trail every process appends to, so a write anywhere
// waits on it: it starts with the host and is never stopped for idling.
func TestTheAuditTrailStartsWithTheHost(t *testing.T) {
	for _, mib := range []string{"", "512", "4720"} {
		t.Setenv(memoryRequestEnv, mib)
		for _, a := range Apps {
			if a.Name != "admin" {
				continue
			}
			if !a.Eager || !a.Pinned() || a.Plugin().Lazy {
				t.Fatalf("admin at %q MiB: Eager=%v Pinned=%v Lazy=%v — a write anywhere would wait on a cold or evicted trail", mib, a.Eager, a.Pinned(), a.Plugin().Lazy)
			}
		}
	}
}

// A sign-in code is delivered by notify, and IAM wakes notify once, at boot. A
// notify that starts on its first request makes the first send after a roll fail.
func TestNotifyStartsWithTheHost(t *testing.T) {
	for _, a := range Apps {
		if a.Name != "notify" {
			continue
		}
		if !a.Eager || a.Plugin().Lazy {
			t.Fatalf("notify resolves Lazy=%v, Eager=%v: the first sign-in code after a boot is refused", a.Plugin().Lazy, a.Eager)
		}
		return
	}
	t.Fatal("notify has no row")
}
