package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// progress_test.go holds the two invariants this feature is only worth having if
// it keeps, and neither is about the model being right.
//
//   - UNKNOWN NEVER RENDERS AS ZERO. A run nobody can estimate has made an
//     unknown amount of progress, and an empty bar says it has made none. The
//     tests assert on the RAW BYTES for this, because a Go zero value and an
//     absent key are the same value in a decoded struct and different facts on
//     the wire — which is exactly the confusion being guarded against.
//   - AN ESTIMATE IS DEBOUNCED. A board polls, and a poll that becomes a
//     completion is a bill that grows with how long a tab is left open.
//
// Every test drives the LIVE routes, so what is asserted is what a board reads.

// errUpstreamForTest stands in for a gateway that is not answering.
var errUpstreamForTest = errors.New("upstream unavailable")

// progressAI is a scripted AI plane that COUNTS. The count is the whole point of
// several tests below, and fakeAI cannot carry it: it records only the last call
// and is not safe under the goroutine a list read starts.
type progressAI struct {
	mu     sync.Mutex
	calls  int
	reply  string
	err    error
	prompt string
}

func (f *progressAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.prompt = req.Text()
	if f.err != nil {
		return nil, f.err
	}
	return &types.ChatResponse{Content: f.reply}, nil
}

func (f *progressAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

func (f *progressAI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *progressAI) sent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompt
}

// openSession registers one session over the real route and returns its id.
func openSession(t *testing.T, app *zip.App, org, title string) string {
	t.Helper()
	code, body := do(t, app, http.MethodPost, "/v1/agents/sessions", org,
		map[string]any{"agent": "hanzo-dev", "title": title})
	if code != http.StatusCreated {
		t.Fatalf("register session: %d %s", code, body)
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("register decode: %v", err)
	}
	return v.ID
}

// logTurn appends one turn over the real route, which is what makes the session
// look like a run with a transcript rather than an empty row.
func logTurn(t *testing.T, app *zip.App, org, id, kind, payload string) {
	t.Helper()
	code, body := do(t, app, http.MethodPost, "/v1/agents/sessions/"+id+"/events", org,
		map[string]any{"kind": kind, "payload": json.RawMessage(payload)})
	if code != http.StatusCreated {
		t.Fatalf("append %s: %d %s", kind, code, body)
	}
}

// readProgress asks the one address that WAITS, and hands back the raw bytes so
// a test can ask whether a key is present rather than what it decoded to.
func readProgress(t *testing.T, app *zip.App, org, id string) (sessionProgress, []byte) {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/agents/sessions/"+id+"/progress", org, nil)
	if code != http.StatusOK {
		t.Fatalf("progress: %d %s", code, body)
	}
	var p sessionProgress
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("progress decode: %v (%s)", err, body)
	}
	return p, body
}

// progressInList pulls one session's progress out of the LIST read, which is the
// read a board actually makes.
func progressInList(t *testing.T, app *zip.App, org, id string) (sessionProgress, []byte) {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/agents/sessions", org, nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var list struct {
		Sessions []struct {
			ID       string          `json:"id"`
			Progress sessionProgress `json:"progress"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	for _, s := range list.Sessions {
		if s.ID == id {
			return s.Progress, body
		}
	}
	t.Fatalf("session %s not in the list: %s", id, body)
	return sessionProgress{}, nil
}

// TestProgressUnknownIsNeverZero is the house law, asserted on the bytes.
//
// A session nothing has estimated must publish phase "unknown" and NO pct key at
// all. Decoding into a struct cannot see the difference — an absent number and a
// zero both arrive as 0 — so the assertion is on the raw JSON, and it is the
// assertion that would catch a future `Pct int` refactor that looks harmless.
func TestProgressUnknownIsNeverZero(t *testing.T) {
	ai := &progressAI{reply: `{"pct":40,"phase":"running","activity":"x"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "ship the landing page")

	p, raw := progressInList(t, app, "acme", id)
	if p.Phase != phaseUnknown {
		t.Fatalf("a session nothing has estimated must read %q, got %q", phaseUnknown, p.Phase)
	}
	if p.Pct != nil {
		t.Fatalf("unknown progress must carry NO pct, got %d", *p.Pct)
	}
	if p.Estimated {
		t.Fatal("nothing estimated this, so `estimated` must be false")
	}
	// The bytes, not the struct: `"pct"` must not appear at all. A zero would.
	if body := string(raw); strings.Contains(body, `"pct"`) {
		t.Fatalf("the wire carries a pct for a run nobody has estimated: %s", body)
	}
}

// TestProgressReachesTheSessionRead is the end-to-end proof: a run with a
// transcript, one estimate, and the number on the board's own read.
func TestProgressReachesTheSessionRead(t *testing.T) {
	ai := &progressAI{reply: `{"pct":45,"phase":"running","activity":"running the reaper's tests"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "write the session reaper")
	logTurn(t, app, "acme", id, KindMessage, `{"text":"plan: write the reaper, then its tests"}`)
	logTurn(t, app, "acme", id, KindToolCall, `{"tool":"zsh","args":"go test ./apps/agents"}`)

	p, _ := readProgress(t, app, "acme", id)
	if p.Pct == nil || *p.Pct != 45 {
		t.Fatalf("pct = %v, want 45", p.Pct)
	}
	if p.Phase != phaseRunning || p.Activity != "running the reaper's tests" {
		t.Fatalf("phase/activity = %q/%q", p.Phase, p.Activity)
	}
	if !p.Estimated {
		t.Fatal("a model made this number, so `estimated` must be true")
	}
	if p.At == "" {
		t.Fatal("an estimate with no timestamp cannot be aged, which is what makes it honest")
	}

	// The same value on the LIST read — the one a board makes — with no second
	// completion, because the estimate was persisted rather than recomputed.
	before := ai.count()
	got, _ := progressInList(t, app, "acme", id)
	if got.Pct == nil || *got.Pct != 45 || got.Activity != p.Activity || !got.Estimated {
		t.Fatalf("the list must carry the same estimate, got %+v", got)
	}
	if ai.count() != before {
		t.Fatalf("the list re-estimated inside the interval: %d calls, want %d", ai.count(), before)
	}

	// The brief is built from the run: its goal and its turns reach the model.
	if sent := ai.sent(); !strings.Contains(sent, "write the session reaper") ||
		!strings.Contains(sent, "go test ./apps/agents") {
		t.Fatalf("the brief carried neither the goal nor the transcript:\n%s", sent)
	}
}

// TestTheBriefCarriesTheLATESTTurns, which is the whole question being asked.
//
// This caught a real bug: ListEvents pages FORWARD from a cursor, so asking it
// for twenty turns with no cursor answers with a run's FIRST twenty — the
// opposite end of the log from "what is it doing now". A short run cannot see
// the difference, which is exactly why this one is long.
func TestTheBriefCarriesTheLATESTTurns(t *testing.T) {
	ai := &progressAI{reply: `{"pct":50,"phase":"running","activity":"a"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "a long run")
	for i := 1; i <= progressTail+10; i++ {
		logTurn(t, app, "acme", id, KindLog, `{"step":`+strconv.Itoa(i)+`}`)
	}
	readProgress(t, app, "acme", id)

	sent := ai.sent()
	// The OPENING turn rides along regardless, because a percentage needs the
	// denominator the ask states, and on a long run it has scrolled off the tail.
	// It is a section of its own, so the tail is asserted on what follows it.
	head, latest, ok := strings.Cut(sent, "latest turns:")
	if !ok || !strings.Contains(head, `"step":1}`) {
		t.Fatalf("a run longer than the tail must still carry its opening turn:\n%s", sent)
	}
	if !strings.Contains(latest, `"step":`+strconv.Itoa(progressTail+10)+`}`) {
		t.Fatalf("the newest turn is missing from the brief:\n%s", sent)
	}
	if strings.Contains(latest, `"step":1}`) {
		t.Fatalf("the brief carried the run's FIRST turns instead of its latest:\n%s", sent)
	}
	if n := strings.Count(latest, `"step":`); n != progressTail {
		t.Fatalf("the tail is %d turns, want %d — the bound is what keeps this cheap", n, progressTail)
	}
}

// TestTheListRefreshesBehindItsAnswer: a board read answers from the row and
// brings the estimate up to date behind it, so the number is current on the next
// poll without the read ever waiting on a model.
func TestTheListRefreshesBehindItsAnswer(t *testing.T) {
	ai := &progressAI{reply: `{"pct":80,"phase":"running","activity":"nearly there"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "a run")
	logTurn(t, app, "acme", id, KindLog, `{"line":"one"}`)

	first, _ := progressInList(t, app, "acme", id)
	if first.Phase != phaseUnknown {
		t.Fatalf("the first read must answer from the row, got %+v", first)
	}
	// The estimate lands behind it. Poll rather than sleep a fixed span: what is
	// being asserted is that it arrives, not how fast.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := progressInList(t, app, "acme", id)
		if got.Pct != nil && *got.Pct == 80 && got.Estimated {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the list never picked up the estimate it kicked off: %+v (%d calls)", got, ai.count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// setClocks puts a session's two clocks exactly where a test needs them, because
// the ONLY way to tell the two halves of `stale` apart is to move one at a time.
//
// A test that just makes calls cannot do it: everything happens inside one
// second, and these stamps are second-granular, so "the transcript has not
// moved" and "the interval has not elapsed" are true together and either alone
// would pass. That is the shape of an unasserted invariant — and it was live
// here: the debounce test passed with the interval term DELETED until this
// existed.
func setClocks(t *testing.T, org, id string, updatedAt, progressAt int64) {
	t.Helper()
	ctx := context.Background()
	sto := storeOf(t, &mounted.State, org)
	x, err := sto.GetSession(ctx, org, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	x.UpdatedAt = updatedAt
	if err := sto.UpdateSession(ctx, x); err != nil {
		t.Fatalf("set updated_at: %v", err)
	}
	if err := sto.SetProgress(ctx, org, id, Progress{
		Pct: x.ProgressPct, Phase: x.ProgressPhase, Activity: x.ProgressActivity,
		At: progressAt, Estimated: x.ProgressEstimated,
	}); err != nil {
		t.Fatalf("set progress_at: %v", err)
	}
}

// TestProgressEstimateIsDebounced is the cost invariant, and it isolates the
// INTERVAL term: the transcript has moved in both halves below, so the only
// thing deciding is how long ago the last estimate was made. A board polls, and
// a poll that becomes a completion is a bill that grows with how long a tab is
// left open.
//
// Mutation-checked: delete `now-x.ProgressAt >= interval` from stale and the
// middle assertion goes to two calls.
func TestProgressEstimateIsDebounced(t *testing.T) {
	ai := &progressAI{reply: `{"pct":10,"phase":"running","activity":"a"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "a run")
	logTurn(t, app, "acme", id, KindLog, `{"line":"one"}`)
	readProgress(t, app, "acme", id)
	if ai.count() != 1 {
		t.Fatalf("first read must estimate once, got %d", ai.count())
	}

	// The transcript is a second AHEAD of the estimate, so that term says yes; the
	// estimate is a second old, so the interval says no.
	now := time.Now().Unix()
	setClocks(t, "acme", id, now, now-1)
	readProgress(t, app, "acme", id)
	if ai.count() != 1 {
		t.Fatalf("a read inside the interval must NOT re-estimate: %d calls, want 1", ai.count())
	}

	// Same transcript term, interval now elapsed: it estimates again, or the number
	// would freeze for the life of the run.
	setClocks(t, "acme", id, now, now-int64(2*progressInterval/time.Second))
	readProgress(t, app, "acme", id)
	if ai.count() != 2 {
		t.Fatalf("past the interval it must re-estimate: %d calls, want 2", ai.count())
	}
}

// TestProgressIsNotReEstimatedWhileNothingHappens isolates the TRANSCRIPT term:
// the interval has elapsed in both halves below, so the only thing deciding is
// whether the run has said anything since. Re-reading an unchanged log produces
// the same answer from the same input, which is what makes a fleet of mostly
// idle runs nearly free.
//
// Mutation-checked: delete `x.UpdatedAt > x.ProgressAt` from stale and the first
// assertion goes to two calls.
func TestProgressIsNotReEstimatedWhileNothingHappens(t *testing.T) {
	ai := &progressAI{reply: `{"pct":70,"phase":"running","activity":"a"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "a quiet run")
	logTurn(t, app, "acme", id, KindLog, `{"line":"one"}`)
	readProgress(t, app, "acme", id)

	// Long past the interval, and the run has said nothing since the estimate.
	now := time.Now().Unix()
	aged := int64(2 * progressInterval / time.Second)
	setClocks(t, "acme", id, now-aged-10, now-aged)
	readProgress(t, app, "acme", id)
	if ai.count() != 1 {
		t.Fatalf("a run that has said nothing must not be re-read: %d calls, want 1", ai.count())
	}

	// One turn, and the same read costs a completion.
	setClocks(t, "acme", id, now, now-aged)
	readProgress(t, app, "acme", id)
	if ai.count() != 2 {
		t.Fatalf("a run that HAS said something must be re-read: %d calls, want 2", ai.count())
	}
}

// TestTerminalProgressIsNotAnEstimate: a finished run reports its own row and
// nothing guesses at it. done is 100% because it finished; error carries NO
// percentage, because "how far along" has no answer for a run that stopped.
func TestTerminalProgressIsNotAnEstimate(t *testing.T) {
	for _, tc := range []struct {
		status string
		phase  string
		pct    int
		hasPct bool
	}{
		{StatusDone, phaseDone, 100, true},
		{StatusError, StatusError, 0, false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			ai := &progressAI{reply: `{"pct":33,"phase":"running","activity":"a"}`}
			app := mountApp(t, ai)
			id := openSession(t, app, "acme", "a run")
			if code, body := do(t, app, http.MethodPatch, "/v1/agents/sessions/"+id, "acme",
				map[string]any{"status": tc.status}); code != http.StatusOK {
				t.Fatalf("patch: %d %s", code, body)
			}

			p, raw := readProgress(t, app, "acme", id)
			if p.Phase != tc.phase {
				t.Fatalf("phase = %q, want %q", p.Phase, tc.phase)
			}
			if p.Estimated {
				t.Fatal("a terminal session's progress is the ROW's word, so `estimated` must be false")
			}
			if tc.hasPct {
				if p.Pct == nil || *p.Pct != tc.pct {
					t.Fatalf("pct = %v, want %d", p.Pct, tc.pct)
				}
			} else if strings.Contains(string(raw), `"pct"`) {
				t.Fatalf("a failed run must carry no pct: %s", raw)
			}
			if ai.count() != 0 {
				t.Fatalf("a terminal run must cost no completion: %d calls", ai.count())
			}
		})
	}
}

// TestBlockedIsWhatTheRowCannotSay is the field's whole reason for existing: the
// session's own status says running, because the surface running it has no idea
// it is stuck, and the estimate says blocked.
func TestBlockedIsWhatTheRowCannotSay(t *testing.T) {
	ai := &progressAI{reply: `{"pct":60,"phase":"blocked","activity":"waiting on an approval"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "deploy the thing")
	logTurn(t, app, "acme", id, KindMessage, `{"text":"waiting for someone to approve the rollout"}`)

	p, _ := readProgress(t, app, "acme", id)
	if p.Phase != phaseBlocked || !p.Estimated {
		t.Fatalf("progress = %+v, want a blocked estimate", p)
	}
	// The row is untouched: an estimate reads a run, it never steers one.
	list, _ := progressInList(t, app, "acme", id)
	if list.Phase != phaseBlocked {
		t.Fatalf("the board must see blocked too, got %q", list.Phase)
	}
	x, err := storeOf(t, &mounted.State, "acme").GetSession(context.Background(), "acme", id)
	if err != nil || x.Status != StatusRunning {
		t.Fatalf("the session's own status must still be running, got %q (%v)", x.Status, err)
	}
}

// TestAnEstimateIsOnlyReplacedByAnEstimate: a model outage and an unreadable
// reply both advance the clock — or every poll would retry exactly when the
// gateway is already unwell — and both leave the last good answer standing,
// whose growing age is published beside it.
func TestAnEstimateIsOnlyReplacedByAnEstimate(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*progressAI)
	}{
		{"the gateway is down", func(f *progressAI) { f.err = errUpstreamForTest }},
		{"the reply is unreadable", func(f *progressAI) { f.reply = "I think it's going well!" }},
		{"the phase is not one of ours", func(f *progressAI) { f.reply = `{"pct":90,"phase":"vibing"}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ai := &progressAI{reply: `{"pct":55,"phase":"running","activity":"the good one"}`}
			app := mountApp(t, ai)
			id := openSession(t, app, "acme", "a run")
			logTurn(t, app, "acme", id, KindLog, `{"line":"one"}`)
			good, _ := readProgress(t, app, "acme", id)
			if good.Pct == nil || *good.Pct != 55 {
				t.Fatalf("seed estimate = %+v", good)
			}

			ctx := context.Background()
			sto := storeOf(t, &mounted.State, "acme")
			x, _ := sto.GetSession(ctx, "acme", id)
			aged := time.Now().Add(-2 * progressInterval).Unix()
			if err := sto.SetProgress(ctx, "acme", id, Progress{
				Pct: x.ProgressPct, Phase: x.ProgressPhase, Activity: x.ProgressActivity,
				At: aged, Estimated: x.ProgressEstimated,
			}); err != nil {
				t.Fatalf("age: %v", err)
			}
			logTurn(t, app, "acme", id, KindLog, `{"line":"two"}`) // make it stale
			tc.fail(ai)

			p, _ := readProgress(t, app, "acme", id)
			if p.Pct == nil || *p.Pct != 55 || p.Activity != "the good one" || !p.Estimated {
				t.Fatalf("the last good estimate must survive, got %+v", p)
			}
			after, _ := sto.GetSession(ctx, "acme", id)
			if after.ProgressAt <= aged {
				t.Fatalf("the clock must advance so the next poll does not retry at once: %d <= %d",
					after.ProgressAt, aged)
			}
		})
	}
}

// TestNoAIPlaneLeavesProgressUnknown: a deployment with no gateway answers the
// address and says it does not know, which is the honest degrade every other AI
// path in this package takes.
func TestNoAIPlaneLeavesProgressUnknown(t *testing.T) {
	app := mountApp(t, nil)
	id := openSession(t, app, "acme", "a run")
	logTurn(t, app, "acme", id, KindLog, `{"line":"one"}`)

	p, raw := readProgress(t, app, "acme", id)
	if p.Phase != phaseUnknown || p.Estimated {
		t.Fatalf("progress = %+v, want an unestimated unknown", p)
	}
	if strings.Contains(string(raw), `"pct"`) {
		t.Fatalf("no AI plane must not mean 0%%: %s", raw)
	}
}

// TestProgressIsOrgScoped: the address is org-scoped like every other read here,
// and an anonymous caller is refused before a session id is ever resolved.
func TestProgressIsOrgScoped(t *testing.T) {
	app := mountApp(t, &progressAI{reply: `{"pct":50,"phase":"running","activity":"a"}`})
	id := openSession(t, app, "acme", "a run")

	if code, _ := do(t, app, http.MethodGet, "/v1/agents/sessions/"+id+"/progress", "other", nil); code != http.StatusNotFound {
		t.Fatalf("another org must not resolve this session, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/sessions/"+id+"/progress", "", nil); code != http.StatusForbidden {
		t.Fatalf("anonymous must be refused, got %d", code)
	}
}

// TestParseEstimate pins the reply contract: forgiving about what surrounds the
// object, strict about the closed phase vocabulary, and INDETERMINATE rather
// than zero for every number it cannot use.
func TestParseEstimate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		ok    bool
		pct   int
		phase string
	}{
		{"plain", `{"pct":42,"phase":"running","activity":"a"}`, true, 42, phaseRunning},
		{"wrapped in prose", "Sure!\n```json\n{\"pct\":7,\"phase\":\"blocked\"}\n```\n", true, 7, phaseBlocked},
		{"pct as a string", `{"pct":"88","phase":"running"}`, true, 88, phaseRunning},
		{"the model said it cannot tell", `{"pct":-1,"phase":"running"}`, true, pctUnknown, phaseRunning},
		{"pct out of range", `{"pct":420,"phase":"running"}`, true, pctUnknown, phaseRunning},
		{"pct is prose", `{"pct":"most of it","phase":"running"}`, true, pctUnknown, phaseRunning},
		{"done with no number is 100", `{"phase":"done"}`, true, 100, phaseDone},
		{"phase outside the four", `{"pct":50,"phase":"vibing"}`, false, 0, ""},
		{"no phase at all", `{"pct":50}`, false, 0, ""},
		{"no object", `it is going fine`, false, 0, ""},
		{"empty", ``, false, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := parseEstimate(tc.reply)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, p)
			}
			if !ok {
				return
			}
			if p.Pct != tc.pct || p.Phase != tc.phase {
				t.Fatalf("got pct=%d phase=%q, want pct=%d phase=%q", p.Pct, p.Phase, tc.pct, tc.phase)
			}
		})
	}
}

// TestActivityIsOneLine: the activity is rendered on a card, so a model that
// answers with a paragraph is cut here rather than by whoever draws it.
func TestActivityIsOneLine(t *testing.T) {
	long := strings.Repeat("running the tests ", 40)
	p, ok := parseEstimate(`{"pct":10,"phase":"running","activity":"` + long + `"}`)
	if !ok {
		t.Fatal("parse")
	}
	if strings.Contains(p.Activity, "\n") {
		t.Fatal("the activity must be one line")
	}
	if len(p.Activity) > progressLine+8 { // +8 for the ellipsis rune and rounding
		t.Fatalf("activity is %d bytes, want <= %d", len(p.Activity), progressLine)
	}
}

// TestConcurrentEstimatesAreBounded: the in-flight set never outgrows its cap,
// and every claim is released — the two properties that keep it from needing a
// reclaim pass of its own.
func TestConcurrentEstimatesAreBounded(t *testing.T) {
	e := newEstimator(&progressAI{})
	for i := 0; i < progressBusy; i++ {
		if !e.claim("acme", string(rune('a'+i))) {
			t.Fatalf("claim %d refused below the ceiling", i)
		}
	}
	if e.claim("acme", "one-too-many") {
		t.Fatal("claim past the ceiling must be refused")
	}
	if e.claim("acme", "a") {
		t.Fatal("a second claim on one run must be refused")
	}
	for i := 0; i < progressBusy; i++ {
		e.release("acme", string(rune('a'+i)))
	}
	if len(e.inflight) != 0 {
		t.Fatalf("release must empty the set, %d left", len(e.inflight))
	}
	if !e.claim("acme", "one-too-many") {
		t.Fatal("the ceiling must free up again")
	}
}

// ---- the run's own word ----

// report appends a `progress` turn — the run reporting on itself — and returns
// the append's status and body so a test can assert a refusal too.
func report(t *testing.T, app *zip.App, org, id, payload string) (int, []byte) {
	t.Helper()
	return do(t, app, http.MethodPost, "/v1/agents/sessions/"+id+"/events", org,
		map[string]any{"kind": KindProgress, "payload": json.RawMessage(payload)})
}

// TestASelfReportIsGroundTruth: a run's own report lands on the session read
// marked NOT estimated, and it costs no completion. `estimated` is the whole
// point — the same field, the same shape, and the reader learns which produced it.
func TestASelfReportIsGroundTruth(t *testing.T) {
	ai := &progressAI{reply: `{"pct":10,"phase":"running","activity":"a guess"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "ship the reaper")

	if code, body := report(t, app, "acme", id,
		`{"pct":60,"phase":"running","activity":"writing the reaper"}`); code != http.StatusCreated {
		t.Fatalf("report: %d %s", code, body)
	}

	p, _ := progressInList(t, app, "acme", id)
	if p.Pct == nil || *p.Pct != 60 || p.Phase != phaseRunning || p.Activity != "writing the reaper" {
		t.Fatalf("the board must carry the run's own report, got %+v", p)
	}
	if p.Estimated {
		t.Fatal("the RUN said this, so `estimated` must be false")
	}
	if ai.count() != 0 {
		t.Fatalf("a run that reports for itself must cost no completion: %d calls", ai.count())
	}
}

// TestASelfReportBeatsTheEstimate is the precedence rule, driven in the order it
// actually happens: the model estimates first, then the run reports, and the
// board carries the run's word.
//
// Mutation-checked: make the append skip SetProgress and the estimate stands.
func TestASelfReportBeatsTheEstimate(t *testing.T) {
	ai := &progressAI{reply: `{"pct":25,"phase":"running","activity":"the guess"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "a run")
	logTurn(t, app, "acme", id, KindLog, `{"line":"one"}`)

	est, _ := readProgress(t, app, "acme", id)
	if est.Pct == nil || *est.Pct != 25 || !est.Estimated {
		t.Fatalf("seed estimate = %+v", est)
	}

	if code, body := report(t, app, "acme", id,
		`{"pct":80,"phase":"running","activity":"the truth"}`); code != http.StatusCreated {
		t.Fatalf("report: %d %s", code, body)
	}
	got, _ := progressInList(t, app, "acme", id)
	if got.Pct == nil || *got.Pct != 80 || got.Activity != "the truth" || got.Estimated {
		t.Fatalf("the run's report must outrank the estimate, got %+v", got)
	}
}

// TestAFreshSelfReportIsTheLastWord: the estimator does not re-guess over a run
// that has just reported, because the report IS the transcript's newest turn —
// so the two terms of `stale` already say no, with no third rule to keep.
//
// Then the run logs something else without reporting, the interval elapses, and
// the estimate takes over again: a self-report holds the field while it is the
// last thing the run said, and no longer.
func TestAFreshSelfReportIsTheLastWord(t *testing.T) {
	ai := &progressAI{reply: `{"pct":90,"phase":"running","activity":"the guess"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "a run")
	report(t, app, "acme", id, `{"pct":40,"phase":"blocked","activity":"waiting on review"}`)

	p, _ := readProgress(t, app, "acme", id)
	if p.Estimated || p.Phase != phaseBlocked {
		t.Fatalf("a fresh report must not be re-guessed, got %+v", p)
	}
	if ai.count() != 0 {
		t.Fatalf("no completion should have been bought: %d", ai.count())
	}

	// The run moves on without reporting, and the interval passes.
	logTurn(t, app, "acme", id, KindLog, `{"line":"kept going"}`)
	now := time.Now().Unix()
	setClocks(t, "acme", id, now, now-int64(2*progressInterval/time.Second))

	p, _ = readProgress(t, app, "acme", id)
	if !p.Estimated || p.Pct == nil || *p.Pct != 90 {
		t.Fatalf("a stale report must fall back to the estimate, got %+v", p)
	}
}

// TestAnEstimateInFlightCannotClobberAReport is the RACE, and it is the reason
// the estimator writes with a compare-and-set. The model is asked over a
// transcript read before the run reported; by the time it answers, the run has
// spoken. The guess must lose.
//
// Mutation-checked: turn SetEstimate's write back into an unconditional
// SetProgress and the report is overwritten by a number formed before it.
func TestAnEstimateInFlightCannotClobberAReport(t *testing.T) {
	ctx := context.Background()
	app := mountApp(t, &progressAI{reply: `{"pct":5,"phase":"running","activity":"stale guess"}`})
	id := openSession(t, app, "acme", "a run")
	logTurn(t, app, "acme", id, KindLog, `{"line":"one"}`)

	sto := storeOf(t, &mounted.State, "acme")
	before, err := sto.GetSession(ctx, "acme", id) // the snapshot an estimate is formed over
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// The run reports while the model is thinking.
	if code, body := report(t, app, "acme", id,
		`{"pct":70,"phase":"running","activity":"the truth"}`); code != http.StatusCreated {
		t.Fatalf("report: %d %s", code, body)
	}

	// The estimate lands afterwards, carrying the stamp it read.
	wrote, err := sto.SetEstimate(ctx, "acme", id,
		Progress{Pct: 5, Phase: phaseRunning, Activity: "stale guess", At: time.Now().Unix(), Estimated: true},
		before.ProgressAt)
	if err != nil {
		t.Fatalf("set estimate: %v", err)
	}
	if wrote {
		t.Fatal("an estimate formed before the run reported must not write")
	}
	got, _ := progressInList(t, app, "acme", id)
	if got.Pct == nil || *got.Pct != 70 || got.Estimated {
		t.Fatalf("the run's report must survive the late estimate, got %+v", got)
	}
}

// TestASelfReportStreamsLive: the report reaches an open subscriber as a SESSION
// frame carrying the new progress — so a board's bar moves without polling, and
// with nothing new for a client to parse.
func TestASelfReportStreamsLive(t *testing.T) {
	app := mountApp(t, &progressAI{})
	id := openSession(t, app, "acme", "a run")

	sub, cancel := mounted.State.bus.subscribe("acme")
	defer cancel()

	if code, body := report(t, app, "acme", id,
		`{"pct":55,"phase":"blocked","activity":"waiting on a credential"}`); code != http.StatusCreated {
		t.Fatalf("report: %d %s", code, body)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case u, ok := <-sub:
			if !ok {
				t.Fatal("the subscription closed before the session frame arrived")
			}
			if u.Type != "session" || u.Session == nil || u.Session.ID != id {
				continue // the event frame rides the same bus; keep reading
			}
			p := u.Session.Progress
			if p.Pct == nil || *p.Pct != 55 || p.Phase != phaseBlocked || p.Estimated {
				t.Fatalf("the streamed session must carry the report, got %+v", p)
			}
			return
		case <-deadline:
			t.Fatal("no session frame carried the progress; a board would have to poll")
		}
	}
}

// TestAMalformedReportIsRefusedAndStoresNothing: the payload shape is OURS, so a
// client that gets it wrong is told, rather than having half its meaning kept —
// and the turn does not reach the transcript, so a run cannot believe it reported.
func TestAMalformedReportIsRefusedAndStoresNothing(t *testing.T) {
	app := mountApp(t, &progressAI{})
	for _, tc := range []struct{ name, payload string }{
		{"no phase", `{"pct":50}`},
		{"a phase we do not have", `{"phase":"vibing"}`},
		{"pct over 100", `{"pct":101,"phase":"running"}`},
		{"pct below zero", `{"pct":-1,"phase":"running"}`},
		{"pct is prose", `{"pct":"most of it","phase":"running"}`},
		{"no payload at all", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := openSession(t, app, "acme", "a run")
			code, body := report(t, app, "acme", id, tc.payload)
			if code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d %s", code, body)
			}
			if n, _ := storeOf(t, &mounted.State, "acme").CountEvents(context.Background(), "acme", id); n != 0 {
				t.Fatalf("a refused report must leave no turn behind, got %d", n)
			}
			p, raw := progressInList(t, app, "acme", id)
			if p.Phase != phaseUnknown || strings.Contains(string(raw), `"pct"`) {
				t.Fatalf("a refused report must move nothing: %+v", p)
			}
		})
	}
}

// TestAReportMayNameAPhaseWithoutANumber: a run that knows it is stuck and does
// not know how far along it is says the half it knows — and the wire still
// carries NO pct, which is the house law holding for the ground-truth path too.
func TestAReportMayNameAPhaseWithoutANumber(t *testing.T) {
	app := mountApp(t, &progressAI{})
	id := openSession(t, app, "acme", "a run")
	if code, body := report(t, app, "acme", id,
		`{"phase":"blocked","activity":"waiting on an approval"}`); code != http.StatusCreated {
		t.Fatalf("report: %d %s", code, body)
	}
	p, raw := progressInList(t, app, "acme", id)
	if p.Phase != phaseBlocked || p.Activity != "waiting on an approval" || p.Estimated {
		t.Fatalf("progress = %+v", p)
	}
	if p.Pct != nil {
		t.Fatalf("a report with no number must carry none, got %d", *p.Pct)
	}
	if strings.Contains(string(raw), `"pct"`) {
		t.Fatalf("unknown must not render as zero on the ground-truth path either: %s", raw)
	}
}

// TestAReportIsDurableHistory: the turn stays in the transcript, so how a run's
// own sense of its progress MOVED is replayable — which an estimate overwritten
// in place could never be.
func TestAReportIsDurableHistory(t *testing.T) {
	app := mountApp(t, &progressAI{})
	id := openSession(t, app, "acme", "a run")
	for _, pct := range []string{"10", "40", "90"} {
		if code, body := report(t, app, "acme", id,
			`{"pct":`+pct+`,"phase":"running","activity":"step"}`); code != http.StatusCreated {
			t.Fatalf("report %s: %d %s", pct, code, body)
		}
	}
	events, err := storeOf(t, &mounted.State, "acme").ListEvents(context.Background(), "acme", id, 0, 10)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 progress turns in the transcript, got %d", len(events))
	}
	for i, want := range []string{`"pct":10`, `"pct":40`, `"pct":90`} {
		if events[i].Kind != KindProgress || !strings.Contains(events[i].Payload, want) {
			t.Fatalf("turn %d = %s %s, want a progress turn carrying %s", i, events[i].Kind, events[i].Payload, want)
		}
	}
}

// TestAReportIsOrgScopedAndGuarded: the report rides the append route, so it
// inherits that route's tenancy and its credential scan rather than restating
// either. Asserted because inheriting a gate is only worth anything if the gate
// is shown to still be there.
func TestAReportIsOrgScopedAndGuarded(t *testing.T) {
	app := mountApp(t, &progressAI{})
	id := openSession(t, app, "acme", "a run")

	if code, _ := report(t, app, "other", id, `{"pct":50,"phase":"running"}`); code != http.StatusNotFound {
		t.Fatalf("another org must not report on this session, got %d", code)
	}
	if code, _ := report(t, app, "", id, `{"pct":50,"phase":"running"}`); code != http.StatusForbidden {
		t.Fatalf("anonymous must be refused, got %d", code)
	}
	code, body := report(t, app, "acme", id,
		`{"pct":50,"phase":"running","activity":"AKIAIOSFODNN7EXAMPLE"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a secret in a report must be refused by the same guard: %d %s", code, body)
	}
}

// TestAnOutageDoesNotRelabelAReportAsAGuess: the estimator advances the clock
// over a value it could not replace, and that write must not change WHO said it.
// Stamping "estimated" on the keep path would turn a run's own report into a
// guess the first time the gateway hiccuped — the honesty law running backwards.
//
// Mutation-checked: write a literal 1 for the provenance in SetEstimate and this
// goes red while every other test stays green, which is why it is its own test.
func TestAnOutageDoesNotRelabelAReportAsAGuess(t *testing.T) {
	ai := &progressAI{reply: `{"pct":30,"phase":"running","activity":"a guess"}`}
	app := mountApp(t, ai)
	id := openSession(t, app, "acme", "a run")
	if code, body := report(t, app, "acme", id,
		`{"pct":65,"phase":"running","activity":"the run's own word"}`); code != http.StatusCreated {
		t.Fatalf("report: %d %s", code, body)
	}

	// The run moves on, the interval elapses, and the gateway is down.
	logTurn(t, app, "acme", id, KindLog, `{"line":"kept going"}`)
	now := time.Now().Unix()
	setClocks(t, "acme", id, now, now-int64(2*progressInterval/time.Second))
	ai.err = errUpstreamForTest

	p, _ := readProgress(t, app, "acme", id)
	if p.Pct == nil || *p.Pct != 65 || p.Activity != "the run's own word" {
		t.Fatalf("the report must survive the outage, got %+v", p)
	}
	if p.Estimated {
		t.Fatal("a failed estimate must not re-label the run's own report as a guess")
	}
	x, err := storeOf(t, &mounted.State, "acme").GetSession(context.Background(), "acme", id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if x.ProgressEstimated {
		t.Fatal("the stored provenance moved on a write that produced no value")
	}
	if x.ProgressAt <= now-int64(2*progressInterval/time.Second) {
		t.Fatal("the clock must still advance, or every poll retries into the outage")
	}
}
