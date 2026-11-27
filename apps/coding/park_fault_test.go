package coding

// park_fault_test.go pins a run the model API failed with a server error (#817): the harness
// has tried that call again itself, so the run is parked and tried again by itself, after a wait
// that doubles with each failure in a row, and never failed.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/coord"
)

func TestFaulted(t *testing.T) {
	turn := func(msg string) *client.Ran {
		b, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": msg}})
		return &client.Ran{ExitCode: 1, Stdout: string(b)}
	}
	for name, c := range map[string]struct {
		ran    *client.Ran
		fault  bool
		reason string
	}{
		"503 after the retries":      {turn("exceeded retry limit, last status: 503 Service Unavailable, request id: r1"), true, agents.ParkFault},
		"500":                        {turn("exceeded retry limit, last status: 500 Internal Server Error"), true, agents.ParkFault},
		"502 on stderr":              {&client.Ran{ExitCode: 1, Stderr: "error: exceeded retry limit, last status: 502 Bad Gateway\n"}, true, agents.ParkFault},
		"504":                        {turn("exceeded retry limit, last status: 504 Gateway Timeout"), true, agents.ParkFault},
		"a limit is a limit":         {turn("exceeded retry limit, last status: 429 Too Many Requests"), false, agents.ParkLimit},
		"a payer is a payer":         {turn("exceeded retry limit, last status: 402 Payment Required"), false, agents.ParkPayment},
		"not implemented is final":   {turn("exceeded retry limit, last status: 501 Not Implemented"), false, ""},
		"a client error is the task": {turn("exceeded retry limit, last status: 400 Bad Request"), false, ""},
		"a 503 the task printed":     {&client.Ran{ExitCode: 1, Stdout: "the server answered 503 once"}, false, ""},
		"no ran":                     {nil, false, ""},
	} {
		if got := faultOf(c.ran) != ""; got != c.fault {
			t.Errorf("%s: a fault = %v, want %v", name, got, c.fault)
		}
		if got := refusedFor(c.ran); got != c.reason {
			t.Errorf("%s: refusedFor = %q, want %q", name, got, c.reason)
		}
	}
}

// A fault is named by its status and phrase alone, so the same failure is the same name whatever
// request it was.
func TestAFaultIsNamedByWhatItWas(t *testing.T) {
	for msg, want := range map[string]string{
		"exceeded retry limit, last status: 503 Service Unavailable, request id: r1":      "503 service unavailable",
		"exceeded retry limit, last status: 503 Service Unavailable, request id: other-2": "503 service unavailable",
		"error: exceeded retry limit, last status: 502  Bad   Gateway":                    "502 bad gateway",
		"exceeded retry limit, last status: 500":                                          "500",
		"exceeded retry limit, last status: 429 Too Many Requests":                        "",
		"the server answered 503":                                                         "",
	} {
		b, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": msg}})
		if got := faultOf(&client.Ran{ExitCode: 1, Stdout: string(b)}); got != want {
			t.Errorf("faultOf(%q) = %q, want %q", msg, got, want)
		}
	}
}

// The count of a fault goes on while it is the same error and no turn answered between, and begins
// again when either changes.
func TestAFaultIsCountedWhileItIsTheSameError(t *testing.T) {
	f := Fault{}.next("503 service unavailable", false)
	f = f.next("503 service unavailable", false)
	if f != (Fault{Print: "503 service unavailable", Count: 2}) {
		t.Fatalf("two of the same error count %+v", f)
	}
	if got := f.next("502 bad gateway", false); got != (Fault{Print: "502 bad gateway", Count: 1}) {
		t.Fatalf("another error counts %+v, want it begun again", got)
	}
	if got := f.next("503 service unavailable", true); got != (Fault{Print: "503 service unavailable", Count: 1}) {
		t.Fatalf("a fault after a turn that answered counts %+v, want it begun again", got)
	}
}

// A turn that ends without error is noted on the run, and one that fails is not.
func TestATurnThatAnsweredIsNoted(t *testing.T) {
	p := &pod{}
	servePod(t, p)
	for _, code := range []int{1, 0} {
		p.answer = func([]string) client.Ran { return client.Ran{ExitCode: code} }
		answered := new(bool)
		in := sandbox{ctx: t.Context(), id: "sbx_1", ttl: 60, session: "sess_1", extraSecrets: new([]string), answered: answered}
		_, _ = in.turn(t.Context(), &client.RunIn{ID: "sbx_1", Session: "sess_1"}, []string{"dev", "exec", "--", "-"}, "go", 60)
		if *answered != (code == 0) {
			t.Fatalf("a turn that exited %d left answered = %v", code, *answered)
		}
	}
}

func TestAFaultIsSaidWithoutCredits(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 15, 0, 0, time.UTC).Unix()
	got := agents.ParkSaid(agents.ParkFault, at)
	if !strings.Contains(got, "the model API failed") || !strings.Contains(got, "2026-11-20 20:15 UTC") || strings.Contains(got, "credit") || len(got) > 120 {
		t.Fatalf("a fault says %q", got)
	}
	if got := agents.ParkSaid(agents.ParkFault, 0); !strings.Contains(got, "on Continue") || strings.Contains(got, "credit") {
		t.Fatalf("a fault with no time says %q", got)
	}
}

// A turn the model API failed with a 503 after the harness's own retries parks the run: its work
// is on its branch, its box is given back, its session says why and when it is tried again, and no
// pull request is opened. Tried again, and failed again, it waits twice as long; tried a third time
// and answered, it goes on in its session and opens the one pull request.
//
// MUTATION: let the failure end the run (the old behaviour) and there is no park, a closed session
// and no pull request; count no failures and the second wait is the first's.
func TestARunTheModelAPIFailedParksAndIsTriedAgainAfterAWaitThatDoubles(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		var turns atomic.Int32
		r := refusingRun(t, 2, &turns, "exceeded retry limit, last status: 503 Service Unavailable, request id: r1")
		app := newSessionsApp(t)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		stop := flights.watch(c)
		t.Cleanup(stop)

		before := time.Now()
		acc := parkedRun(t, r, app, theTask())
		x := app.get("hanzo", acc.SessionID)
		first := before.Add(retryAfter(1)).Unix()
		if !x.Parked() || x.Park != agents.ParkFault || x.ParkAt < first || x.ParkAt > first+3 || app.parked() != 1 {
			t.Fatalf("the session reads %+v, want one park for the fault, a first wait away (%d)", x, first)
		}
		r.mu.Lock()
		held := r.refs[acc.Branch]
		r.mu.Unlock()
		if held != newSHA || boxesGivenBack(r.pod) != 1 || len(r.pulls) != 0 || len(r.todo.inputs) != 0 {
			t.Fatalf("a run the API failed kept %v on %s, gave back %d boxes, opened %v %v:\n%s",
				held, acc.Branch, boxesGivenBack(r.pod), r.pulls, r.todo.inputs, strings.Join(r.pod.lines(), "\n"))
		}
		rows, err := l.parksOf(t.Context(), "hanzo")
		if err != nil || len(rows) != 1 || rows[0].reason != agents.ParkFault || rows[0].tries != 1 || rows[0].at != x.ParkAt {
			t.Fatalf("the park row is %+v (%v), want the fault, one failure counted, at %d", rows, err, x.ParkAt)
		}
		var parked struct{ Status, Reason, Message, Note string }
		for _, p := range eventsOf(r, kindStatus) {
			var e struct{ Status, Reason, Message, Note string }
			_ = json.Unmarshal([]byte(p), &e)
			if e.Status == statusParked {
				parked = e
			}
		}
		if parked.Reason != agents.ParkFault || parked.Message != agents.ParkSaid(agents.ParkFault, x.ParkAt) || !strings.Contains(parked.Note, "last status: 503") {
			t.Fatalf("the stream's parked event is %+v", parked)
		}
		if slices.Contains(statusesOf(r), "error") || slices.Contains(statusesOf(r), "done") {
			t.Fatalf("a parked run reported %v", statusesOf(r))
		}
		if got := armed(flights); got != x.ParkAt {
			t.Fatalf("the wake is set for %d, want the fault's %d", got, x.ParkAt)
		}

		// The wait is over and the API is still failing: parked again, for twice as long.
		before = time.Now()
		if err := flights.proceed(t.Context(), rows[0], byWake); err != nil {
			t.Fatal(err)
		}
		second := before.Add(retryAfter(2)).Unix()
		waitFor(t, "the run to be parked a second time", func() bool {
			again, _ := l.parksOf(t.Context(), "hanzo")
			return len(again) == 1 && again[0].tries == 2
		})
		letGo(t, flights, acc.SessionID)
		x = app.get("hanzo", acc.SessionID)
		if !x.Parked() || x.Park != agents.ParkFault || x.ParkAt < second || x.ParkAt > second+3 || retryAfter(2) != 2*retryAfter(1) {
			t.Fatalf("the session reads %+v, want parked for the fault again, a doubled wait away (%d)", x, second)
		}

		// The API answers: the run goes on in its session, and opens the pull request once.
		rows, _ = l.parksOf(t.Context(), "hanzo")
		if err := flights.proceed(t.Context(), rows[0], byWake); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the run to finish", func() bool {
			if slices.Contains(statusesOf(r), "error") {
				t.Fatalf("the run that went on failed: %v", eventsOf(r, kindStatus))
			}
			return slices.Contains(statusesOf(r), "done")
		})
		letGo(t, flights, acc.SessionID)
		if turns.Load() != 3 || len(r.todo.inputs) != 1 || r.todo.inputs[0].Head != acc.Branch {
			t.Fatalf("%d turns and the pull request filed %v, want three turns and one pull request for %s", turns.Load(), r.todo.inputs, acc.Branch)
		}
	})
}

// A fault that does not clear ends the run: the same error on five turns in a row is not waited
// on for a day. The run is failed, not parked again, with its work on its branch, and its last
// status carries the reason, the fault's name and the count for whoever reads it.
//
// MUTATION: raise the cap or never count and the run parks a fifth time, with no final status.
func TestARunTheModelAPIKeepsFailingIsEndedAndNamesTheFault(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		var turns atomic.Int32
		r := refusingRunBy(t, 100, &turns, func(n int) string {
			return fmt.Sprintf("exceeded retry limit, last status: 503 Service Unavailable, request id: r%d", n)
		})
		app := newSessionsApp(t)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		stop := flights.watch(c)
		t.Cleanup(stop)

		acc := parkedRun(t, r, app, theTask())
		for count := 2; count < faultCap; count++ {
			rows, _ := l.parksOf(t.Context(), "hanzo")
			if len(rows) != 1 || rows[0].spec.Fault.Count != count-1 {
				t.Fatalf("after %d faults the park holds %+v", count-1, rows)
			}
			if err := flights.proceed(t.Context(), rows[0], byWake); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the run to be parked again", func() bool {
				again, _ := l.parksOf(t.Context(), "hanzo")
				return len(again) == 1 && again[0].spec.Fault.Count == count
			})
			letGo(t, flights, acc.SessionID)
		}
		rows, _ := l.parksOf(t.Context(), "hanzo")
		if err := flights.proceed(t.Context(), rows[0], byWake); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the run to end", func() bool { return slices.Contains(statusesOf(r), "error") })
		letGo(t, flights, acc.SessionID)

		var last struct {
			Status, Error, Reason, Fingerprint, Branch string
			Faults                                     int
		}
		for _, p := range eventsOf(r, kindStatus) {
			var e struct {
				Status, Error, Reason, Fingerprint, Branch string
				Faults                                     int
			}
			_ = json.Unmarshal([]byte(p), &e)
			if e.Status == "error" {
				last = e
			}
		}
		if last.Reason != endFaults || last.Fingerprint != "503 service unavailable" || last.Faults != faultCap || last.Branch != acc.Branch {
			t.Fatalf("the last status is %+v, want %s, the fault's name, %d and the branch", last, endFaults, faultCap)
		}
		if !strings.Contains(last.Error, "5 times in a row") || !strings.Contains(last.Error, "503 service unavailable") || !strings.Contains(last.Error, acc.Branch) {
			t.Fatalf("the person is told %q", last.Error)
		}
		if turns.Load() != int32(faultCap) {
			t.Fatalf("%d turns ran, want %d and no more", turns.Load(), faultCap)
		}
		if again, _ := l.parksOf(t.Context(), "hanzo"); len(again) != 0 {
			t.Fatalf("the ended run is still parked: %+v", again)
		}
		if x := app.get("hanzo", acc.SessionID); x.Parked() {
			t.Fatalf("the ended run's session is still parked: %+v", x)
		}
		r.sessions.mu.Lock()
		closes := len(r.sessions.closes)
		r.sessions.mu.Unlock()
		if closes != 1 || len(r.pulls) != 0 {
			t.Fatalf("the session was closed %d times and %d pull requests opened, want closed once and none", closes, len(r.pulls))
		}
	})
}

// A different error begins the count again: 503, 503, 502, then 503 four times does not reach the cap,
// and the run that is answered on its eighth turn finishes.
func TestAnotherFaultBeginsTheCountAgain(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		var turns atomic.Int32
		r := refusingRunBy(t, 7, &turns, func(n int) string {
			if n == 3 {
				return "exceeded retry limit, last status: 502 Bad Gateway, request id: r3"
			}
			return "exceeded retry limit, last status: 503 Service Unavailable, request id: r" + strconv.Itoa(n)
		})
		app := newSessionsApp(t)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		stop := flights.watch(c)
		t.Cleanup(stop)

		acc := parkedRun(t, r, app, theTask())
		want := []Fault{{"503 service unavailable", 1}, {"503 service unavailable", 2}, {"502 bad gateway", 1}, {"503 service unavailable", 1},
			{"503 service unavailable", 2}, {"503 service unavailable", 3}, {"503 service unavailable", 4}}
		for i, f := range want {
			var rows []park
			waitFor(t, "the run to be parked", func() bool {
				rows, _ = l.parksOf(t.Context(), "hanzo")
				return len(rows) == 1 && rows[0].spec.Fault == f
			})
			if i == len(want)-1 {
				break
			}
			letGo(t, flights, acc.SessionID)
			if err := flights.proceed(t.Context(), rows[0], byWake); err != nil {
				t.Fatal(err)
			}
		}
		letGo(t, flights, acc.SessionID)
		rows, _ := l.parksOf(t.Context(), "hanzo")
		if err := flights.proceed(t.Context(), rows[0], byWake); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the run to finish", func() bool {
			if slices.Contains(statusesOf(r), "error") {
				t.Fatalf("the run was ended: %v", eventsOf(r, kindStatus))
			}
			return slices.Contains(statusesOf(r), "done")
		})
		letGo(t, flights, acc.SessionID)
		if turns.Load() != 8 || len(r.todo.inputs) != 1 {
			t.Fatalf("%d turns and the pull request filed %v, want eight and one", turns.Load(), r.todo.inputs)
		}
	})
}
