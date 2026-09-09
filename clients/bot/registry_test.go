package bot

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// The registry: a bot announces itself, is listed, parks with what it needs to
// come back, and comes back. The helpers are in bot_test.go, with the rest of
// the surface's.

// A bot's life: it announces itself, is listed as live, says it is running,
// suspends carrying what it needs to come back, is no longer live, comes back
// with that same thing in hand, and is live again.
func TestBotLife(t *testing.T) {
	app := mount(t)

	code, body := as(t, app, "acme", http.MethodPost, "/v1/bot",
		`{"name":"scribe","where":"cloud","edition":"browser","host":"sandbox-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("announce: %d %s", code, body)
	}
	id, _ := field(t, body, "id").(string)
	if id == "" {
		t.Fatalf("announce returned no id: %s", body)
	}
	if got := field(t, body, "status"); got != "starting" {
		t.Errorf("a bot that just announced is %v, want starting", got)
	}

	if code, body = as(t, app, "acme", http.MethodPatch, "/v1/bot/"+id,
		`{"status":"running","url":"https://sandbox-1.example/bot"}`); code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", code, body)
	}

	code, body = as(t, app, "acme", http.MethodGet, "/v1/bot?live=1", "")
	if code != http.StatusOK || !strings.Contains(body, id) {
		t.Fatalf("a running bot is not live: %d %s", code, body)
	}

	if code, body = as(t, app, "acme", http.MethodPost, "/v1/bot/"+id+"/suspend",
		`{"resume":"ckpt-7","message":"idle"}`); code != http.StatusOK {
		t.Fatalf("suspend: %d %s", code, body)
	}
	if got := field(t, body, "status"); got != "suspended" {
		t.Errorf("status after suspend is %v, want suspended", got)
	}

	code, body = as(t, app, "acme", http.MethodGet, "/v1/bot?live=1", "")
	if strings.Contains(body, id) {
		t.Errorf("a suspended bot is still listed live: %s", body)
	}

	code, body = as(t, app, "acme", http.MethodPost, "/v1/bot/"+id+"/resume", `{}`)
	if code != http.StatusOK {
		t.Fatalf("resume: %d %s", code, body)
	}
	if got := field(t, body, "resume"); got != "ckpt-7" {
		t.Errorf("resume handed back %v, want the ckpt-7 it was given", got)
	}

	code, body = as(t, app, "acme", http.MethodGet, "/v1/bot?live=1", "")
	if !strings.Contains(body, id) {
		t.Errorf("a resumed bot is not live again: %s", body)
	}
}

// The resume token leaves through the resume door and no other. A bot that has
// suspended is holding something of its own, and a list of bots is not the
// place to hand it out.
func TestResumeTokenIsNotOnView(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot",
		`{"name":"scribe","where":"cloud"}`)
	id, _ := field(t, body, "id").(string)
	if _, b := as(t, app, "acme", http.MethodPost, "/v1/bot/"+id+"/suspend",
		`{"resume":"secret-token"}`); strings.Contains(b, "secret-token") {
		t.Errorf("suspend echoed the token back: %s", b)
	}
	if _, b := as(t, app, "acme", http.MethodGet, "/v1/bot/"+id, ""); strings.Contains(b, "secret-token") {
		t.Errorf("a bot's own view carries the token: %s", b)
	}
	if _, b := as(t, app, "acme", http.MethodGet, "/v1/bot", ""); strings.Contains(b, "secret-token") {
		t.Errorf("the list carries the token: %s", b)
	}
}

// One org cannot see, read or stop another's bots. The id is not the secret;
// the org is the boundary.
func TestOrgsDoNotShareABot(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"where":"local"}`)
	id, _ := field(t, body, "id").(string)

	if code, _ := as(t, app, "other", http.MethodGet, "/v1/bot/"+id, ""); code != http.StatusNotFound {
		t.Errorf("another org read the bot: %d", code)
	}
	if code, _ := as(t, app, "other", http.MethodPost, "/v1/bot/"+id+"/suspend", `{}`); code != http.StatusNotFound {
		t.Errorf("another org suspended the bot: %d", code)
	}
	if code, b := as(t, app, "other", http.MethodGet, "/v1/bot", ""); strings.Contains(b, id) {
		t.Errorf("another org listed the bot: %d %s", code, b)
	}
}

// Suspension and resumption are states, not free transitions: a stopped bot
// cannot be parked, and a running one cannot be resumed.
func TestSuspendAndResumeRefuseTheWrongState(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"where":"cloud"}`)
	id, _ := field(t, body, "id").(string)

	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/"+id+"/resume", `{}`); code != http.StatusConflict {
		t.Errorf("resumed a bot that was never suspended: %d %s", code, b)
	}
	if code, b := as(t, app, "acme", http.MethodPatch, "/v1/bot/"+id, `{"status":"ended"}`); code != http.StatusOK {
		t.Fatalf("end: %d %s", code, b)
	}
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot/"+id+"/suspend", `{}`); code != http.StatusConflict {
		t.Errorf("suspended a bot that had stopped: %d %s", code, b)
	}
}

// Suspending through the heartbeat would write a status without the token that
// makes it recoverable, so the heartbeat refuses and names the door that works.
func TestHeartbeatWillNotSuspend(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"where":"cloud"}`)
	id, _ := field(t, body, "id").(string)
	code, b := as(t, app, "acme", http.MethodPatch, "/v1/bot/"+id, `{"status":"suspended"}`)
	if code != http.StatusBadRequest {
		t.Errorf("heartbeat suspended a bot: %d %s", code, b)
	}
	if !strings.Contains(b, "/suspend") {
		t.Errorf("the refusal does not name the door that works: %s", b)
	}
}

// where is the one field with no default: a bot that does not say whether it
// runs on someone's machine or in a sandbox has not said enough to be listed,
// because the answer decides who may stop it.
func TestAnnounceRequiresWhere(t *testing.T) {
	app := mount(t)
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"name":"x"}`); code != http.StatusBadRequest {
		t.Errorf("announced without where: %d %s", code, b)
	}
	if code, b := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"where":"somewhere"}`); code != http.StatusBadRequest {
		t.Errorf("announced from somewhere: %d %s", code, b)
	}
	// edition is the one that defaults, because most bots only answer.
	code, b := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"where":"local"}`)
	if code != http.StatusCreated {
		t.Fatalf("announce: %d %s", code, b)
	}
	if got := field(t, b, "edition"); got != "plain" {
		t.Errorf("edition defaulted to %v, want plain", got)
	}
}

// A suspension and a resumption are recorded, so a console can say what a bot
// has been doing without having held a connection to it.
func TestSuspendAndResumeAreRecorded(t *testing.T) {
	app := mount(t)
	_, body := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"where":"cloud"}`)
	id, _ := field(t, body, "id").(string)
	as(t, app, "acme", http.MethodPost, "/v1/bot/"+id+"/suspend", `{"resume":"c1","message":"idle"}`)
	as(t, app, "acme", http.MethodPost, "/v1/bot/"+id+"/resume", `{}`)

	code, b := as(t, app, "acme", http.MethodGet, "/v1/bot/"+id+"/events", "")
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
// A bot has four homes: the roster row, a file of its own, coordinates in KMS,
// and the goroutines and sockets that are driving it. Forgetting it that leaves
// any of them standing hands the next bot to take the id what the last one had.

// forgotten deletes one bot through the door a console uses.
func forgotten(t *testing.T, app *zip.App, org, id string) {
	t.Helper()
	if code, body := as(t, app, org, http.MethodDelete, "/v1/bot/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("forget %s: %d %s", id, code, body)
	}
}

// The id is announceable again after a forget, and a bot that takes it starts
// with nothing: not the conversations, not the sidebar rows, not the settings.
func TestAForgottenBotLeavesNothingForTheNextOneToTakeTheId(t *testing.T) {
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
		t.Errorf("the new bot inherited %d sessions: %v", len(rows), rows)
	}
	page := aiPage(t, app, me, `{"sessionKey":"secret"}`)
	if messages, _ := page["messages"].([]any); len(messages) != 0 {
		t.Errorf("the new bot inherited the last one's conversation: %v", messages)
	}
}

// A credential is a coordinate in KMS, and the settings that name it are the
// only record there is. They go together, or the ciphertext outlives everything
// that could ever name it — and the next bot to take the id reads a credential
// it never set.
func TestAForgottenBotClearsWhatItSealed(t *testing.T) {
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
			t.Errorf("%s still holds %q after the bot was forgotten", field, got)
		}
	}
}

// A socket resolves its bot once, at the upgrade. A connection bound to a bot
// that has been forgotten is holding a binding to nothing, and goes on reading
// and writing that bot's file until it is told.
func TestForgettingABotEndsTheSocketsBoundToIt(t *testing.T) {
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
	at := "http" + strings.TrimSuffix(strings.TrimPrefix(url, "ws"), "/bot") + "/bot/livebot001"
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
			t.Fatal("a connection bound to a forgotten bot is still open")
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

	code, body := as(t, app, "acme", http.MethodPost, "/v1/bot", `{"where":"cloud"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("announce over an unopenable store answered %d %s", code, body)
	}
	for _, said := range []string{blocked, "mkdir", "OrgDB", "sqlite", "no such file"} {
		if strings.Contains(body, said) {
			t.Errorf("the refusal tells the caller %q: %s", said, body)
		}
	}
}
