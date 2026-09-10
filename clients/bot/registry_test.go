package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// The registry: a run announces itself, is listed, parks with what it needs to
// come back, and comes back. The helpers are in bot_test.go, with the rest of
// the surface's.

// A run's life: it announces itself, is listed as live, says it is running,
// suspends carrying what it needs to come back, is no longer live, comes back
// with that same thing in hand, and is live again.
func TestRunLife(t *testing.T) {
	app := mount(t)

	code, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs",
		`{"name":"scribe","where":"cloud","surface":"browser","host":"sandbox-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("announce: %d %s", code, body)
	}
	id, _ := field(t, body, "runId").(string)
	if id == "" {
		t.Fatalf("announce returned no id: %s", body)
	}
	if got := field(t, body, "status"); got != "starting" {
		t.Errorf("a run that just announced is %v, want starting", got)
	}

	if code, body = as(t, app, "acme", http.MethodPatch, "/v1/bot/runs/"+id,
		`{"status":"running","sessionUrl":"https://sandbox-1.example/bot"}`); code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", code, body)
	}

	code, body = as(t, app, "acme", http.MethodGet, "/v1/bot/runs?live=1", "")
	if code != http.StatusOK || !strings.Contains(body, id) {
		t.Fatalf("a running run is not live: %d %s", code, body)
	}

	if code, body = as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/suspend",
		`{"resume":"ckpt-7","message":"idle"}`); code != http.StatusOK {
		t.Fatalf("suspend: %d %s", code, body)
	}
	if got := field(t, body, "status"); got != "suspended" {
		t.Errorf("status after suspend is %v, want suspended", got)
	}

	code, body = as(t, app, "acme", http.MethodGet, "/v1/bot/runs?live=1", "")
	if strings.Contains(body, id) {
		t.Errorf("a suspended run is still listed live: %s", body)
	}

	code, body = as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/resume", `{}`)
	if code != http.StatusOK {
		t.Fatalf("resume: %d %s", code, body)
	}
	if got := field(t, body, "resume"); got != "ckpt-7" {
		t.Errorf("resume handed back %v, want the ckpt-7 it was given", got)
	}

	code, body = as(t, app, "acme", http.MethodGet, "/v1/bot/runs?live=1", "")
	if !strings.Contains(body, id) {
		t.Errorf("a resumed run is not live again: %s", body)
	}
}

// The resume token leaves through the resume door and no other. A run that has
// suspended is holding something of its own, and a list of runs is not the
// place to hand it out.
func TestResumeTokenIsNotOnView(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs",
		`{"name":"scribe","where":"cloud"}`)
	id, _ := field(t, body, "runId").(string)
	if _, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/suspend",
		`{"resume":"secret-token"}`); strings.Contains(b, "secret-token") {
		t.Errorf("suspend echoed the token back: %s", b)
	}
	if _, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id, ""); strings.Contains(b, "secret-token") {
		t.Errorf("a run's own view carries the token: %s", b)
	}
	if _, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs", ""); strings.Contains(b, "secret-token") {
		t.Errorf("the list carries the token: %s", b)
	}
}

// One org cannot see, read or stop another's runs. The id is not the secret;
// the org is the boundary.
func TestOrgsDoNotShareARun(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local"}`)
	id, _ := field(t, body, "runId").(string)

	if code, _ := as(t, app, "other", http.MethodGet, "/v1/bot/runs/"+id, ""); code != http.StatusNotFound {
		t.Errorf("another org read the run: %d", code)
	}
	if code, _ := as(t, app, "other", http.MethodPost, "/v1/bot/runs/"+id+"/suspend", `{}`); code != http.StatusNotFound {
		t.Errorf("another org suspended the run: %d", code)
	}
	if code, _ := as(t, app, "other", http.MethodPost, "/v1/bot/runs/"+id+"/stop", `{}`); code != http.StatusNotFound {
		t.Errorf("another org stopped the run: %d", code)
	}
	if code, b := as(t, app, "other", http.MethodGet, "/v1/bot/runs", ""); strings.Contains(b, id) {
		t.Errorf("another org listed the run: %d %s", code, b)
	}
}

// Suspension and resumption are states, not free transitions: a stopped run
// cannot be parked, and a running one cannot be resumed.
func TestSuspendAndResumeRefuseTheWrongState(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"cloud"}`)
	id, _ := field(t, body, "runId").(string)

	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/resume", `{}`); code != http.StatusConflict {
		t.Errorf("resumed a run that was never suspended: %d %s", code, b)
	}
	code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/stop", `{}`)
	if code != http.StatusOK {
		t.Fatalf("stop: %d %s", code, b)
	}
	if got := field(t, b, "status"); got != "stopped" {
		t.Errorf("stop reported %v, want stopped", got)
	}
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/suspend", `{}`); code != http.StatusConflict {
		t.Errorf("suspended a run that had stopped: %d %s", code, b)
	}
}

// Parking a run and stopping one each write more than a status — a token in one
// direction, the end of the work in flight in the other — so the heartbeat
// refuses both and names the door that works. Stopping through the heartbeat is
// how a second stop would appear, and there is one.
func TestHeartbeatWillNotSuspendOrStop(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"cloud"}`)
	id, _ := field(t, body, "runId").(string)
	for status, door := range map[string]string{"suspended": "/suspend", "stopped": "/stop"} {
		code, b := as(t, app, "acme", http.MethodPatch, "/v1/bot/runs/"+id, `{"status":"`+status+`"}`)
		if code != http.StatusBadRequest {
			t.Errorf("heartbeat set %s: %d %s", status, code, b)
		}
		if !strings.Contains(b, door) {
			t.Errorf("the refusal for %s does not name the door that works: %s", status, b)
		}
	}
}

// where is the one field with no default: a run that describes itself without
// saying whether it is on someone's machine or in a sandbox has not said enough
// to be listed, because the answer decides who may stop it.
func TestARunMustSayWhereItIs(t *testing.T) {
	app := mount(t)
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"name":"x"}`); code != http.StatusBadRequest {
		t.Errorf("began without where: %d %s", code, b)
	}
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"somewhere"}`); code != http.StatusBadRequest {
		t.Errorf("began from somewhere: %d %s", code, b)
	}
	// surface is the one that defaults, because most runs only answer.
	code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local"}`)
	if code != http.StatusCreated {
		t.Fatalf("begin: %d %s", code, b)
	}
	if got := field(t, b, "surface"); got != "plain" {
		t.Errorf("surface defaulted to %v, want plain", got)
	}
}

// A suspension and a resumption are recorded, so a console can say what a run
// has been doing without having held a connection to it.
func TestSuspendAndResumeAreRecorded(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"cloud"}`)
	id, _ := field(t, body, "runId").(string)
	as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/suspend", `{"resume":"c1","message":"idle"}`)
	as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/resume", `{}`)

	code, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id+"/events", "")
	if code != http.StatusOK {
		t.Fatalf("events: %d %s", code, b)
	}
	for _, want := range []string{"suspend", "resume", "idle"} {
		if !strings.Contains(b, want) {
			t.Errorf("the log does not mention %q: %s", want, b)
		}
	}
}

// ---- forgetting ----
//
// A run has four homes: the roster row, a file of its own, coordinates in KMS,
// and the goroutines and sockets that are driving it. Forgetting it that leaves
// any of them standing hands the next run to take the id what the last one had.

// forgotten deletes one run through the door a console uses.
func forgotten(t *testing.T, app *zip.App, org, id string) {
	t.Helper()
	if code, body := as(t, app, org, http.MethodDelete, "/v1/bot/runs/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("forget %s: %d %s", id, code, body)
	}
}

// The id is announceable again after a forget, and a run that takes it starts
// with nothing: not the conversations, not the sidebar rows, not the settings.
func TestAForgottenRunLeavesNothingForTheNextOneToTakeTheId(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "an answer"}, "m"))
	announced(t, app, "acme", "sharedbot1")
	me := who{org: "acme", bot: "sharedbot1"}

	if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"secret"}`); frame["ok"] != true {
		t.Fatalf("create: %v", frame)
	}
	aiSend(t, app, me, "secret", "run-1", "the previous tenant's question")
	aiSettled(t, app, me, "secret", 2)

	forgotten(t, app, "acme", "sharedbot1")
	announced(t, app, "acme", "sharedbot1")

	_, frame := ask(t, app, me, "2:a", "sessions.list", `{}`)
	if frame["ok"] != true {
		t.Fatalf("sessions.list: %v", frame)
	}
	if rows, _ := payload(t, frame)["sessions"].([]any); len(rows) != 0 {
		t.Errorf("the new run inherited %d sessions: %v", len(rows), rows)
	}
	page := aiPage(t, app, me, `{"sessionKey":"secret"}`)
	if messages, _ := page["messages"].([]any); len(messages) != 0 {
		t.Errorf("the new run inherited the last one's conversation: %v", messages)
	}
}

// A credential is a coordinate in KMS, and the settings that name it are the
// only record there is. They go together, or the ciphertext outlives everything
// that could ever name it — and the next run to take the id reads a credential
// it never set.
func TestAForgottenRunClearsWhatItSealed(t *testing.T) {
	app, vault := vaulted(t)
	announced(t, app, "acme", "sealedbot1")
	me := who{org: "acme", admin: true, bot: "sealedbot1"}

	if _, frame := ask(t, app, me, "1:a", "skills.update",
		`{"skillKey":"stripe","apiKey":"sk-previous-tenant","env":{"STRIPE_TOKEN":"tok-1"}}`); frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}
	const at = "orgs/acme/bots/sealedbot1/bot/skills/stripe/"
	for _, field := range []string{"apiKey", "env/STRIPE_TOKEN"} {
		if got, ok := heldSecret(t, vault, at+field); got == "" || !ok {
			t.Fatalf("%s was never sealed (%q, present=%v)", field, got, ok)
		}
	}

	forgotten(t, app, "acme", "sealedbot1")

	for _, field := range []string{"apiKey", "env/STRIPE_TOKEN"} {
		if got, _ := heldSecret(t, vault, at+field); got != "" {
			t.Errorf("%s still holds %q after the run was forgotten", field, got)
		}
	}
}

// A socket resolves its run once, at the upgrade. A connection bound to a run
// that has been forgotten is holding a binding to nothing, and goes on reading
// and writing that run's file until it is told.
func TestForgettingARunEndsTheSocketsBoundToIt(t *testing.T) {
	url := serve(t)
	announceAt(t, url, "acme", "livebot001")
	ws := dialAs(t, url, "acme", "op@acme", "livebot001", false)
	if frame := reply(t, ws, "1:w", "probe.write", `{"key":"k","value":"bound"}`); frame["ok"] != true {
		t.Fatalf("probe.write: %v", frame)
	}

	// The last read left a deadline on the socket, and a deadline is terminal
	// for this client: what is being waited for here is the gateway ending the
	// connection, not a read timing out.
	if err := ws.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	seen := listen(ws)
	at := "http" + strings.TrimSuffix(strings.TrimPrefix(url, "ws"), "/bot") + "/bot/runs/livebot001"
	req, err := http.NewRequest(http.MethodDelete, at, nil)
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "op@acme")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("forget answered %d", res.StatusCode)
	}

	until(t, seen, "shutdown")
	// And the notice is followed by the end of the connection rather than by a
	// socket that goes on answering. listen owns the reads, so the socket
	// ending is its channel closing.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case frame, open := <-seen:
			if !open {
				return
			}
			t.Logf("after the notice the socket was still sent %v", frame["event"])
		case <-deadline:
			t.Fatal("a connection bound to a forgotten run is still open")
		}
	}
}

// What went wrong opening a file is this deployment's business: a path on the
// server's disk, the shape of its storage layer, the driver underneath. A
// caller is told the store could not be opened, and the detail goes to the log.
func TestAStoreThatCannotOpenAnswersWithoutTheDetail(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", blocked, err)
	}
	app := mount(t, func(d *cloud.Deps) { d.DataDir = blocked })

	code, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"cloud"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("begin over an unopenable store answered %d %s", code, body)
	}
	for _, said := range []string{blocked, "mkdir", "OrgDB", "sqlite", "no such file"} {
		if strings.Contains(body, said) {
			t.Errorf("the refusal tells the caller %q: %s", said, body)
		}
	}
}

// ---- runs this cloud did not start ----

// standIn is an executor of runs, for the tests that need one. It holds a fixed
// set, records what it was asked to stop, and can fail the way a real one does.
type standIn struct {
	has     []types.Run
	err     error
	absent  bool // Stop answers ErrNoRun: this org has no such run
	stopped []string
}

func (x *standIn) Runs(context.Context, string) ([]types.Run, error) {
	return x.has, x.err
}

func (x *standIn) Stop(_ context.Context, _, run string) error {
	if x.err != nil {
		return x.err
	}
	if x.absent {
		return types.ErrNoRun
	}
	x.stopped = append(x.stopped, run)
	return nil
}

func places(x *standIn) func(*cloud.Deps) {
	return func(d *cloud.Deps) { d.Runs = x }
}

// One roster answers for both halves: the runs that announced themselves here
// and the runs an executor placed. A person asking what their bots are doing
// asks once, and neither half is a different address.
func TestTheRosterHoldsBothHalves(t *testing.T) {
	ex := &standIn{has: []types.Run{{
		ID: "placed-1", Task: "book the flight", Surface: "computer",
		Status: "running", SessionURL: "https://vnc.example/placed-1",
		StartedAt: "2026-09-09T10:00:00Z",
	}}}
	app := mount(t, places(ex))

	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local","host":"laptop"}`)
	mine, _ := field(t, body, "runId").(string)

	code, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs", "")
	if code != http.StatusOK {
		t.Fatalf("roster: %d %s", code, b)
	}
	var out struct {
		Bots []map[string]any `json:"bots"`
	}
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatalf("the roster is not {bots:[...]}: %v (%s)", err, b)
	}
	where := map[string]string{}
	for _, r := range out.Bots {
		id, _ := r["runId"].(string)
		where[id], _ = r["where"].(string)
	}
	if where[mine] != "local" {
		t.Errorf("the run announced here is %q, want local: %s", where[mine], b)
	}
	if where["placed-1"] != "cloud" {
		t.Errorf("the run the executor placed is %q, want cloud: %s", where["placed-1"], b)
	}
	if !strings.Contains(b, "book the flight") {
		t.Errorf("the executor's task did not survive the roster: %s", b)
	}
}

// A run the executor placed reads back one at a time too. The executor serves
// no read of a single run, so that answer comes out of the roster it does
// serve — the same run, not a second story about it.
func TestOneRunTheExecutorPlacedReadsBack(t *testing.T) {
	ex := &standIn{has: []types.Run{{ID: "placed-1", StartedAt: "2026-09-09T10:00:00Z"}}}
	app := mount(t, places(ex))

	code, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/placed-1", "")
	if code != http.StatusOK {
		t.Fatalf("read a placed run: %d %s", code, b)
	}
	// An executor that names no status of its own has a run that is running.
	if got := field(t, b, "status"); got != "running" {
		t.Errorf("status is %v, want running", got)
	}
	if code, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/placed-9", ""); code != http.StatusNotFound {
		t.Errorf("an id neither half holds answered %d %s", code, b)
	}
}

// Stopping is one door for both halves. A run this registry holds ends here; a
// run the executor holds ends there, and it is the executor that is asked.
func TestStopReachesWhicheverHalfHoldsTheRun(t *testing.T) {
	ex := &standIn{has: []types.Run{{ID: "placed-1", StartedAt: "2026-09-09T10:00:00Z"}}}
	app := mount(t, places(ex))

	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local"}`)
	mine, _ := field(t, body, "runId").(string)

	code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+mine+"/stop", `{"message":"done"}`)
	if code != http.StatusOK || field(t, b, "status") != "stopped" {
		t.Fatalf("stop a run held here: %d %s", code, b)
	}
	if len(ex.stopped) != 0 {
		t.Errorf("a run held here was sent to the executor: %v", ex.stopped)
	}

	code, b = as(t, app, "acme", http.MethodPost, "/v1/bot/runs/placed-1/stop", `{}`)
	if code != http.StatusOK || field(t, b, "status") != "stopped" {
		t.Fatalf("stop a placed run: %d %s", code, b)
	}
	if len(ex.stopped) != 1 || ex.stopped[0] != "placed-1" {
		t.Errorf("the executor was asked to stop %v, want [placed-1]", ex.stopped)
	}
}

// An executor that cannot answer is a failure and not an empty list. "This org
// has no runs" and "we could not ask" are different claims, and a console that
// read the second as the first would show an empty page over live work.
func TestAnExecutorThatCannotAnswerIsNotAnEmptyRoster(t *testing.T) {
	ex := &standIn{err: errors.New("dial tcp: connection refused")}
	app := mount(t, places(ex))

	code, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs", "")
	if code != http.StatusBadGateway {
		t.Errorf("a roster over a silent executor answered %d %s", code, b)
	}
	if strings.Contains(b, "connection refused") {
		t.Errorf("the refusal tells the caller how this deployment is wired: %s", b)
	}
	// And a stop it did not answer is not a stop: reporting one on that basis
	// would be a stop that cannot fail.
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/placed-1/stop", `{}`); code != http.StatusBadGateway {
		t.Errorf("a stop the executor never answered reported %d %s", code, b)
	}
}

// Absence is honoured only when the executor says so, and it is the same answer
// an id that never existed gets — so this address tells nobody which ids
// another tenant holds.
func TestARunNeitherHalfHoldsIsAbsent(t *testing.T) {
	app := mount(t, places(&standIn{absent: true}))
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/placed-9/stop", `{}`); code != http.StatusNotFound {
		t.Errorf("stopping a run nobody holds answered %d %s", code, b)
	}
	// And with no executor at all there is nowhere else to look.
	plain := mount(t)
	if code, b := as(t, plain, "acme", http.MethodPost, "/v1/bot/runs/placed-9/stop", `{}`); code != http.StatusNotFound {
		t.Errorf("stopping a run with no executor answered %d %s", code, b)
	}
}

// Asking this cloud to start a run is refused, and the refusal names what is
// absent. Nothing here places a sandbox, so a run that has not started cannot
// be made to start — and a plausible answer would be an id nobody is carrying
// out, pointed at a machine that was never found.
func TestStartingARunIsRefusedAndSaysWhatIsMissing(t *testing.T) {
	app := mount(t, places(&standIn{}))
	for _, body := range []string{"", "{}", `{"task":"book the flight"}`, `{"task":"x","surface":"computer"}`} {
		code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", body)
		if code != http.StatusNotImplemented {
			t.Fatalf("%q answered %d %s, want 501", body, code, b)
		}
		for _, said := range []string{"sandbox", "where"} {
			if !strings.Contains(b, said) {
				t.Errorf("%q was refused without mentioning %q: %s", body, said, b)
			}
		}
	}
	// Nothing is minted by a refusal: the roster is still empty afterwards.
	_, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs", "")
	if !strings.Contains(b, `{"bots":[]}`) {
		t.Errorf("a refused start left something behind: %s", b)
	}
}

// Stopping twice is stopping once, and a run that failed on its own still reads
// as failed afterwards: the run's own last word about itself is not overwritten
// by somebody asking it to stop.
func TestStoppingIsSettledOnce(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local"}`)
	id, _ := field(t, body, "runId").(string)

	for range 2 {
		if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/stop", `{}`); code != http.StatusOK {
			t.Fatalf("stop: %d %s", code, b)
		}
	}

	_, body = as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local"}`)
	failed, _ := field(t, body, "runId").(string)
	if code, b := as(t, app, "acme", http.MethodPatch, "/v1/bot/runs/"+failed, `{"status":"error"}`); code != http.StatusOK {
		t.Fatalf("a run reporting its own failure: %d %s", code, b)
	}
	code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+failed+"/stop", `{}`)
	if code != http.StatusOK {
		t.Fatalf("stop a failed run: %d %s", code, b)
	}
	if got := field(t, b, "status"); got != "error" {
		t.Errorf("stopping a failed run reported %v, want the error it ended with", got)
	}
}

// A stopped run's account of itself outlives it. Stopping ends the work; it is
// forgetting that disposes of what the run held, and the two are not one act.
func TestAStoppedRunKeepsWhatItReported(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local"}`)
	id, _ := field(t, body, "runId").(string)
	as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/events", `{"kind":"log","message":"halfway"}`)
	as(t, app, "acme", http.MethodPost, "/v1/bot/runs/"+id+"/stop", `{"message":"the work is done"}`)

	code, b := as(t, app, "acme", http.MethodGet, "/v1/bot/runs/"+id+"/events", "")
	if code != http.StatusOK {
		t.Fatalf("events: %d %s", code, b)
	}
	for _, want := range []string{"halfway", "the work is done"} {
		if !strings.Contains(b, want) {
			t.Errorf("a stopped run's log does not mention %q: %s", want, b)
		}
	}
}

// The published stop takes no input, and the client generated from it sends
// none: no body, and no content type saying what a body would have been. A
// surface that needed one would answer a refusal to every caller of that
// client, so this is the request shape that has to work.
func TestTheDoorsAnswerARequestWithNoBodyAtAll(t *testing.T) {
	app := mount(t)
	_, created := as(t, app, "acme", http.MethodPost, "/v1/bot/runs", `{"where":"local"}`)
	id, _ := field(t, created, "runId").(string)

	bare := func(method, path string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "u@acme")
		res, err := app.Fiber().Test(req)
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

	if code, b := bare(http.MethodPost, "/v1/bot/runs/"+id+"/suspend"); code != http.StatusOK {
		t.Errorf("a bodiless suspend answered %d %s", code, b)
	}
	if code, b := bare(http.MethodPost, "/v1/bot/runs/"+id+"/resume"); code != http.StatusOK {
		t.Errorf("a bodiless resume answered %d %s", code, b)
	}
	if code, b := bare(http.MethodPatch, "/v1/bot/runs/"+id); code != http.StatusOK {
		t.Errorf("a bodiless heartbeat answered %d %s", code, b)
	}
	code, b := bare(http.MethodPost, "/v1/bot/runs/"+id+"/stop")
	if code != http.StatusOK || field(t, b, "status") != "stopped" {
		t.Fatalf("a bodiless stop answered %d %s", code, b)
	}
	// And the one the published surface answers with no input of its own: a
	// caller asking this cloud to start a run, saying nothing else.
	if code, b := bare(http.MethodPost, "/v1/bot/runs"); code != http.StatusNotImplemented {
		t.Errorf("a bodiless start answered %d %s, want 501", code, b)
	}
}
