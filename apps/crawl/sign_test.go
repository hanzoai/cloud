package crawl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// bot is what the identity boundary mints for the hanzo.bot gateway's own
// client_credentials token: an application of the brand's org acting as itself.
var bot = principal.Principal{Org: "hanzo", User: "app:hanzo/bot", Subject: "hanzo/bot", Stable: true, Kind: principal.App, Holder: "hanzo-bot", Client: "hanzo-bot"}

// caller is one credential a test sends: the principal the boundary mints and
// the two headers it stamps beside it (home org, issuing brand).
type caller struct {
	p     principal.Principal
	owner string
	brand string
}

// signApp mounts crawl as the composition root does, behind a stand-in for the
// identity boundary that mints the caller a request names (X-Test-As).
func signApp(t *testing.T, callers map[string]caller) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	app.Use(zip.H(func(c *zip.Ctx) error {
		if who, ok := callers[c.Header("X-Test-As")]; ok {
			principal.Mint(c, who.p)
			c.Fiber().Request().Header.Set("X-User-Id", who.p.User)
			c.Fiber().Request().Header.Set("X-User-Owner", who.owner)
			c.Fiber().Request().Header.Set("X-User-Brand", who.brand)
		}
		return c.Continue()
	}))
	crawl := cloud.Plugin{Name: "crawl", Price: cloud.Metered, Prefixes: []string{botauth.Path, Path}, Use: Use}
	if err := cloud.UseAll(app, []cloud.Plugin{crawl}, &cloud.Config{}, cloud.Deps{}); err != nil {
		t.Fatalf("UseAll: %v", err)
	}
	return app
}

// askSign posts one sign request as who and answers the status and the body.
func askSign(t *testing.T, app *zip.App, who, authority string) (int, []byte) {
	t.Helper()
	return askSignScheme(t, app, who, authority, "")
}

// askSignScheme is askSign naming the request's scheme.
func askSignScheme(t *testing.T, app *zip.App, who, authority, scheme string) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(agentSignRequest{Authority: authority, Scheme: scheme})
	rq := httptest.NewRequest(http.MethodPost, SignPath, bytes.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	if who != "" {
		rq.Header.Set("X-Test-As", who)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", SignPath, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// published is the deployment's directory as a verifier reads it.
func published(t *testing.T, s *botauth.Signer) []byte {
	t.Helper()
	doc, _, err := s.Directory(context.Background(), "api.hanzo.test")
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestTheBotGetsASignatureThatVerifiesForItsHostOnly is the op's purpose: the
// hanzo.bot gateway asks for one host, attaches the three headers it is given,
// and a site verifies them against the published directory. The same headers on
// a request to another host are refused, so one answer signs one host.
func TestTheBotGetsASignatureThatVerifiesForItsHostOnly(t *testing.T) {
	s := &botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{}}
	botauth.Use(s)
	t.Cleanup(func() { botauth.Use(nil) })
	doc := published(t, s)
	app := signApp(t, map[string]caller{"bot": {bot, "hanzo", "hanzo"}})

	code, body := askSign(t, app, "bot", "Example.com:443")
	if code != http.StatusOK {
		t.Fatalf("the bot was refused: %d %s", code, body)
	}
	var got agentSignature
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if got.Expires <= time.Now().Unix() || got.Expires > time.Now().Add(botauth.Window+time.Minute).Unix() {
		t.Fatalf("expires %d is not about five minutes out", got.Expires)
	}
	carry := func(url string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		r.Host = r.URL.Host
		r.Header.Set("Signature-Agent", got.SignatureAgent)
		r.Header.Set("Signature-Input", got.SignatureInput)
		r.Header.Set("Signature", got.Signature)
		return r
	}
	if err := botauth.Verify(carry("https://example.com/a/page?q=1"), doc, time.Now()); err != nil {
		t.Fatalf("the bot's signed request does not verify: %v", err)
	}
	if err := botauth.Verify(carry("https://other.example/"), doc, time.Now()); !errors.Is(err, botauth.ErrRefused) {
		t.Fatalf("a signature for example.com verified on another host: %v", err)
	}
	// Over http the default port is 80, and a request to it is signed without it.
	code, body = askSignScheme(t, app, "bot", "example.com:80", "http")
	if code != http.StatusOK {
		t.Fatalf("an http authority was refused: %d %s", code, body)
	}
	var plain agentSignature
	if err := json.Unmarshal(body, &plain); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r.Host = "example.com"
	r.Header.Set("Signature-Agent", plain.SignatureAgent)
	r.Header.Set("Signature-Input", plain.SignatureInput)
	r.Header.Set("Signature", plain.Signature)
	if err := botauth.Verify(r, doc, time.Now()); err != nil {
		t.Fatalf("an http request signed for example.com:80 does not verify: %v", err)
	}
	if !strings.Contains(got.SignatureInput, `tag="web-bot-auth"`) || got.SignatureAgent != `"https://api.hanzo.test/.well-known/http-message-signatures-directory"` {
		t.Fatalf("not the Web Bot Auth wire: %+v", got)
	}
}

// TestOnlyThePlatformsOwnAgentMaySign is the gate. An answer is a signature any
// site takes as Hanzo's agent, so everything that is not the brand org's own
// listed application acting as itself is refused, and so is a typed call with no
// request behind it (MCP, the plane), where no boundary minted anyone.
func TestOnlyThePlatformsOwnAgentMaySign(t *testing.T) {
	botauth.Use(&botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{}})
	t.Cleanup(func() { botauth.Use(nil) })

	withAct := bot
	withAct.Act = principal.Act{Sub: "admin/op", Owner: "admin", Name: "op"}
	switched := bot
	switched.Org = "acme"
	refused := map[string]caller{
		// A person signed in to app.hanzo.bot: the same client, but a person.
		"person": {principal.Principal{Org: "hanzo", User: "u-1", Subject: "u-1", Stable: true, Kind: principal.Person, Holder: "hanzo/ada", Client: "hanzo-bot"}, "hanzo", "hanzo"},
		"key":    {principal.Principal{Org: "hanzo", User: "hanzo/ada", Subject: "hanzo/ada", Stable: true, Kind: principal.Key, Key: "k1"}, "hanzo", "hanzo"},
		// Another of the brand's applications, not on the list.
		"cloud-app": {principal.Principal{Org: "hanzo", User: "app:hanzo/cloud", Subject: "hanzo/cloud", Stable: true, Kind: principal.App, Holder: "hanzo-cloud", Client: "hanzo-cloud"}, "hanzo", "hanzo"},
		// A customer's application that calls itself hanzo-bot.
		"impostor": {principal.Principal{Org: "acme", User: "app:acme/bot", Subject: "acme/bot", Stable: true, Kind: principal.App, Holder: "hanzo-bot", Client: "hanzo-bot"}, "acme", "hanzo"},
		// The bot switched into an org that granted it membership.
		"switched": {switched, "hanzo", "hanzo"},
		// The bot's name on a token another brand's IAM issued.
		"other-brand": {bot, "hanzo", "lux"},
		"acting":      {withAct, "hanzo", "hanzo"},
	}
	app := signApp(t, refused)
	for who := range refused {
		if code, body := askSign(t, app, who, "example.com"); code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", who, code, body)
		}
	}
	if code, body := askSign(t, app, "", "example.com"); code != http.StatusForbidden {
		t.Errorf("nobody: %d %s, want 403", code, body)
	}
	if _, err := signForAgent(cloud.For(context.Background(), "hanzo"), &agentSignRequest{Authority: "example.com"}); err == nil {
		t.Error("a typed call with no request behind it was signed for")
	}
}

// TestTheSignOpRefusesWhatIsNotAHostAndSaysWhenItHasNoKey: an authority with a
// scheme, path, userinfo or space is 400 before anything is signed; no signer, no
// stored key and an unreadable store are each 503, never a signature over nothing.
func TestTheSignOpRefusesWhatIsNotAHostAndSaysWhenItHasNoKey(t *testing.T) {
	botauth.Use(&botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{set: mustKeySet(t)}})
	t.Cleanup(func() { botauth.Use(nil) })
	app := signApp(t, map[string]caller{"bot": {bot, "hanzo", "hanzo"}})
	for _, bad := range []string{"", "https://example.com", "example.com/x", "ada@example.com", "exa mple.com", "example.com:0", "example.com:99999", "[::1", "a..b"} {
		if code, body := askSign(t, app, "bot", bad); code != http.StatusBadRequest {
			t.Errorf("authority %q: %d %s, want 400", bad, code, body)
		}
	}
	if code, body := askSignScheme(t, app, "bot", "example.com", "ftp"); code != http.StatusBadRequest {
		t.Errorf("scheme ftp: %d %s, want 400", code, body)
	}

	botauth.Use(nil)
	if code, body := askSign(t, app, "bot", "example.com"); code != http.StatusServiceUnavailable {
		t.Errorf("no signer: %d %s, want 503", code, body)
	}
	botauth.Use(&botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{}})
	if code, body := askSign(t, app, "bot", "example.com"); code != http.StatusServiceUnavailable || !strings.Contains(string(body), "no signing key") {
		t.Errorf("no stored key: %d %s, want 503", code, body)
	}
	botauth.Use(&botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{fail: errors.New("kms: unreachable")}})
	if code, body := askSign(t, app, "bot", "example.com"); code != http.StatusServiceUnavailable || strings.Contains(string(body), "unreachable") {
		t.Errorf("unreadable store: %d %s, want 503 without the store's words", code, body)
	}
}

func mustKeySet(t *testing.T) []byte {
	t.Helper()
	set, err := botauth.NewKeySet()
	if err != nil {
		t.Fatal(err)
	}
	return set
}
