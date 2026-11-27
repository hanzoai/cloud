package leaderboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/zap-proto/zip"
)

// aHub is a hub on the mounted store, with the clock held at noon.
func aHub(t *testing.T) *hub {
	t.Helper()
	at(t)
	mountApp(t)
	return newHub(mountedStore, quiet{})
}

func lineOfTokens(m map[string]int64, points map[string]int) *line {
	l := &line{}
	for org, n := range m {
		e := entry{org: org, src: client.Sources{Tokens: n}}
		e.points = points[org]
		l.ranked = append(l.ranked, e)
	}
	return l
}

func kinds(ls []FeedLine) string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Kind+":"+l.Handle+":"+l.Detail)
	}
	return strings.Join(out, " | ")
}

func TestFeedNamesAnOrgTheWayTheStandingDoes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		seedUserOptin(t, "listed/ann", "listed", "Ann", true)
		seedUserOptin(t, "gone/gus", "gone", "Gus", false)
		for _, org := range []string{"valerypetrov", "tiny", "listed", "gone"} {
			h.pulse(bus.Pulse{Kind: "org", Key: org, Org: org, Title: "Org created", Detail: "Display Name <ann@example.com>"})
		}
		got := kinds(h.ring)
		if want := "joined:va•••ov: | joined:t•••: | joined:Ann: | joined::"; got != want {
			t.Fatalf("lines = %s, want %s", got, want)
		}
		body, _ := json.Marshal(h.ring)
		for _, leak := range []string{"valerypetrov", "gone", "Gus", "example.com", "Display Name", "@"} {
			if strings.Contains(string(body), leak) {
				t.Errorf("a line carries %q: %s", leak, body)
			}
		}
		for _, l := range h.ring {
			var keys map[string]any
			raw, _ := json.Marshal(l)
			_ = json.Unmarshal(raw, &keys)
			if len(keys) != 5 {
				t.Errorf("a line holds %v", keys)
			}
		}
	})
}

func TestFeedSaysEachKindOfPulse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		h.pulse(bus.Pulse{Kind: "org", Org: "alphabet"})
		h.pulse(bus.Pulse{Kind: "referral", Org: "alphabet"})
		h.pulse(bus.Pulse{Kind: "bounty", Org: "alphabet", Title: "bug_report", Detail: "50"})
		h.pulse(bus.Pulse{Kind: "expert", Org: "alphabet"})
		h.pulse(bus.Pulse{Kind: "paid", Org: "alphabet", Cents: 1250}) // money is not the board's
		h.pulse(bus.Pulse{Kind: "signup", Org: "alphabet"})            // the org's creation says it
		h.pulse(bus.Pulse{Kind: "model", Key: "openrouter/x"})
		if got, want := kinds(h.ring), "joined:al•••et: | referred:al•••et: | bounty:al•••et:+50 pts | expert:al•••et:"; got != want {
			t.Fatalf("lines = %s, want %s", got, want)
		}
	})
}

func TestUsageIsCoalescedIntoMilestonesAndLevels(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		// The first read is the baseline, and says nothing.
		h.observe(lineOfTokens(map[string]int64{"jumper": 500_000, "steady": 2_000_000, "climber": 0}, map[string]int{"climber": 50}), map[string]bool{}, noon)
		if len(h.ring) != 0 {
			t.Fatalf("the baseline said %s", kinds(h.ring))
		}
		// Then: one org passes 1M, 10M and 100M between two reads, and one passes two levels.
		h.observe(lineOfTokens(
			map[string]int64{"jumper": 250_000_000, "steady": 2_500_000, "climber": 0},
			map[string]int{"jumper": 16000, "climber": 4500}), map[string]bool{}, noon)
		got := kinds(h.ring)
		// jumper: one milestone, the highest (100M), and one level, the highest (Comet, at
		// 16000, though it passed 1000, 4000 and 9000). climber: one level (Focus) though
		// it passed 1000 and 4000. steady crossed nothing.
		first := "milestone:ju•••er:100M tokens | level:ju•••er:Comet | level:cl•••er:Focus"
		second := "milestone:ju•••er:100M tokens | level:cl•••er:Focus | level:ju•••er:Comet"
		if got != first && got != second {
			t.Fatalf("lines = %s", got)
		}
		if len(h.ring) != 3 {
			t.Fatalf("three orgs' changes are three lines, not one per request: %s", got)
		}
		// Nothing changed since: nothing is said.
		before := len(h.ring)
		h.observe(lineOfTokens(
			map[string]int64{"jumper": 250_000_000, "steady": 2_500_000, "climber": 0},
			map[string]int{"jumper": 16000, "climber": 4500}), map[string]bool{}, noon)
		if len(h.ring) != before {
			t.Fatalf("an unchanged read said %s", kinds(h.ring[before:]))
		}
	})
}

func TestAFirstTryIsSaidOnceAndABaseModelNever(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		l := lineOfTokens(map[string]int64{"newcomer": 1}, nil)
		h.observe(l, map[string]bool{"old\x00zen4": true}, noon)
		h.observe(l, map[string]bool{"old\x00zen4": true, "newcomer\x00enso-pro": true, "newcomer\x00qwen3-72b": true}, noon)
		h.observe(l, map[string]bool{"old\x00zen4": true, "newcomer\x00enso-pro": true, "newcomer\x00qwen3-72b": true}, noon)
		if got, want := kinds(h.ring), "tried:ne•••er:enso-pro"; got != want {
			t.Fatalf("lines = %s, want %s", got, want)
		}
	})
}

func TestTheHourIsSaidOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		hours := make([]string, 24)
		toks := make([]int64, 24)
		for i := range hours {
			hours[i] = noon.Truncate(time.Hour).Add(time.Duration(i-23) * time.Hour).Format(time.RFC3339)
		}
		toks[22], toks[23] = 1_300_000, 40
		p := &PulseView{Hours: hours, Tokens: &PulseSeries{Days: make([]int64, 30), Hours: toks}}
		h.hourly(p)
		h.hourly(p)
		if got, want := kinds(h.ring), "hour::1.3M tokens"; got != want {
			t.Fatalf("lines = %s, want %s", got, want)
		}
		if h.ring[0].At != "2026-11-08T13:00:00Z" {
			t.Fatalf("an hour is dated by its end: %s", h.ring[0].At)
		}
		// An idle hour is not a line.
		toks[22] = 0
		hours2 := append([]string{}, hours...)
		hours2[22] = noon.Truncate(time.Hour).Add(time.Hour).Format(time.RFC3339)
		h.hourly(&PulseView{Hours: hours2, Tokens: &PulseSeries{Days: make([]int64, 30), Hours: toks}})
		if len(h.ring) != 1 {
			t.Fatalf("lines = %s", kinds(h.ring))
		}
	})
}

func TestTheFeedIsCapped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		for i := range feedRate + 25 {
			h.pulse(bus.Pulse{Kind: "org", Org: fmt.Sprintf("builder%03d", i)})
		}
		if h.next != feedRate {
			t.Fatalf("%d lines left the hub in a minute, want %d", h.next, feedRate)
		}
		// A minute later it says more.
		later := noon.Add(61 * time.Second)
		nowFn = func() time.Time { return later }
		h.pulse(bus.Pulse{Kind: "org", Org: "latecomer"})
		if h.next != feedRate+1 {
			t.Fatalf("the cap did not lift: %d", h.next)
		}
	})
}

func TestANewReaderIsReplayedTheLastTwenty(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		for i := range 25 {
			h.pulse(bus.Pulse{Kind: "org", Org: fmt.Sprintf("builder%03d", i)})
		}
		replay, live, leave := h.subscribe()
		defer leave()
		if len(replay) != feedKeep || replay[0].ID != 6 || replay[feedKeep-1].ID != 25 {
			t.Fatalf("replay = %d lines, ids %d..%d", len(replay), replay[0].ID, replay[len(replay)-1].ID)
		}
		h.pulse(bus.Pulse{Kind: "referral", Org: "builder999"})
		select {
		case l := <-live:
			if l.Kind != kindReferred || l.ID != 26 {
				t.Fatalf("live line: %+v", l)
			}
		case <-time.After(time.Second):
			t.Fatal("a live line did not reach the reader")
		}
		// Written as a stream: the replay, then the line, each an event with its id.
		var buf bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			stream(ctx, bufio.NewWriter(&buf), replay[:2], make(chan FeedLine), 10*time.Millisecond)
			close(done)
		}()
		time.Sleep(60 * time.Millisecond)
		cancel()
		<-done
		out := buf.String()
		if !strings.HasPrefix(out, "retry: 5000\n\n") || !strings.Contains(out, "id: 6\ndata: {") || !strings.Contains(out, ": ping\n\n") {
			t.Fatalf("stream = %q", out)
		}
	})
}

func TestAnEmptyHubIsFilledFromWhatIsDurable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		world(t,
			[]iamOrg{{"oldest", "2026-01-01T00:00:00Z"}, {"middle", "2026-06-01T00:00:00Z"}, {"newest", "2026-10-01T00:00:00Z"}, {"valerypetrov", "2026-07-01T00:00:00Z"}},
			nil, nil, nil)
		// Twenty-five orgs IAM does not date must not crowd the dated ones out of the replay.
		was := directoryFn
		t.Cleanup(func() { directoryFn = was })
		directoryFn = func(ctx context.Context) (*client.Directory, error) {
			d, _ := was(ctx)
			for i := range 25 {
				d.Orgs = append(d.Orgs, client.DirectoryOrg{Name: fmt.Sprintf("undated%02d", i)})
			}
			return d, nil
		}
		at(t)
		mountApp(t)
		h := newHub(mountedStore, quiet{})
		h.fill(context.Background())
		// The newest orgs and the seeded awards, oldest first.
		if len(h.ring) < 4 {
			t.Fatalf("an empty hub stayed empty: %s", kinds(h.ring))
		}
		for i := 1; i < len(h.ring); i++ {
			if h.ring[i].At < h.ring[i-1].At {
				t.Fatalf("not oldest first: %v then %v", h.ring[i-1].At, h.ring[i].At)
			}
		}
		if !strings.Contains(kinds(h.ring), "bounty:va•••ov:+50 pts") || !strings.Contains(kinds(h.ring), "joined:ne•••st:") {
			t.Fatalf("lines = %s", kinds(h.ring))
		}
		// What is filled in is placed by when it happened, and a hub that has been filled is not filled again.
		n := len(h.ring)
		h.fill(context.Background())
		if len(h.ring) != n {
			t.Fatal("a hub with lines was filled again")
		}
	})
}

func TestALineLearnedLateTakesItsPlaceInTime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := aHub(t)
		h.say(kindJoined, "newer", "", noon)
		h.say(kindJoined, "older", "", noon.Add(-48*time.Hour))
		if len(h.ring) != 2 || h.ring[0].At != "2026-11-06T13:30:00Z" || h.ring[1].At != "2026-11-08T13:30:00Z" {
			t.Fatalf("not oldest first: %+v", h.ring)
		}
	})
}

func TestTheStreamNeedsNoPrincipal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		world(t, []iamOrg{{"someone", "2026-10-01T00:00:00Z"}}, nil, nil, nil)
		app := mountApp(t)
		t.Cleanup(stopHub)
		req := httptest.NewRequest(http.MethodGet, "/v1/leaderboard/feed", nil)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 500 * time.Millisecond, FailOnTimeout: false})
		if err != nil {
			t.Fatalf("feed with no principal: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("feed with no principal: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		// And the rest still need one (TestStandingIsPublicAndTheRestIsNot holds the others).
		if code, _ := doGet(t, app, "/v1/leaderboard/bounty", nil); code != http.StatusUnauthorized {
			t.Fatalf("bounty with no principal: %d", code)
		}
	})
}

// quiet is a log that says nothing.
type quiet struct{}

func (quiet) Warn(string, ...any) {}
