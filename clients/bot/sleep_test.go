package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// What calls a parked run back: it names events, one of them happens, and the
// run is due. The helpers are here rather than in bot_test.go because nothing
// else in the surface parks anything.

// park announces a run and puts it to sleep naming what should call it back.
// It is the pair of requests a bot loop makes when it stops for a while.
func park(t *testing.T, app *zip.App, org, id string, wake ...string) {
	t.Helper()
	announced(t, app, org, id)
	if code, b := as(t, app, org, http.MethodPost, "/v1/bot/runs/"+id+"/suspend",
		`{"resume":"ckpt-`+id+`","wake":`+listed(wake)+`}`); code != http.StatusOK {
		t.Fatalf("suspend %s: %d %s", id, code, b)
	}
}

func listed(ss []string) string {
	b, err := json.Marshal(ss)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// rest issues one registry request against a served listener, for the tests
// that hold a socket address rather than the app.
func rest(t *testing.T, url, org, method, path, body string) (int, string) {
	t.Helper()
	at := "http" + strings.TrimSuffix(strings.TrimPrefix(url, "ws"), "/bot") + "/bot" + path
	req, err := http.NewRequest(method, at, strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u@"+org)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close() //nolint:errcheck
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return res.StatusCode, string(b)
}

// A parked run names what should call it back; the event happens; the run is
// due. The row says so, the org hears it, the reason is spent, and the token the
// run left with is still waiting at the one door tokens leave by — because a
// wake makes a run due and does not spend the suspension it is holding.
func TestAParkedRunIsCalledBackByTheEventItNamed(t *testing.T) {
	url := serve(t)
	const id = "run_named_one"
	if code, b := rest(t, url, "acme", http.MethodPost, "/runs",
		`{"runId":"`+id+`","where":"local"}`); code != http.StatusCreated {
		t.Fatalf("announce: %d %s", code, b)
	}
	if code, b := rest(t, url, "acme", http.MethodPost, "/runs/"+id+"/suspend",
		`{"resume":"ckpt-9","wake":["config.changed"]}`); code != http.StatusOK {
		t.Fatalf("suspend: %d %s", code, b)
	}

	ws := dial(t, url, who{org: "acme"})
	seen := listen(ws)
	Publish("acme", "", "", "config.changed", nil)

	// A marker every connection of the org hears, raised after the event, so
	// what did not arrive before it was not sent rather than not yet sent.
	PublishOrg("acme", "", "probe.moved", nil)
	if got := until(t, seen, "probe.moved"); !slices.Contains(got, runWake) {
		t.Fatalf("the org was not told the run is due: %v", got)
	}

	_, body := rest(t, url, "acme", http.MethodGet, "/runs/"+id, "")
	if got := field(t, body, "wokenBy"); got != "config.changed" {
		t.Errorf("the row says it was called back by %v, want config.changed: %s", got, body)
	}
	if got := field(t, body, "status"); got != "suspended" {
		t.Errorf("a wake spent the suspension: status is %v, want suspended", got)
	}
	if got := field(t, body, "wake"); got != nil {
		t.Errorf("the reason survived the wake it caused: %v", got)
	}

	// The whole point of leaving the suspension standing: the run comes back
	// the one way runs come back, holding the token it parked with.
	code, body := rest(t, url, "acme", http.MethodPost, "/runs/"+id+"/resume", `{}`)
	if code != http.StatusOK {
		t.Fatalf("a run that was called back could not come back: %d %s", code, body)
	}
	if got := field(t, body, "resume"); got != "ckpt-9" {
		t.Errorf("resume handed back %v, want the ckpt-9 the run parked with", got)
	}
}

// A run is called back by what it named and by nothing else. The second half is
// the control: the same run, the same process, the event it did name.
func TestAParkedRunIgnoresAnEventItDidNotName(t *testing.T) {
	app := mount(t)
	const id = "run_unnamed_one"
	park(t, app, "acme", id, "config.changed")

	Publish("acme", "", "", "session.message", nil)
	_, body := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, "")
	if got := field(t, body, "wokenBy"); got != nil {
		t.Errorf("an event the run never named called it back: %v", got)
	}

	Publish("acme", "", "", "config.changed", nil)
	_, body = as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, "")
	if got := field(t, body, "wokenBy"); got != "config.changed" {
		t.Fatalf("the event the run did name called back %v; the test above proved nothing", got)
	}
}

// The org is the boundary here as everywhere else: an event raised in one
// tenant cannot reach another's run, however exactly it matches what that run is
// waiting for. The same event in the run's own org is the control.
func TestAnotherOrgsEventDoesNotCallARunBack(t *testing.T) {
	app := mount(t)
	const id = "run_tenant_one"
	park(t, app, "acme", id, "config.changed")
	announced(t, app, "other", "run_tenant_two")

	Publish("other", "", "", "config.changed", nil)
	_, body := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, "")
	if got := field(t, body, "wokenBy"); got != nil {
		t.Errorf("another org's event called this org's run back: %v", got)
	}

	Publish("acme", "", "", "config.changed", nil)
	_, body = as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, "")
	if got := field(t, body, "wokenBy"); got != "config.changed" {
		t.Fatalf("the run's own org could not call it back either (%v); the test above proved nothing", got)
	}
}

// A stopped run is not resting, so nothing calls it back. Stopping clears what
// it was waiting for, drops it from the index, and the row is re-read inside the
// wake, so no one of those three going wrong raises the dead.
//
// The reports are what is read rather than the run, because a run's view shows
// what called it back only while it is resting: asking the view about a stopped
// run would be asking the projection, which hides the field either way.
func TestAStoppedRunIsNotCalledBack(t *testing.T) {
	app := mount(t)
	const dead, live = "run_stopped_one", "run_stopped_two"
	park(t, app, "acme", dead, "config.changed")
	park(t, app, "acme", live, "config.changed")
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+dead+"/stop", `{}`); code != http.StatusOK {
		t.Fatalf("stop: %d %s", code, b)
	}

	Publish("acme", "", "", "config.changed", nil)

	_, body := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+dead, "")
	if got := field(t, body, "status"); got != "stopped" {
		t.Errorf("a stopped run is %v, want stopped: %s", got, body)
	}
	if n := called(t, app, "acme", dead); n != 0 {
		t.Errorf("a stopped run was called back %d times", n)
	}
	if n := called(t, app, "acme", live); n != 1 {
		t.Fatalf("the run that was still resting was called back %d times; the test above proved nothing", n)
	}
}

// called is how many times a run was called back, read from what it reported.
// A report is written once and outlives the run, so it answers for a run in any
// state — which a run's view, showing a wake only while the run is resting,
// does not.
func called(t *testing.T, app *zip.App, org, id string) int {
	t.Helper()
	_, body := as(t, app, org, http.MethodGet, "/v1/bot/runs/"+id+"/events", "")
	var rows []Report
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("events: %v (%s)", err, body)
	}
	n := 0
	for _, r := range rows {
		if r.Kind == "wake" {
			n++
		}
	}
	return n
}

// The claim, measured: waiting costs a row. A hundred parked runs run nothing,
// so the process holds no more goroutines than it did with one, or with none.
func TestParkedRunsCostNoGoroutines(t *testing.T) {
	app := mount(t)
	as(t, app, "acme", http.MethodGet, "/v1/bot/runs", "") // open the org's file first
	none := settled()

	park(t, app, "acme", "run_cost_0000", "config.changed")
	one := settled()

	for i := 1; i < 100; i++ {
		park(t, app, "acme", fmt.Sprintf("run_cost_%04d", i), "config.changed")
	}
	hundred := settled()

	t.Logf("goroutines: 0 parked runs %d, 1 parked run %d, 100 parked runs %d", none, one, hundred)
	if one > none {
		t.Errorf("parking one run cost %d goroutines", one-none)
	}
	if hundred > one {
		t.Errorf("parking a hundred runs cost %d goroutines beyond the first", hundred-one)
	}

	// And they were really waiting: one event calls all hundred back, which is
	// what makes the count above a measurement of sleeping runs rather than of
	// runs that were never listening.
	Publish("acme", "", "", "config.changed", nil)
	for _, id := range []string{"run_cost_0000", "run_cost_0037", "run_cost_0099"} {
		_, body := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, "")
		if got := field(t, body, "wokenBy"); got != "config.changed" {
			t.Fatalf("%s was not called back (%v); the counts above measured nothing", id, got)
		}
	}
	t.Logf("goroutines after calling a hundred runs back: %d", settled())
}

// settled is the goroutine count once whatever the last request started has
// finished. A count taken the instant a request returns still holds the
// goroutines the transport is winding down, which says nothing about what a
// parked run costs.
func settled() int {
	last := -1
	for range 100 {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n == last {
			return n
		}
		last = n
	}
	return runtime.NumGoroutine()
}

// A reason to come back is a row, and the index over those rows is a hint that
// a process is free to have none of. Emptying it is what a restart does to a
// gateway — the file keeps the reasons, the memory keeps nothing — and the first
// event an org raises afterwards is what makes it read them again.
func TestAGatewayThatRemembersNothingReadsTheReasonBack(t *testing.T) {
	app := mount(t)
	const id = "run_reread_one"
	park(t, app, "acme", id, "config.changed")

	asleep.forget()

	Publish("acme", "", "", "config.changed", nil)
	_, body := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, "")
	if got := field(t, body, "wokenBy"); got != "config.changed" {
		t.Errorf("a gateway holding no index could not find what calls the run back: %v (%s)", got, body)
	}
}

// A reason is spent when it fires. A run that asked to be called back is called
// back once, not once for every event of that name that follows.
func TestAReasonIsSpentOnce(t *testing.T) {
	app := mount(t)
	const id = "run_spent_one"
	park(t, app, "acme", id, "config.changed")

	Publish("acme", "", "", "config.changed", nil)
	Publish("acme", "", "", "config.changed", nil)
	Publish("acme", "", "", "config.changed", nil)

	if n := called(t, app, "acme", id); n != 1 {
		t.Errorf("three events called the run back %d times, want 1", n)
	}
}

// A wake says which run is due and what called it, and carries nothing of the
// event itself. An event may be addressed to the connections that asked for one
// key; a wake is addressed to the whole org, and copying one into the other
// would hand a narrowed payload to an audience its author never chose.
func TestAWakeCarriesNothingOfTheEventThatCausedIt(t *testing.T) {
	url := serve(t)
	const id = "run_quiet_one"
	if code, b := rest(t, url, "acme", http.MethodPost, "/runs",
		`{"runId":"`+id+`","where":"local"}`); code != http.StatusCreated {
		t.Fatalf("announce: %d %s", code, b)
	}
	if code, b := rest(t, url, "acme", http.MethodPost, "/runs/"+id+"/suspend",
		`{"wake":["board.changed"]}`); code != http.StatusOK {
		t.Fatalf("suspend: %d %s", code, b)
	}

	ws := dial(t, url, who{org: "acme"})
	seen := listen(ws)
	// Under a key this connection never asked for, so the event itself reaches
	// it not at all and only the wake it caused does.
	Publish("acme", "", "board:private", "board.changed", map[string]any{"note": "not-for-you"})

	deadline := time.After(10 * time.Second)
	for {
		select {
		case frame, ok := <-seen:
			if !ok {
				t.Fatal("the socket ended before the wake arrived")
			}
			raw, err := json.Marshal(frame)
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			if strings.Contains(string(raw), "not-for-you") {
				t.Fatalf("a wake carried the payload of the event that caused it: %s", raw)
			}
			if frame["event"] != runWake {
				continue
			}
			p, _ := frame["payload"].(map[string]any)
			if p["runId"] != id || p["wokenBy"] != "board.changed" {
				t.Errorf("the wake says %v, want the run and the event that called it", p)
			}
			return
		case <-deadline:
			t.Fatal("no wake arrived")
		}
	}
}

// A wake is an event like any other, so a run may wait on one: a supervisor that
// wants to know when its workers come back. The wake of the worker raises the
// wake of the supervisor on the same goroutine, inside the publish that caused
// it, which is the one place this could seize — an org's file takes one
// connection at a time, and a second write to it from inside the first would
// wait for a transaction that cannot commit until it returns. It does not,
// because the write commits before the event that follows it is raised.
func TestARunMayWaitForAnotherRunComingBack(t *testing.T) {
	app := mount(t)
	park(t, app, "acme", "run_watch_worker", "config.changed")
	park(t, app, "acme", "run_watch_super", runWake)

	done := make(chan struct{})
	go func() { defer close(done); Publish("acme", "", "", "config.changed", nil) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("publishing never returned: the wake it caused is waiting on the write that caused it")
	}

	for _, id := range []string{"run_watch_worker", "run_watch_super"} {
		if n := called(t, app, "acme", id); n != 1 {
			t.Errorf("%s was called back %d times, want 1", id, n)
		}
	}
}

// A run may name only events this surface publishes. One nothing raises is a run
// that sleeps for ever, so it is refused — and refused whole: the run is left as
// it was rather than parked with half of what it asked for.
func TestSuspendRefusesAnEventThisSurfaceDoesNotPublish(t *testing.T) {
	app := mount(t)
	const id = "run_vocab_one"
	announced(t, app, "acme", id)

	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/suspend",
		`{"wake":["config.changed","nothing.happens"]}`); code != http.StatusBadRequest {
		t.Fatalf("suspend accepted an event nothing raises: %d %s", code, b)
	}
	_, body := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, "")
	if got := field(t, body, "status"); got == "suspended" {
		t.Errorf("the refused suspension parked the run anyway: %s", body)
	}
}
