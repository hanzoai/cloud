package integrations

// copilot_test.go proves copilot.go's wire protocol against TWO httptest
// stand-ins (COPILOT_GITHUB_BASE login host + COPILOT_API_BASE verify host;
// zero live network): the RFC-8628 error-string flow (authorization_pending /
// slow_down / expired_token / access_denied), the live Copilot-seat verify
// gating every custody write, retryable verify transport failures, direct
// token intake, and start-side user-code bounds.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ghMock is a scriptable fake of the github.com login host.
type ghMock struct {
	mu           sync.Mutex
	userCode     string // "" => "WDJB-MJHT"
	pendingPolls int    // authorization_pending while poll n <= pendingPolls
	slowAt       int    // emit slow_down on poll n (0 = never)
	slowInterval int64  // slow_down interval field; 0 omits it (the +5s path)
	terminal     string // "" = authorize with gho_test; else the RFC-8628 error
	polls        int
	deviceForms  []url.Values
	tokenForms   []url.Values
}

func (m *ghMock) pollCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.polls
}

func (m *ghMock) lastTokenForm(t *testing.T) url.Values {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tokenForms) == 0 {
		t.Fatal("no access_token request captured")
	}
	return m.tokenForms[len(m.tokenForms)-1]
}

func newGHMock(t *testing.T, m *ghMock) *httptest.Server {
	t.Helper()
	if m.userCode == "" {
		m.userCode = "WDJB-MJHT"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_ = r.ParseForm()
			m.deviceForms = append(m.deviceForms, r.PostForm)
			_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"` + m.userCode +
				`","verification_uri":"https://ignored.example/login/device","expires_in":900,"interval":1}`))
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			m.tokenForms = append(m.tokenForms, r.PostForm)
			m.polls++
			switch {
			case m.slowAt != 0 && m.polls == m.slowAt:
				if m.slowInterval > 0 {
					_, _ = w.Write([]byte(`{"error":"slow_down","interval":` + strconv.FormatInt(m.slowInterval, 10) + `}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"slow_down"}`))
				}
			case m.polls <= m.pendingPolls:
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			case m.terminal != "":
				_, _ = w.Write([]byte(`{"error":"` + m.terminal + `"}`))
			default:
				// Re-served on repeat polls: a verify transport failure must be
				// retryable without burning the device code.
				_, _ = w.Write([]byte(`{"access_token":"gho_test","token_type":"bearer","scope":"read:user"}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COPILOT_GITHUB_BASE", srv.URL)
	return srv
}

// apiMock is a scriptable fake of the Copilot token-mint verify endpoint.
type apiMock struct {
	mu     sync.Mutex
	status int // 0 => 200 with a minted token
	seen   apiSeen
}

// apiSeen is the mutex-free capture of the last verify request.
type apiSeen struct {
	calls                              int
	auth, integrationID, editorVersion string
}

func (m *apiMock) setStatus(code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = code
}

func (m *apiMock) snapshot() apiSeen {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen
}

func newAPIMock(t *testing.T, m *apiMock) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/copilot_internal/v2/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		m.seen.calls++
		m.seen.auth = r.Header.Get("Authorization")
		m.seen.integrationID = r.Header.Get("Copilot-Integration-Id")
		m.seen.editorVersion = r.Header.Get("Editor-Version")
		status := m.status
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"no access"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"cop-minted","expires_at":9999999999}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COPILOT_API_BASE", srv.URL)
	return srv
}

func TestCopilotDevice(t *testing.T) {
	gh := &ghMock{pendingPolls: 1}
	ghSrv := newGHMock(t, gh)
	api := &apiMock{}
	newAPIMock(t, api)
	kc := newKMS(t)
	app := newApp(t, kc)

	res := asOK(t, app, http.MethodPost, devStartPath("github-copilot"), "acme", userUUID, nil)
	st := decode[startResp](t, res)
	// verifyUrl is the CONSTRUCTED canonical login URL, never the echoed
	// verification_uri.
	if st.Flow == "" || st.UserCode != "WDJB-MJHT" || st.VerifyURL != ghSrv.URL+"/login/device" || st.Interval != 1 {
		t.Fatalf("start response: %+v", st)
	}
	if bytes.Contains(res.Body, []byte("dc-1")) {
		t.Fatalf("device_code leaked into the start response: %s", res.Body)
	}
	gh.mu.Lock()
	df := gh.deviceForms[0]
	gh.mu.Unlock()
	if df.Get("client_id") != copilotClientID || df.Get("scope") != "read:user" {
		t.Fatalf("device start form: %v", df)
	}

	p1 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
	if p1.Status != "pending" || p1.Interval != 1 {
		t.Fatalf("pending poll want pending/1, got %+v", p1)
	}
	rewind(t, st.Flow)
	p2 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
	if p2.Status != "connected" || p2.Connector == nil || p2.Connector.ID != "github-copilot:default" {
		t.Fatalf("authorized poll want connected, got %+v", p2)
	}
	// The long-lived gh token has no known expiry.
	if p2.Connector.ExpiresAt != "" {
		t.Fatalf("copilot connector must carry no expiry, got %q", p2.Connector.ExpiresAt)
	}
	tf := gh.lastTokenForm(t)
	if tf.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" ||
		tf.Get("device_code") != "dc-1" || tf.Get("client_id") != copilotClientID {
		t.Fatalf("poll form: %v", tf)
	}
	// pollDone was live-proven by the Copilot token mint with the IDE headers.
	got := api.snapshot()
	if got.auth != "Bearer gho_test" || got.integrationID != "vscode-chat" || got.editorVersion != "vscode/1.107.0" {
		t.Fatalf("verify headers: %+v", got)
	}
	if v, err := userHas(t, kc, "acme", userUUID, "github-copilot", "default", accessSecret); err != nil || string(v) != "gho_test" {
		t.Fatalf("gh token custody: %q err=%v", v, err)
	}
}

func TestCopilotSlowDown(t *testing.T) {
	t.Run("with interval", func(t *testing.T) {
		gh := &ghMock{slowAt: 1, slowInterval: 7, pendingPolls: 2}
		newGHMock(t, gh)
		newAPIMock(t, &apiMock{})
		app := newApp(t, newKMS(t))

		st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("github-copilot"), "acme", userUUID, nil))
		p1 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
		if p1.Status != "pending" || p1.Interval != 7 {
			t.Fatalf("slow_down want pending/7, got %+v", p1)
		}
		// The raised cadence PERSISTED on the grant: the next pending poll
		// reflects it.
		rewind(t, st.Flow)
		p2 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
		if p2.Status != "pending" || p2.Interval != 7 {
			t.Fatalf("post-slow_down poll want pending/7, got %+v", p2)
		}
	})
	t.Run("interval omitted", func(t *testing.T) {
		gh := &ghMock{slowAt: 1, pendingPolls: 2}
		newGHMock(t, gh)
		newAPIMock(t, &apiMock{})
		app := newApp(t, newKMS(t))

		st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("github-copilot"), "acme", userUUID, nil))
		p1 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
		if p1.Status != "pending" || p1.Interval != 1+copilotSlowStep {
			t.Fatalf("intervalless slow_down want pending/%d, got %+v", 1+copilotSlowStep, p1)
		}
	})
}

func TestCopilotTerminal(t *testing.T) {
	for _, tc := range []struct{ terminal, wire string }{
		{"expired_token", "expired"},
		{"access_denied", "denied"},
	} {
		t.Run(tc.terminal, func(t *testing.T) {
			newGHMock(t, &ghMock{terminal: tc.terminal})
			newAPIMock(t, &apiMock{})
			kc := newKMS(t)
			app := newApp(t, kc)

			st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("github-copilot"), "acme", userUUID, nil))
			p := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
			if p.Status != tc.wire {
				t.Fatalf("terminal want %q, got %+v", tc.wire, p)
			}
			// The grant is burned; nothing entered custody.
			if res := as(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil); res.Code != http.StatusNotFound {
				t.Fatalf("poll after %s want 404, got %d", tc.wire, res.Code)
			}
			if _, err := userHas(t, kc, "acme", userUUID, "github-copilot", "default", accessSecret); err == nil {
				t.Fatal("terminal outcome must store nothing")
			}
		})
	}
}

func TestCopilotNoSeat(t *testing.T) {
	newGHMock(t, &ghMock{}) // authorizes immediately
	api := &apiMock{status: http.StatusForbidden}
	newAPIMock(t, api)
	kc := newKMS(t)
	app := newApp(t, kc)

	st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("github-copilot"), "acme", userUUID, nil))
	// GitHub authorized, but the account has no Copilot seat — a DEFINITIVE
	// refusal is terminal (denied), not a retryable 502.
	p := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
	if p.Status != "denied" {
		t.Fatalf("seatless authorize want denied, got %+v", p)
	}
	if res := as(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil); res.Code != http.StatusNotFound {
		t.Fatalf("poll after denied want 404, got %d", res.Code)
	}
	if _, err := userHas(t, kc, "acme", userUUID, "github-copilot", "default", accessSecret); err == nil {
		t.Fatal("seatless authorize must store nothing")
	}
}

func TestCopilotVerifyRetryable(t *testing.T) {
	gh := &ghMock{} // authorizes on every poll (re-serves the token)
	newGHMock(t, gh)
	api := &apiMock{}
	apiSrv := newAPIMock(t, api)
	kc := newKMS(t)
	app := newApp(t, kc)

	st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("github-copilot"), "acme", userUUID, nil))

	// Verify transport failure AFTER GitHub authorization: retryable — 502 and
	// the grant stays pollable (the device code is not burned).
	t.Setenv("COPILOT_API_BASE", "http://127.0.0.1:1")
	res := as(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil)
	if res.Code != http.StatusBadGateway || !bytes.Contains(res.Body, []byte("device poll failed")) {
		t.Fatalf("verify transport failure want 502, got %d (%s)", res.Code, res.Body)
	}

	t.Setenv("COPILOT_API_BASE", apiSrv.URL)
	rewind(t, st.Flow)
	p := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("github-copilot", st.Flow), "acme", userUUID, nil))
	if p.Status != "connected" {
		t.Fatalf("retried poll want connected, got %+v", p)
	}
	if gh.pollCount() != 2 {
		t.Fatalf("github must have been polled twice, got %d", gh.pollCount())
	}
	if v, err := userHas(t, kc, "acme", userUUID, "github-copilot", "default", accessSecret); err != nil || string(v) != "gho_test" {
		t.Fatalf("gh token custody after retry: %q err=%v", v, err)
	}
}

func TestCopilotTokenIntake(t *testing.T) {
	api := &apiMock{}
	newAPIMock(t, api)
	kc := newKMS(t)
	app := newApp(t, kc)

	// Pasted gh token: admitted only after the live Copilot verify.
	asOK(t, app, http.MethodPost, credPath("github-copilot"), "acme", userUUID, map[string]any{"token": "gho_pasted"})
	got := api.snapshot()
	if got.auth != "Bearer gho_pasted" || got.calls != 1 {
		t.Fatalf("intake verify: %+v", got)
	}
	if v, err := userHas(t, kc, "acme", userUUID, "github-copilot", "default", accessSecret); err != nil || string(v) != "gho_pasted" {
		t.Fatalf("intake custody: %q err=%v", v, err)
	}

	// Verify-before-store on intake: a rejected token stores nothing.
	api.setStatus(http.StatusUnauthorized)
	res := as(t, app, http.MethodPost, credPath("github-copilot"), "acme", "reject@user.io", map[string]any{"token": "gho_bad"})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body, []byte("credential verification failed")) {
		t.Fatalf("rejected intake want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, err := userHas(t, kc, "acme", "reject@user.io", "github-copilot", "default", accessSecret); err == nil {
		t.Fatal("a rejected token must not be sealed")
	}
}

func TestCopilotUserCodeBounds(t *testing.T) {
	newGHMock(t, &ghMock{userCode: strings.Repeat("X", 65)})
	newAPIMock(t, &apiMock{})
	app := newApp(t, newKMS(t))

	res := as(t, app, http.MethodPost, devStartPath("github-copilot"), "acme", userUUID, nil)
	if res.Code != http.StatusBadGateway || !bytes.Contains(res.Body, []byte("device start failed")) {
		t.Fatalf("oversized user_code want start-side 502, got %d (%s)", res.Code, res.Body)
	}
}
