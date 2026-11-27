package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// google_user_test.go drives a person's own Google end to end against a Google
// that holds real state: authorization codes, access tokens and refresh tokens per
// account, the scopes each grant carries, revocations, and the messages Gmail
// accepted. Nothing under test is faked; only Google is.

// fakeGoogle is Google, as much of it as a person's connection and a send read.
type fakeGoogle struct {
	mu      sync.Mutex
	gen     map[string]int
	access  map[string]string // token -> email
	refresh map[string]string // refresh token -> email
	revoked []string
	sent    []string // the raw messages Gmail accepted
	expires int      // expires_in for new access tokens
	rotate  bool     // whether a refresh hands a new refresh token
	// tokenDown answers the token endpoint 502; sendStatus answers every send it.
	tokenDown  bool
	sendStatus int
	sendReason string
	tokenCalls int
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{gen: map[string]int{}, access: map[string]string{}, refresh: map[string]string{}, expires: 3600}
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)
	for _, v := range []struct {
		p    *string
		path string
	}{{&googleAuthURL, "/auth"}, {&googleTokenURL, "/token"}, {&googleRevokeURL, "/revoke"}, {&googleUserinfoURL, "/userinfo"}, {&gmailSendURL, "/send"}} {
		old := *v.p
		*v.p = srv.URL + v.path
		t.Cleanup(func() { *v.p = old })
	}
	return g
}

func (g *fakeGoogle) issue(email string) (string, string) {
	g.gen[email]++
	n := g.gen[email]
	tok, ref := fmt.Sprintf("gtok-%s-%d", email, n), fmt.Sprintf("gref-%s-%d", email, n)
	g.access[tok] = email
	g.refresh[ref] = email
	return tok, ref
}

func (g *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch r.URL.Path {
	case "/token":
		g.tokenCalls++
		if g.tokenDown {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("client_secret") != "gshh" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
			return
		}
		scope := "openid https://www.googleapis.com/auth/userinfo.email " + gmailSendScope
		switch r.Form.Get("grant_type") {
		case "refresh_token":
			email := g.refresh[r.Form.Get("refresh_token")]
			if email == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			tok, ref := g.issue(email)
			out := map[string]any{"access_token": tok, "expires_in": g.expires, "scope": scope}
			if g.rotate {
				delete(g.refresh, r.Form.Get("refresh_token"))
				out["refresh_token"] = ref
			} else {
				delete(g.refresh, ref)
			}
			_ = json.NewEncoder(w).Encode(out)
		case "authorization_code":
			code := r.Form.Get("code")
			if !strings.HasPrefix(code, "code-") {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			email := strings.TrimPrefix(code, "code-")
			if e, ok := strings.CutSuffix(email, "-nosend"); ok {
				email, scope = e, "openid https://www.googleapis.com/auth/userinfo.email"
			}
			online := false
			if e, ok := strings.CutSuffix(email, "-online"); ok {
				email, online = e, true
			}
			tok, ref := g.issue(email)
			out := map[string]any{"access_token": tok, "expires_in": g.expires, "scope": scope}
			if !online {
				out["refresh_token"] = ref
			}
			_ = json.NewEncoder(w).Encode(out)
		}
	case "/userinfo":
		email, ok := g.access[auth]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "g-" + email, "email": email})
	case "/revoke":
		_ = r.ParseForm()
		t := r.Form.Get("token")
		g.revoked = append(g.revoked, t)
		if email, ok := g.refresh[t]; ok {
			delete(g.refresh, t)
			for k, v := range g.access {
				if v == email {
					delete(g.access, k)
				}
			}
		}
		delete(g.access, t)
		w.WriteHeader(http.StatusOK)
	case "/send":
		if _, ok := g.access[auth]; !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"status": "UNAUTHENTICATED"}})
			return
		}
		if g.sendStatus != 0 {
			w.WriteHeader(g.sendStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": g.sendReason}}}})
			return
		}
		var body struct {
			Raw string `json:"raw"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, err := base64.RawURLEncoding.DecodeString(body.Raw)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g.sent = append(g.sent, string(raw))
		_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("m%d", len(g.sent)), "threadId": "t1"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// withGoogle points every Google endpoint at the fake and configures the client.
func withGoogle(t *testing.T) (*fakeGoogle, *zip.App) {
	t.Helper()
	g := newFakeGoogle(t)
	t.Setenv(googleClientIDEnv, "gid.apps.googleusercontent.com")
	t.Setenv(googleClientSecretEnv, "gshh")
	return g, newApp(t, newKMS(t))
}

// googleState reads the state off a connect's consent URL, and checks the URL
// asks for exactly what a person's grant is.
func googleState(t *testing.T, body []byte) string {
	t.Helper()
	var out googleUserConnectOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("connect body: %v (%s)", err, body)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatalf("authorizeUrl: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "gid.apps.googleusercontent.com" || q.Get("redirect_uri") != "https://api.hanzo.ai/v1/provider/google/user/callback" ||
		q.Get("scope") != "openid email "+gmailSendScope || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Fatalf("consent URL asks for the wrong grant: %s", out.AuthorizeURL)
	}
	return q.Get("state")
}

// googleGrant runs Google's return and reads the grant the page is handed.
func googleGrant(t *testing.T, app *zip.App, state, code string) string {
	t.Helper()
	r := asPerson(t, app, http.MethodGet, "/v1/provider/google/user/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), "", "", false, nil)
	if r.Code != http.StatusFound {
		t.Fatalf("callback want 302, got %d (%s)", r.Code, r.Body)
	}
	u, err := url.Parse(r.Location)
	if err != nil || u.Query().Get("complete") != "google" || u.Query().Get("grant") == "" {
		t.Fatalf("callback should hand the page a grant, got %q", r.Location)
	}
	return u.Query().Get("grant")
}

// connectGoogle walks the whole flow as user in org, authorizing as code.
func connectGoogle(t *testing.T, app *zip.App, org, user, code string) httpResult {
	t.Helper()
	r := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/connect", org, user, false, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", r.Code, r.Body)
	}
	grant := googleGrant(t, app, googleState(t, r.Body), code)
	return asPerson(t, app, http.MethodPost, "/v1/provider/google/user/complete", org, user, false, map[string]string{"grant": grant})
}

func googleOut(t *testing.T, r httpResult) googleUserOut {
	t.Helper()
	var out googleUserOut
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("status body: %v (%s)", err, r.Body)
	}
	return out
}

func googleStatus(t *testing.T, app *zip.App, org, user string) googleUserOut {
	t.Helper()
	return googleOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/google/user", org, user, false, nil))
}

// sendAs asks the plane op as a person, the way the app that composes invites will.
func sendAs(org, user string, in *client.GoogleSendIn) (*client.GoogleSendOut, error) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user})
	return planeGoogleSend(ctx, in)
}

func invite(to string) *client.GoogleSendIn {
	return &client.GoogleSendIn{To: to, Subject: "Join me on Hanzo — früh", Text: "Hey, try Hanzo: https://hanzo.ai/beta?ref=ada7", Unsubscribe: "https://api.hanzo.ai/v1/marketing/unsubscribe?t=x"}
}

// TestPersonConnectsTheirOwnGoogle is the whole flow: the callback completes
// nothing, the page completes it as the person who started it, the tokens are
// sealed in their own custody, and status reports the address.
func TestPersonConnectsTheirOwnGoogle(t *testing.T) {
	_, app := withGoogle(t)
	if got := googleStatus(t, app, "acme", "alice"); !got.Configured || got.Connected {
		t.Fatalf("before connecting: want configured and not connected, got %+v", got)
	}
	if r := connectGoogle(t, app, "acme", "alice", "code-alice@example.com"); r.Code != http.StatusOK {
		t.Fatalf("complete want 200, got %d (%s)", r.Code, r.Body)
	}
	got := googleStatus(t, app, "acme", "alice")
	if !got.Connected || !got.Send || got.Email != "alice@example.com" || got.ExpiresAt == "" {
		t.Fatalf("after connecting: %+v", got)
	}
	path, _ := userPath("acme", "alice", googleUserID, defaultLabel)
	for name, want := range map[string]string{accessSecret: "gtok-alice@example.com-", refreshSecret: "gref-alice@example.com-"} {
		if v, err := kmsGet(mounted, path, name); err != nil || !strings.HasPrefix(string(v), want) {
			t.Fatalf("%s not in the person's custody: %q %v", name, v, err)
		}
	}
	if got := googleStatus(t, app, "acme", "bob"); got.Connected {
		t.Fatalf("bob must not inherit alice's connection: %+v", got)
	}
	if got := googleStatus(t, app, "other", "alice"); got.Connected {
		t.Fatalf("alice's connection is hers in acme alone: %+v", got)
	}
	if conns := Connections("acme", "google"); len(conns) != 0 {
		t.Fatalf("a personal connection must not appear as the org's Google: %+v", conns)
	}
}

// TestAnotherPersonCannotCompleteAGoogleFlow is the transplant: a link carrying
// alice's state, opened by bob, parks bob's code for alice's flow; nobody but alice
// completes it, once, and a state is single-use.
func TestAnotherPersonCannotCompleteAGoogleFlow(t *testing.T) {
	_, app := withGoogle(t)
	r := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/connect", "acme", "alice", false, nil)
	st := googleState(t, r.Body)
	grant := googleGrant(t, app, st, "code-bob@example.com")
	for _, who := range []struct{ org, user string }{{"acme", "bob"}, {"other", "alice"}} {
		if rr := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/complete", who.org, who.user, false,
			map[string]string{"grant": grant}); rr.Code != http.StatusNotFound {
			t.Fatalf("%s/%s completing alice's flow want 404, got %d (%s)", who.org, who.user, rr.Code, rr.Body)
		}
	}
	if rr := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/complete", "acme", "alice", false,
		map[string]string{"grant": grant}); rr.Code != http.StatusOK {
		t.Fatalf("alice completing her own flow want 200, got %d (%s)", rr.Code, rr.Body)
	}
	if rr := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/complete", "acme", "alice", false,
		map[string]string{"grant": grant}); rr.Code != http.StatusNotFound {
		t.Fatalf("a completed grant must not complete twice, got %d", rr.Code)
	}
	rr := asPerson(t, app, http.MethodGet, "/v1/provider/google/user/callback?state="+url.QueryEscape(st)+"&code=code-x@example.com", "", "", false, nil)
	if !strings.Contains(rr.Location, "error=google") {
		t.Fatalf("a replayed state must fail, got %q", rr.Location)
	}
	// The org's Google state is not a person's, nor is GitHub's.
	for _, p := range []struct{ user, provider string }{{"", "google"}, {"alice", githubUserID}} {
		other, _ := sign(mounted, "acme", p.user, p.provider, "n1")
		rr = asPerson(t, app, http.MethodGet, "/v1/provider/google/user/callback?state="+url.QueryEscape(other)+"&code=code-x@example.com", "", "", false, nil)
		if !strings.Contains(rr.Location, "error=google") {
			t.Fatalf("a %s state must not complete a person's Google flow, got %q", p.provider, rr.Location)
		}
	}
}

// TestAGrantThatCannotSendIsNotKept: a person who unticks sending, or a grant with
// no offline access, is refused, nothing is sealed, and the grant dies at Google.
func TestAGrantThatCannotSendIsNotKept(t *testing.T) {
	g, app := withGoogle(t)
	for _, code := range []string{"code-alice@example.com-nosend", "code-alice@example.com-online"} {
		if r := connectGoogle(t, app, "acme", "alice", code); r.Code != http.StatusFailedDependency {
			t.Fatalf("%s: want 424, got %d (%s)", code, r.Code, r.Body)
		}
		if got := googleStatus(t, app, "acme", "alice"); got.Connected {
			t.Fatalf("%s: nothing may be kept: %+v", code, got)
		}
		path, _ := userPath("acme", "alice", googleUserID, defaultLabel)
		if v, err := kmsGet(mounted, path, accessSecret); err == nil && len(v) > 0 {
			t.Fatalf("%s: a refused grant must seal nothing", code)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.revoked) != 2 {
		t.Fatalf("each refused grant must be revoked at Google, got %v", g.revoked)
	}
}

// TestUnconfiguredGoogleConnectIs503 is the deployment without the client secret.
func TestUnconfiguredGoogleConnectIs503(t *testing.T) {
	_, app := withGoogle(t)
	t.Setenv(googleClientSecretEnv, "")
	if got := googleStatus(t, app, "acme", "alice"); got.Configured {
		t.Fatalf("no client secret must read configured=false: %+v", got)
	}
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/connect", "acme", "alice", false, nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("connect without a client secret want 503, got %d (%s)", r.Code, r.Body)
	}
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/connect", "", "", false, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous connect want 401, got %d", r.Code)
	}
}

// TestGoogleTokenRotates: a token near its end is rotated with the refresh token;
// a new refresh token Google hands is sealed; Google not answering keeps the
// connection; a refresh token Google refuses drops it, and nothing retries it.
func TestGoogleTokenRotates(t *testing.T) {
	g, app := withGoogle(t)
	g.expires = 30 // inside the one-minute window
	connectGoogle(t, app, "acme", "alice", "code-alice@example.com")
	tok, email, ok, err := personGoogle(context.Background(), mounted, "acme", "alice", false)
	if err != nil || !ok || email != "alice@example.com" || tok != "gtok-alice@example.com-2" {
		t.Fatalf("a token inside the window must rotate: tok=%q email=%q ok=%v err=%v", tok, email, ok, err)
	}
	g.mu.Lock()
	g.rotate = true
	g.mu.Unlock()
	if tok, _, ok, err := personGoogle(context.Background(), mounted, "acme", "alice", false); !ok || err != nil || tok != "gtok-alice@example.com-3" {
		t.Fatalf("second rotation: %q ok=%v err=%v", tok, ok, err)
	}
	path, _ := userPath("acme", "alice", googleUserID, defaultLabel)
	if ref, _ := kmsGet(mounted, path, refreshSecret); string(ref) != "gref-alice@example.com-3" {
		t.Fatalf("a refresh token Google rotated must be sealed in the old one's place, got %q", ref)
	}
	if got := googleStatus(t, app, "acme", "alice"); !got.Send {
		t.Fatalf("a refresh must keep the grant's scopes: %+v", got)
	}
	g.mu.Lock()
	g.tokenDown = true
	g.mu.Unlock()
	if _, _, ok, err := personGoogle(context.Background(), mounted, "acme", "alice", false); ok || err == nil {
		t.Fatalf("a token endpoint that is down must fail the read, got ok=%v err=%v", ok, err)
	}
	if got := googleStatus(t, app, "acme", "alice"); !got.Connected {
		t.Fatalf("a transient Google failure must not drop the connection: %+v", got)
	}
	g.mu.Lock()
	g.tokenDown = false
	g.refresh = map[string]string{}
	g.mu.Unlock()
	if _, _, ok, err := personGoogle(context.Background(), mounted, "acme", "alice", false); ok || err != nil {
		t.Fatalf("a dead refresh token must read as not connected, got ok=%v err=%v", ok, err)
	}
	if got := googleStatus(t, app, "acme", "alice"); got.Connected {
		t.Fatalf("a dead connection must read not connected: %+v", got)
	}
	g.mu.Lock()
	calls := g.tokenCalls
	g.mu.Unlock()
	for range 3 {
		_, _, _, _ = personGoogle(context.Background(), mounted, "acme", "alice", false)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tokenCalls != calls {
		t.Fatalf("a dropped connection must not be refreshed again, Google was asked %d more times", g.tokenCalls-calls)
	}
}

// TestGoogleDisconnectForgetsAndRevokes: disconnect revokes the grant at Google,
// wipes both sealed secrets and the row, and succeeds when nothing is connected.
func TestGoogleDisconnectForgetsAndRevokes(t *testing.T) {
	g, app := withGoogle(t)
	connectGoogle(t, app, "acme", "alice", "code-alice@example.com")
	r := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/disconnect", "acme", "alice", false, nil)
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"revoked":true`) {
		t.Fatalf("disconnect want 200 and revoked, got %d (%s)", r.Code, r.Body)
	}
	g.mu.Lock()
	if len(g.revoked) != 1 || !strings.HasPrefix(g.revoked[0], "gref-alice@example.com-") {
		g.mu.Unlock()
		t.Fatalf("disconnect must revoke the refresh token, which ends the grant: %v", g.revoked)
	}
	g.mu.Unlock()
	path, _ := userPath("acme", "alice", googleUserID, defaultLabel)
	for _, name := range []string{accessSecret, refreshSecret} {
		if v, err := kmsGet(mounted, path, name); err == nil && len(v) > 0 {
			t.Fatalf("disconnect must wipe %s from custody", name)
		}
	}
	if got := googleStatus(t, app, "acme", "alice"); got.Connected {
		t.Fatalf("disconnected must read not connected: %+v", got)
	}
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/google/user/disconnect", "acme", "alice", false, nil); r.Code != http.StatusOK ||
		!strings.Contains(string(r.Body), `"revoked":false`) {
		t.Fatalf("disconnecting nothing want 200 and nothing revoked, got %d (%s)", r.Code, r.Body)
	}
}

// TestASendIsThePersonsOwn: the message goes from the caller's own address,
// whoever else is in the org; a person with no connection sends nothing; the
// message carries what the caller gave and no header it did not.
func TestASendIsThePersonsOwn(t *testing.T) {
	g, app := withGoogle(t)
	connectGoogle(t, app, "acme", "alice", "code-alice@example.com")
	out, err := sendAs("acme", "alice", invite("Grace <grace@example.org>"))
	if err != nil || out.ID == "" || out.From != "alice@example.com" || out.Reason != "" {
		t.Fatalf("alice's send: %+v %v", out, err)
	}
	g.mu.Lock()
	raw := g.sent[0]
	g.mu.Unlock()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("the sent message does not parse: %v", err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if m.Header.Get("From") != "<alice@example.com>" || m.Header.Get("To") != `"Grace" <grace@example.org>` || subject != "Join me on Hanzo — früh" ||
		m.Header.Get("List-Unsubscribe") != "<https://api.hanzo.ai/v1/marketing/unsubscribe?t=x>" || m.Header.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers: %v", m.Header)
	}
	// Bob is in the same org and has connected nothing; alice in another org
	// is another person's custody.
	for _, who := range []struct{ org, user string }{{"acme", "bob"}, {"other", "alice"}} {
		out, err := sendAs(who.org, who.user, invite("grace@example.org"))
		if err != nil || out.Reason != sendNotConnected || out.ID != "" {
			t.Fatalf("%s/%s must not send from alice's Gmail: %+v %v", who.org, who.user, out, err)
		}
	}
	if _, err := sendAs("acme", "", invite("grace@example.org")); err == nil {
		t.Fatal("a call with no person on it must be refused")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sent) != 1 {
		t.Fatalf("only alice's message may reach Gmail, got %d", len(g.sent))
	}
}

// TestASendRefusesWhatIsNotOneMessage: a second recipient, a line break that
// would start a header, an empty body or a foreign unsubscribe scheme is 400.
func TestASendRefusesWhatIsNotOneMessage(t *testing.T) {
	g, app := withGoogle(t)
	connectGoogle(t, app, "acme", "alice", "code-alice@example.com")
	bad := []*client.GoogleSendIn{
		{To: "a@b.co, c@d.co", Subject: "s", Text: "t"},
		{To: "a@b.co\r\nBcc: x@y.co", Subject: "s", Text: "t"},
		{To: "a@b.co", Subject: "s\r\nBcc: x@y.co", Text: "t"},
		{To: "a@b.co", Subject: "s", Text: " "},
		{To: "a@b.co", Subject: "s", Text: "t", Unsubscribe: "javascript:alert(1)"},
		{To: "a@b.co", Subject: "s", Text: "t", Unsubscribe: "https://x.co/u>\r\nBcc: y@z.co"},
		{To: "not an address", Subject: "s", Text: "t"},
	}
	for _, in := range bad {
		if _, err := sendAs("acme", "alice", in); err == nil {
			t.Fatalf("must refuse %+v", in)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sent) != 0 {
		t.Fatalf("nothing refused may reach Gmail, got %d", len(g.sent))
	}
}

// TestASendRotatesAndSaysWhy: a token Google refuses is rotated once and the send
// tried once more; Gmail's limit and its refusals are answers with reasons; a
// grant that is gone is dropped and the answer says to connect again.
func TestASendRotatesAndSaysWhy(t *testing.T) {
	g, app := withGoogle(t)
	connectGoogle(t, app, "acme", "alice", "code-alice@example.com")
	// Google forgets the access token before it expires: one rotation, one retry.
	g.mu.Lock()
	g.access = map[string]string{}
	g.mu.Unlock()
	if out, err := sendAs("acme", "alice", invite("grace@example.org")); err != nil || out.ID == "" {
		t.Fatalf("a refused token must rotate and send: %+v %v", out, err)
	}
	for _, c := range []struct {
		status int
		reason string
		want   string
	}{
		{http.StatusTooManyRequests, "rateLimitExceeded", sendLimited},
		{http.StatusForbidden, "dailyLimitExceeded", sendLimited},
		{http.StatusForbidden, "insufficientPermissions", sendNotAllowed},
		{http.StatusBadRequest, "invalidArgument", sendRejected},
		{http.StatusInternalServerError, "backendError", sendUnavailable},
	} {
		g.mu.Lock()
		g.sendStatus, g.sendReason = c.status, c.reason
		g.mu.Unlock()
		out, err := sendAs("acme", "alice", invite("grace@example.org"))
		if err != nil || out.Reason != c.want || out.ID != "" || out.Error == "" {
			t.Fatalf("Gmail %d %s: want %s, got %+v %v", c.status, c.reason, c.want, out, err)
		}
	}
	// The person revokes Hanzo at Google: the refresh is refused, the connection goes.
	g.mu.Lock()
	g.sendStatus = 0
	g.access, g.refresh = map[string]string{}, map[string]string{}
	g.mu.Unlock()
	if out, err := sendAs("acme", "alice", invite("grace@example.org")); err != nil || out.Reason != sendNotConnected {
		t.Fatalf("a revoked grant must answer not_connected: %+v %v", out, err)
	}
	if got := googleStatus(t, app, "acme", "alice"); got.Connected {
		t.Fatalf("a revoked grant must be dropped: %+v", got)
	}
}

// TestGoogleConnectAndDisconnectAreRecorded: each leaves one audit record naming
// the person; a connect the trail does not take is not kept.
func TestGoogleConnectAndDisconnectAreRecorded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		g, app := withGoogle(t)
		rec, err := audit.Open(t.TempDir(), "provider", nil)
		if err != nil {
			t.Fatal(err)
		}
		mounted.State.audit = rec
		connectGoogle(t, app, "acme", "alice", "code-alice@example.com")
		asPerson(t, app, http.MethodPost, "/v1/provider/google/user/disconnect", "acme", "alice", false, nil)
		for _, action := range []string{"provider.google_user.connect", "provider.google_user.disconnect"} {
			rows, _, err := rec.Query(context.Background(), audit.Filter{Action: action, Limit: 5})
			if err != nil || len(rows) != 1 || rows[0].Actor.Sub != "alice" || rows[0].Actor.Org != "acme" {
				t.Fatalf("%s: want one record for acme/alice, got %d %v", action, len(rows), err)
			}
		}
		// A trail that cannot be written refuses the connect, and the grant dies.
		_ = rec.Close()
		if r := connectGoogle(t, app, "acme", "alice", "code-alice@example.com"); r.Code != http.StatusServiceUnavailable {
			t.Fatalf("an unrecorded connect want 503, got %d (%s)", r.Code, r.Body)
		}
		if got := googleStatus(t, app, "acme", "alice"); got.Connected {
			t.Fatalf("an unrecorded connect must not be kept: %+v", got)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if n := len(g.revoked); n < 2 {
			t.Fatalf("the unrecorded grant must be revoked at Google, revocations %v", g.revoked)
		}
	})
}
