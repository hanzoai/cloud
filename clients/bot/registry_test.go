package bot

import (
	"net/http"
	"strings"
	"testing"
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
