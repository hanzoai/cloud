package integrations

// openai_test.go proves openai.go's wire protocol against a scriptable
// httptest stand-in for auth.openai.com (OPENAI_AUTH_BASE seam; zero live
// network): device start/poll (pending = HTTP 403, no RFC-8628 strings),
// server-side PKCE riding the exchange, bundle adoption via one live refresh,
// mandatory rotation on refresh, and token-free transport failures.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// oaMock is a scriptable fake of auth.openai.com.
type oaMock struct {
	mu                  sync.Mutex
	pendingPolls        int    // serve N x 403 before authorizing
	userCodeKey         string // "" => "user_code"; "usercode" covers the wire fallback key
	startStatus         int    // 0 => 200; 404 = device flow disabled
	refreshDropsRefresh bool   // omit refresh_token in refresh responses
	noAccountClaim      bool   // mint access JWTs without chatgpt_account_id
	requests            int
	pollBodies          [][]byte
	tokenForms          []url.Values
}

func (m *oaMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

func (m *oaMock) forms(grant string) []url.Values {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []url.Values{}
	for _, f := range m.tokenForms {
		if f.Get("grant_type") == grant {
			out = append(out, f)
		}
	}
	return out
}

func (m *oaMock) setDrops(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshDropsRefresh = v
}

// oaAccount/oaEmail are the identity claims every minted access JWT carries.
const (
	oaAccount = "acct-42"
	oaEmail   = "dev@acme.io"
)

// jwtToken mints an unsigned header.payload.sig access token (identity
// extraction does no signature check). Deterministic for equal inputs, so
// tests recompute the expected custody value.
func jwtToken(t *testing.T, account, email string, exp int64) string {
	t.Helper()
	claims := map[string]any{
		"https://api.openai.com/auth":    map[string]string{"chatgpt_account_id": account},
		"https://api.openai.com/profile": map[string]string{"email": email},
	}
	if exp != 0 {
		claims["exp"] = exp
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("jwt claims: %v", err)
	}
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return hdr + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func (m *oaMock) jwt(t *testing.T) string {
	// callers hold m.mu or run before concurrency; noAccountClaim is set at
	// construction only.
	account := oaAccount
	if m.noAccountClaim {
		account = ""
	}
	return jwtToken(t, account, oaEmail, 0)
}

func newOAMock(t *testing.T, m *oaMock) *httptest.Server {
	t.Helper()
	key := m.userCodeKey
	if key == "" {
		key = "user_code"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			if m.startStatus != 0 {
				w.WriteHeader(m.startStatus)
				_, _ = w.Write([]byte(`{"error":"disabled"}`))
				return
			}
			_, _ = w.Write([]byte(`{"device_auth_id":"da-1","` + key + `":"WDJB-MJHT","interval":1}`))
		case "/api/accounts/deviceauth/token":
			b := new(bytes.Buffer)
			_, _ = b.ReadFrom(r.Body)
			m.pollBodies = append(m.pollBodies, b.Bytes())
			if m.pendingPolls > 0 {
				m.pendingPolls--
				// Pending is expressed by STATUS CODE in this protocol.
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"authorization_code":"ac-1","code_verifier":"cv-1"}`))
		case "/oauth/token":
			_ = r.ParseForm()
			m.tokenForms = append(m.tokenForms, r.PostForm)
			resp := map[string]any{"access_token": m.jwt(t), "expires_in": 3600}
			switch r.PostForm.Get("grant_type") {
			case "authorization_code":
				resp["refresh_token"] = "R1"
			case "refresh_token":
				if !m.refreshDropsRefresh {
					resp["refresh_token"] = "R2"
				}
			default:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"unsupported_grant_type"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_AUTH_BASE", srv.URL)
	return srv
}

func TestOpenAIDevice(t *testing.T) {
	t.Run("full flow", func(t *testing.T) {
		m := &oaMock{pendingPolls: 1}
		srv := newOAMock(t, m)
		kc := newKMS(t)
		app := newApp(t, kc)

		res := asOK(t, app, http.MethodPost, devStartPath("openai"), "acme", userUUID, nil)
		st := decode[startResp](t, res)
		if st.Flow == "" || st.UserCode != "WDJB-MJHT" || st.VerifyURL != srv.URL+"/codex/device" || st.Interval != 1 {
			t.Fatalf("start response: %+v", st)
		}
		// The device_auth_id is the poll handle — secret-adjacent, never returned.
		if bytes.Contains(res.Body, []byte("da-1")) {
			t.Fatalf("device_auth_id leaked into the start response: %s", res.Body)
		}

		p1 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("openai", st.Flow), "acme", userUUID, nil))
		if p1.Status != "pending" {
			t.Fatalf("403 upstream must read as pending, got %+v", p1)
		}
		rewind(t, st.Flow)
		p2 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("openai", st.Flow), "acme", userUUID, nil))
		if p2.Status != "connected" || p2.Connector == nil {
			t.Fatalf("authorized poll want connected, got %+v", p2)
		}
		// Identity comes from the access JWT claims; expiry from expires_in.
		if p2.Connector.ID != "openai:default" || p2.Connector.ExternalID != oaAccount || p2.Connector.Account != oaEmail {
			t.Fatalf("connector identity: %+v", p2.Connector)
		}
		exp, err := time.Parse(time.RFC3339, p2.Connector.ExpiresAt)
		if err != nil {
			t.Fatalf("expiresAt parse: %v (%q)", err, p2.Connector.ExpiresAt)
		}
		if d := exp.Unix() - (time.Now().Unix() + 3600); d < -5 || d > 5 {
			t.Fatalf("expiresAt want now+3600±5s, off by %ds", d)
		}
		// The poll referenced the stored device handle.
		m.mu.Lock()
		pollBody := string(m.pollBodies[0])
		m.mu.Unlock()
		if !strings.Contains(pollBody, "da-1") {
			t.Fatalf("poll request must carry the device handle: %s", pollBody)
		}
		// The exchange form carried the server-side PKCE verifier untouched.
		ex := m.forms("authorization_code")
		if len(ex) != 1 {
			t.Fatalf("want exactly one exchange, got %d", len(ex))
		}
		f := ex[0]
		if f.Get("code") != "ac-1" || f.Get("redirect_uri") != srv.URL+"/deviceauth/callback" ||
			f.Get("client_id") != openaiClientID || f.Get("code_verifier") != "cv-1" {
			t.Fatalf("exchange form: %v", f)
		}
		// Custody: the exact JWT + refresh token, in the user namespace.
		if v, err := userHas(t, kc, "acme", userUUID, "openai", "default", accessSecret); err != nil || string(v) != jwtToken(t, oaAccount, oaEmail, 0) {
			t.Fatalf("access custody: err=%v", err)
		}
		if v, err := userHas(t, kc, "acme", userUUID, "openai", "default", refreshSecret); err != nil || string(v) != "R1" {
			t.Fatalf("refresh custody: %q err=%v", v, err)
		}
	})

	t.Run("usercode fallback key", func(t *testing.T) {
		m := &oaMock{userCodeKey: "usercode"}
		newOAMock(t, m)
		app := newApp(t, newKMS(t))
		st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("openai"), "acme", userUUID, nil))
		if st.UserCode != "WDJB-MJHT" {
			t.Fatalf("fallback usercode key must populate userCode, got %+v", st)
		}
	})
}

func TestOpenAIStartDisabled(t *testing.T) {
	m := &oaMock{startStatus: http.StatusNotFound}
	newOAMock(t, m)
	app := newApp(t, newKMS(t))
	res := as(t, app, http.MethodPost, devStartPath("openai"), "acme", userUUID, nil)
	if res.Code != http.StatusBadGateway || !bytes.Contains(res.Body, []byte("device start failed")) {
		t.Fatalf("disabled device flow want 502, got %d (%s)", res.Code, res.Body)
	}
	if bytes.Contains(res.Body, []byte("da-1")) {
		t.Fatalf("response must not carry a device handle: %s", res.Body)
	}
}

func TestOpenAIAdopt(t *testing.T) {
	// noAccountClaim: the refreshed JWT carries no account id, proving the
	// Bundle.Account hint is the ExternalID fallback.
	m := &oaMock{noAccountClaim: true}
	newOAMock(t, m)
	kc := newKMS(t)
	app := newApp(t, kc)

	res := asOK(t, app, http.MethodPost, credPath("openai"), "acme", userEmail,
		map[string]any{"oauth": map[string]any{"access": "ignored", "refresh": "R0", "account": "acct-hint"}})
	cr := decode[credResp](t, res)
	if !cr.Connected || cr.Connector == nil || cr.Connector.ID != "openai:default" ||
		cr.Connector.ExternalID != "acct-hint" || cr.Connector.Account != oaEmail {
		t.Fatalf("adopt response: %+v", cr)
	}
	// Adoption is ONE live refresh with the submitted refresh token.
	rf := m.forms("refresh_token")
	if len(rf) != 1 || rf[0].Get("refresh_token") != "R0" {
		t.Fatalf("adopt must perform exactly one refresh with R0: %v", rf)
	}
	// Custody owns the ROTATED material — never the submitted bundle.
	jwt := jwtToken(t, "", oaEmail, 0)
	if v, err := userHas(t, kc, "acme", userEmail, "openai", "default", accessSecret); err != nil || string(v) != jwt {
		t.Fatalf("access custody: err=%v", err)
	}
	if v, err := userHas(t, kc, "acme", userEmail, "openai", "default", refreshSecret); err != nil || string(v) != "R2" {
		t.Fatalf("refresh custody: %q err=%v", v, err)
	}
	for _, leak := range []string{jwt, "R0", "R2"} {
		if bytes.Contains(res.Body, []byte(leak)) {
			t.Fatalf("token material leaked into the adopt response: %s", res.Body)
		}
	}

	// A bundle without a refresh token is refused OFFLINE — zero upstream calls.
	before := m.count()
	res = as(t, app, http.MethodPost, credPath("openai"), "acme", userEmail,
		map[string]any{"oauth": map[string]any{"access": "x"}, "label": "second"})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body, []byte("credential verification failed")) {
		t.Fatalf("refreshless bundle want 400, got %d (%s)", res.Code, res.Body)
	}
	if m.count() != before {
		t.Fatalf("refreshless bundle must not reach the provider (%d -> %d requests)", before, m.count())
	}
}

func TestOpenAIRefreshMandatoryRotation(t *testing.T) {
	m := &oaMock{}
	newOAMock(t, m)
	kc := newKMS(t)
	app := newApp(t, kc)

	asOK(t, app, http.MethodPost, credPath("openai"), "acme", userUUID,
		map[string]any{"oauth": map[string]any{"refresh": "R0"}})
	if v, err := userHas(t, kc, "acme", userUUID, "openai", "default", refreshSecret); err != nil || string(v) != "R2" {
		t.Fatalf("adopt custody precondition: %q err=%v", v, err)
	}

	// The server ALWAYS rotates; a response missing the rotated refresh token is
	// a FAILURE and nothing is overwritten.
	m.setDrops(true)
	res := as(t, app, http.MethodPost, refreshPath("openai:default"), "acme", userUUID, nil)
	if res.Code != http.StatusBadGateway || !bytes.Contains(res.Body, []byte("token refresh failed")) {
		t.Fatalf("rotationless refresh want 502, got %d (%s)", res.Code, res.Body)
	}
	if v, err := userHas(t, kc, "acme", userUUID, "openai", "default", refreshSecret); err != nil || string(v) != "R2" {
		t.Fatalf("failed rotation must keep the previous refresh token: %q err=%v", v, err)
	}
}

func TestOpenAIRefreshRotates(t *testing.T) {
	m := &oaMock{} // authorize on the first poll
	newOAMock(t, m)
	kc := newKMS(t)
	app := newApp(t, kc)

	st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("openai"), "acme", userUUID, nil))
	p := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("openai", st.Flow), "acme", userUUID, nil))
	if p.Status != "connected" || p.Connector == nil {
		t.Fatalf("device connect: %+v", p)
	}
	exp0, err := time.Parse(time.RFC3339, p.Connector.ExpiresAt)
	if err != nil {
		t.Fatalf("expiresAt parse: %v", err)
	}

	rr := decode[refreshResp](t, asOK(t, app, http.MethodPost, refreshPath("openai:default"), "acme", userUUID, nil))
	if !rr.Refreshed || rr.Connector == nil {
		t.Fatalf("force refresh: %+v", rr)
	}
	rf := m.forms("refresh_token")
	if len(rf) != 1 {
		t.Fatalf("want exactly one refresh grant, got %d", len(rf))
	}
	f := rf[0]
	if f.Get("refresh_token") != "R1" || f.Get("client_id") != openaiClientID {
		t.Fatalf("refresh form: %v", f)
	}
	// The refresh grant carries NEITHER scope NOR redirect_uri.
	if f.Has("scope") || f.Has("redirect_uri") {
		t.Fatalf("refresh grant must omit scope/redirect_uri: %v", f)
	}
	// Custody rotated to R2, and the request used the STORED R1 (never R2).
	if v, err := userHas(t, kc, "acme", userUUID, "openai", "default", refreshSecret); err != nil || string(v) != "R2" {
		t.Fatalf("rotated refresh custody: %q err=%v", v, err)
	}
	exp1, err := time.Parse(time.RFC3339, rr.Connector.ExpiresAt)
	if err != nil || exp1.Before(exp0) {
		t.Fatalf("refresh must advance expiresAt: %v -> %v (err=%v)", exp0, exp1, err)
	}
}

func TestOpenAITransportFailure(t *testing.T) {
	kc := newKMS(t)
	app, logs := newLogApp(t, kc)
	t.Setenv("OPENAI_AUTH_BASE", "http://127.0.0.1:1")

	res := as(t, app, http.MethodPost, devStartPath("openai"), "acme", userUUID, nil)
	if res.Code != http.StatusBadGateway || !bytes.Contains(res.Body, []byte("device start failed")) {
		t.Fatalf("transport failure want 502, got %d (%s)", res.Code, res.Body)
	}
	// Transport errors are deliberately unwrapped: no upstream URL (and never
	// token material) reaches a log line.
	if strings.Contains(logs.String(), "127.0.0.1:1") {
		t.Fatalf("raw transport error leaked into logs:\n%s", logs.String())
	}
}
