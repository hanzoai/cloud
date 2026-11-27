package dev

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
)

func mount(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Routes(app); err != nil {
		t.Fatalf("Routes: %v", err)
	}
	return app
}

func as(t *testing.T, app *zip.App, user, method, path, body string) (int, []byte) {
	t.Helper()
	return asWith(t, app, user, method, path, body, nil)
}

// asWith is as, with the request carrying headers the boundary would also have
// minted for this person (the billing account, say).
func asWith(t *testing.T, app *zip.App, user, method, path, body string, minted map[string]string) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest(method, "http://api.hanzo.ai"+path, strings.NewReader(body))
	r.Header.Set("X-User-Id", user)
	r.Header.Set("X-Org-Id", "hanzo")
	r.Header.Set("X-User-Owner", "hanzo")
	r.Header.Set("X-User-Name", strings.TrimPrefix(user, "u_"))
	for k, v := range minted {
		r.Header.Set(k, v)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func open(t *testing.T, app *zip.App, user, body string) Session {
	t.Helper()
	code, b := as(t, app, user, http.MethodPost, "/v1/dev/sessions", body)
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/dev/sessions = %d %s", code, b)
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return s
}

func TestHealth(t *testing.T) {
	app := mount(t)
	if code, b := as(t, app, "u_alice", http.MethodGet, "/v1/dev/health", ""); code != http.StatusOK || !strings.Contains(string(b), `"ok"`) {
		t.Fatalf("health = %d %s", code, b)
	}
}

// A session is the runtime a Dev front end resolves, and the dock's sandbox is the
// cell itself.
func TestASessionIsTheRuntimeTheFrontEndResolves(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	app := mount(t)
	s := open(t, app, "u_alice", "")
	if s.ID == "" || s.Sandbox != s.ID || s.Label == "" {
		t.Fatalf("session %+v: id, label and the dock's sandbox are all owed", s)
	}
	if want := "https://zap.hanzo.ai/v1/dev/zap/" + s.ID; s.Zap != want {
		t.Errorf("zap = %q, want %q", s.Zap, want)
	}
	started := false
	for _, l := range p.Lines() {
		started = started || strings.Contains(l, "hanzo-dev serve --host 127.0.0.1 --port "+Port)
	}
	if !started {
		t.Error("the Dev server was never started in the cell")
	}
}

// A cell is leased as the person and as whoever pays for them: the username, home org
// and signed billing account the request was validated with reach the sandbox, which
// resolves the wallet from them. With the subject alone the lease was charged to
// "hanzo/<subject>", a wallet nobody funds, and refused 402 while the account the same
// token pays from over HTTP held money (#907).
func TestALeaseNamesWhoPaysForIt(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	var leased []zip.Caller
	p.LeasedBy = func(who zip.Caller) { leased = append(leased, who) }
	app := mount(t)
	code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", "", map[string]string{zip.HeaderAccount: "org:hanzo"})
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/dev/sessions = %d %s", code, b)
	}
	if len(leased) != 1 {
		t.Fatalf("%d leases, want one", len(leased))
	}
	who := leased[0]
	if who.Org != "hanzo" || who.User != "u_alice" || who.Name != "alice" || who.Owner != "hanzo" || who.Account != "org:hanzo" {
		t.Errorf("the lease was taken as %+v; want the person, their name, home org and billing account", who)
	}
	if got := meter.PayerFrom(zip.WithCaller(context.Background(), who)); got != account.Org("hanzo") {
		t.Errorf("the lease is paid from %v, want the account the token names (org:hanzo)", got)
	}
}

// A Dev server that does not start is answered 503 with the cell's reason, and a cell
// the call leased for it is ended, since the caller gets no id to end it with. A cell
// the person already held is left as it was.
func TestACellWhoseDevDoesNotStartIsGivenBack(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	app := mount(t)
	held := open(t, app, "u_alice", "")
	p.Other = func(_ string, argv []string) client.Ran {
		if len(argv) > 2 && strings.Contains(argv[2], "hanzo-dev serve") {
			return client.Ran{ExitCode: 127, Stderr: "hanzo-dev is not installed in this cell"}
		}
		return client.Ran{}
	}
	code, b := as(t, app, "u_bob", http.MethodPost, "/v1/dev/sessions", "")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(b), "hanzo-dev is not installed") {
		t.Errorf("a cell whose Dev did not start = %d %s, want 503 with its reason", code, b)
	}
	if p.Live() != 1 {
		t.Errorf("%d cells leased, want only the one alice held", p.Live())
	}
	if code, _ := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", ""); code != http.StatusServiceUnavailable {
		t.Errorf("alice's held cell answered %d, want the same 503", code)
	}
	if p.Pod(held.ID) == nil {
		t.Error("a cell the person already held was ended because Dev did not start in it")
	}
}

// The cell's Dev server checks every bearer on its stream itself, against one issuer,
// so it is started for the issuer of the token the person signed in with. Left on its
// hanzo.ai default it refused every hanzo.id-signed stream with an empty 401 (#907).
func TestACellsDevChecksTheIssuerThePersonSignedInWith(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	var started []string
	p.Other = func(_ string, argv []string) client.Ran {
		if len(argv) > 2 && strings.Contains(argv[2], "hanzo-dev serve") {
			started = argv
		}
		return client.Ran{}
	}
	app := mount(t)
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://hanzo.id","aud":["admin-cli"],"sub":"u_alice"}`))
	bearer := "Bearer eyJhbGciOiJSUzI1NiJ9." + claims + ".c2lnbmF0dXJl"
	code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", "", map[string]string{"Authorization": bearer})
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/dev/sessions = %d %s", code, b)
	}
	if len(started) != 7 || started[4] != "hanzo/alice" || started[5] != "https://hanzo.id" || started[6] != "admin-cli" {
		t.Fatalf("Dev was started with %q; want the person, issuer, and audience of their token", started[min(len(started), 3):])
	}
	if !strings.Contains(started[2], `${2:+"HANZO_DEV_IAM_ISSUER=$2"}`) {
		t.Error("the start script does not hand Dev the issuer it was given")
	}
	if !strings.Contains(started[2], `${3:+"HANZO_DEV_IAM_CLIENT_ID=$3"}`) {
		t.Error("the start script does not hand Dev the audience (client id) it was given")
	}
}

// One cell per person and thread: asking again answers the cell already held, and
// nobody else's.
func TestACellIsPerPersonAndThread(t *testing.T) {
	planetest.ServeSandboxes(t)
	app := mount(t)
	a := open(t, app, "u_alice", "")
	if again := open(t, app, "u_alice", ""); again.ID != a.ID {
		t.Errorf("the same person was given a second cell: %s then %s", a.ID, again.ID)
	}
	if b := open(t, app, "u_bob", ""); b.ID == a.ID {
		t.Error("two people were given one cell")
	}
	if th := open(t, app, "u_alice", `{"thread":"t1"}`); th.ID == a.ID {
		t.Error("a thread shared the person's own cell")
	}
}

func TestEndingASessionGivesTheCellBack(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	app := mount(t)
	s := open(t, app, "u_alice", "")
	if code, b := as(t, app, "u_alice", http.MethodDelete, "/v1/dev/sessions/"+s.ID, ""); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", code, b)
	}
	if p.Live() != 0 {
		t.Errorf("%d cells still leased", p.Live())
	}
	if code, _ := as(t, app, "u_alice", http.MethodDelete, "/v1/dev/sessions/"+s.ID, ""); code >= 300 {
		t.Errorf("ending an ended cell = %d, want it to be already done", code)
	}
}

func TestACredentialWithNoPersonHoldsNoCell(t *testing.T) {
	planetest.ServeSandboxes(t)
	app := mount(t)
	r := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/dev/sessions", nil)
	resp, err := app.Test(r, zip.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 400 {
		t.Errorf("an anonymous POST = %d, want a refusal", resp.StatusCode)
	}
}

func TestARuntimeThatIsNotHereIsRefusedByName(t *testing.T) {
	planetest.ServeSandboxes(t)
	app := mount(t)
	for runtime, want := range map[string]int{
		"": http.StatusCreated, "sandbox": http.StatusCreated, "auto": http.StatusCreated,
		"wasm": http.StatusNotImplemented, "source": http.StatusBadRequest, "docker": http.StatusBadRequest,
	} {
		body := ""
		if runtime != "" {
			body = `{"runtime":"` + runtime + `"}`
		}
		if code, b := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", body); code != want {
			t.Errorf("runtime %q = %d %s, want %d", runtime, code, b, want)
		}
	}
}

func TestExecutionCellKeyAndDesktop(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	app := mount(t)

	// Explicit cell execution leases a cloud cell and echoes execution.via=cell.
	cell := open(t, app, "u_alice", `{"execution":{"via":"cell"}}`)
	if cell.Sandbox == "" || cell.Execution == nil || cell.Execution.Via != "cell" {
		t.Fatalf("cell execution: %+v", cell)
	}

	// Key execution requires account and echoes execution.
	if code, _ := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", `{"execution":{"via":"key"}}`); code != http.StatusBadRequest {
		t.Fatalf("via=key without account: want 400, got %d", code)
	}
	keyed := open(t, app, "u_alice", `{"execution":{"via":"key","account":"acc_123"}}`)
	if keyed.Sandbox == "" || keyed.Execution == nil || keyed.Execution.Via != "key" || keyed.Execution.Account != "acc_123" {
		t.Fatalf("key execution: %+v", keyed)
	}

	// Desktop execution calls source Dispatch over the plane, returns the desktop's
	// ZAP endpoint + lease, and never leases a cloud cell.
	origDispatch, origRelease := dispatchSource, releaseSource
	t.Cleanup(func() { dispatchSource, releaseSource = origDispatch, origRelease })

	var gotOrg, gotSub, gotID, gotProv, gotInst string
	dispatchSource = func(_ context.Context, org, sub, id, prov, inst string) (*client.SourceRouteOut, error) {
		gotOrg, gotSub, gotID, gotProv, gotInst = org, sub, id, prov, inst
		return &client.SourceRouteOut{
			Zap:      "https://mac.example/zap",
			Provider: "claude",
			Instance: "claude-1",
			Lease:    "lse_abc",
			Expires:  "2026-11-21T10:15:00Z",
		}, nil
	}
	var releasedLease string
	releaseSource = func(_ context.Context, org, sub, id, lease string) error {
		if org != "hanzo" || sub != "u_alice" || id != "src_0123456789abcdef0123456789abcdef" {
			t.Errorf("unexpected release args: %s %s %s", org, sub, id)
		}
		releasedLease = lease
		return nil
	}

	liveBefore := p.Live()
	desk := open(t, app, "u_alice", `{"execution":{"via":"desktop","desktop":"src_0123456789abcdef0123456789abcdef","provider":"claude","instance":"claude-1"}}`)
	if p.Live() != liveBefore {
		t.Fatalf("desktop execution leased a cloud cell: live went %d -> %d", liveBefore, p.Live())
	}
	if gotOrg != "hanzo" || gotSub != "u_alice" || gotID != "src_0123456789abcdef0123456789abcdef" || gotProv != "claude" || gotInst != "claude-1" {
		t.Fatalf("dispatch args = (%s, %s, %s, %s, %s)", gotOrg, gotSub, gotID, gotProv, gotInst)
	}
	if desk.ID != gotID || desk.Zap != "https://mac.example/zap" || desk.Lease != "lse_abc" || desk.Expires != "2026-11-21T10:15:00Z" || desk.Sandbox != "" {
		t.Fatalf("desktop session = %+v", desk)
	}
	if desk.Execution == nil || desk.Execution.Via != "desktop" || desk.Execution.Provider != "claude" || desk.Execution.Instance != "claude-1" {
		t.Fatalf("desktop execution = %+v", desk.Execution)
	}

	// Ending a desktop session with ?lease= releases the lease via apps/source.
	if code, b := as(t, app, "u_alice", http.MethodDelete, "/v1/dev/sessions/"+desk.ID+"?lease="+desk.Lease, ""); code != http.StatusOK {
		t.Fatalf("DELETE desktop session = %d %s", code, b)
	}
	if releasedLease != "lse_abc" {
		t.Fatalf("released lease = %q, want lse_abc", releasedLease)
	}

	// Refusals from Dispatch propagate untouched with exact status and machine code,
	// and never fall back to a cloud cell.
	dispatchSource = func(_ context.Context, _, _, _, _, _ string) (*client.SourceRouteOut, error) {
		return nil, &zip.HTTPError{Status: http.StatusConflict, Code: "offline", Msg: "this source is offline"}
	}
	code, body := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions",
		`{"runtime":"source","execution":{"via":"desktop","desktop":"src_0123456789abcdef0123456789abcdef","provider":"claude-1"}}`)
	if code != http.StatusConflict || !strings.Contains(string(body), `"offline"`) {
		t.Fatalf("refusal propagation = %d %s, want 409 offline", code, body)
	}
	if p.Live() != liveBefore {
		t.Fatalf("refused desktop fell back to a cloud cell")
	}

	// Validation errors are 400.
	for name, body := range map[string]string{
		"unknown via":       `{"execution":{"via":"magic"}}`,
		"missing desktop":   `{"execution":{"via":"desktop","provider":"claude"}}`,
		"missing provider":  `{"execution":{"via":"desktop","desktop":"src_1"}}`,
		"sandbox + desktop": `{"runtime":"sandbox","execution":{"via":"desktop","desktop":"src_1","provider":"claude"}}`,
		"source + cell":     `{"runtime":"source","execution":{"via":"cell"}}`,
	} {
		if code, out := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", body); code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", name, code, out)
		}
	}
}

// Suspending a sandbox session parks its pod (clearing ephemeral /tmp and stopping
// processes) while keeping its disk; resuming re-leases the cell and re-runs
// `hanzo-dev serve` so its WebTransport ZAP socket is live again.
func TestResumingAParkedCellRestartsTheDevServer(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	var starts [][]string
	failDaemon := false
	p.Other = func(_ string, argv []string) client.Ran {
		if len(argv) > 2 && strings.Contains(argv[2], "hanzo-dev serve") {
			starts = append(starts, append([]string(nil), argv...))
			if failDaemon {
				return client.Ran{ExitCode: 127, Stderr: "hanzo-dev failed on resume"}
			}
		}
		return client.Ran{}
	}
	app := mount(t)

	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://hanzo.id","sub":"u_alice"}`))
	bearer := "Bearer eyJhbGciOiJSUzI1NiJ9." + claims + ".c2lnbmF0dXJl"
	headers := map[string]string{"Authorization": bearer}

	code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", "", headers)
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/dev/sessions = %d %s", code, b)
	}
	var opened Session
	if err := json.Unmarshal(b, &opened); err != nil {
		t.Fatalf("decode opened: %v", err)
	}
	if len(starts) != 1 {
		t.Fatalf("starts after open = %d, want 1", len(starts))
	}

	if code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+opened.ID+"/suspend", "", headers); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, b)
	}
	if p.Live() != 0 {
		t.Fatalf("live pods after suspend = %d, want 0", p.Live())
	}

	code, b = asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+opened.ID+"/resume", "", headers)
	if code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, b)
	}
	var resumed Session
	if err := json.Unmarshal(b, &resumed); err != nil {
		t.Fatalf("decode resumed: %v", err)
	}
	if resumed.ID != opened.ID || resumed.Sandbox != opened.ID || resumed.Zap != opened.Zap {
		t.Fatalf("resumed = %+v, want same cell %+v", resumed, opened)
	}
	if p.Live() != 1 {
		t.Fatalf("live pods after resume = %d, want 1", p.Live())
	}
	if len(starts) != 2 {
		t.Fatalf("hanzo-dev serve starts = %d, want 2 (initial create + resume)", len(starts))
	}
	last := starts[1]
	if len(last) != 7 || !strings.Contains(last[2], "HANZO_DEV_ZAP_SOCKET="+Socket) || last[4] != "hanzo/alice" || last[5] != "https://hanzo.id" {
		t.Fatalf("resume started Dev with %q; want socket %s, principal hanzo/alice, issuer https://hanzo.id", last, Socket)
	}

	// If hanzo-dev fails to start on resume, return 503 rather than a dead ZAP URL.
	if code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+opened.ID+"/suspend", "", headers); code != http.StatusOK {
		t.Fatalf("second suspend = %d %s", code, b)
	}
	failDaemon = true
	if code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+opened.ID+"/resume", "", headers); code != http.StatusServiceUnavailable || !strings.Contains(string(b), "hanzo-dev failed on resume") {
		t.Fatalf("resume when daemon fails = %d %s, want 503 with reason", code, b)
	}
}
