package bot

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestMain hands cek a throwaway master key. The store opens through cek,
// which refuses to open a data plane unencrypted, so these tests run the REAL
// encrypted path — the same code a deployment runs, not a way around it.
func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Exit(m.Run())
}

// mount builds the subsystem over a fresh directory exactly as a deployment
// does, so what the tests below drive is the mounted surface rather than the
// handlers called out of band.
func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "bot-test", DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.NewWriter(io.Discard), DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// as issues one request carrying a validated caller in org. Both headers are
// set: an org with no user is the anonymous forge and scopes to nothing, so a
// test stating only the org would prove nothing about orgs.
func as(t *testing.T, app *zip.App, org, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u@"+org)
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

func field(t *testing.T, body, name string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, body)
	}
	return m[name]
}

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

// A caller with no validated org is refused before anything is read or written.
func TestUnvalidatedCallerIsRefused(t *testing.T) {
	app := mount(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/bot", nil)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET /v1/bot: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("an unvalidated caller got %d, want 403", res.StatusCode)
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
