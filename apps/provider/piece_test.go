package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/piece"
	data "github.com/hanzoai/cloud/piece/catalog"
	"github.com/hanzoai/cloud/sdk"
)

// The test binary installs the generated catalog as plugin/provider does at its
// composition root. Run and Check are the two things only the runtime can do;
// here they record what they were handed, so a test can prove the credential
// reached them in the shape the piece reads — and that nothing else did.
var fake = struct {
	sync.Mutex
	check   func(id, kind string, auth any) (bool, error)
	ran     []ranCall
	runErr  error
	runOut  any
	checked []any
}{}

type ranCall struct {
	org, id, action string
	auth            any
	input           map[string]any
}

func init() {
	c, err := data.Load()
	if err != nil {
		panic(err)
	}
	UsePieces(Pieces{
		Catalog: c.Pieces,
		Logo:    data.Logo,
		Run: func(_ context.Context, org, id, action string, auth any, input map[string]any) (any, error) {
			fake.Lock()
			defer fake.Unlock()
			fake.ran = append(fake.ran, ranCall{org, id, action, auth, input})
			return fake.runOut, fake.runErr
		},
		Check: func(_ context.Context, id, kind string, auth any) (bool, error) {
			fake.Lock()
			check := fake.check
			fake.checked = append(fake.checked, auth)
			fake.Unlock()
			if check == nil {
				return true, nil
			}
			return check(id, kind, auth)
		},
	})
}

func resetFake(t *testing.T) {
	t.Helper()
	fake.Lock()
	fake.check, fake.ran, fake.runErr, fake.runOut, fake.checked = nil, nil, nil, nil, nil
	fake.Unlock()
}

func listProviders(t *testing.T, res httpResult) map[string]providerView {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", res.Code, res.Body)
	}
	var out listOut
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("list body: %v", err)
	}
	by := map[string]providerView{}
	for _, p := range out.Providers {
		by[p.ID] = p
	}
	return by
}

// Every piece the catalog carries is a connector on the org surface, and each
// card says how it connects from what the piece declares.
func TestEveryPieceIsAConnector(t *testing.T) {
	app := newApp(t, newKMS(t))
	by := listProviders(t, req(t, app, http.MethodGet, "/v1/provider", "acme", nil))
	c, _ := data.Load()
	for _, pc := range c.Pieces {
		if _, ok := by[pc.ID]; !ok {
			if p := registry[pc.ID]; p != nil && p.Scope == userScope {
				continue // a native user-plane connector holds this id
			}
			t.Errorf("piece %q is not on the org surface", pc.ID)
		}
	}

	bitly := by["bitly"]
	if bitly.Kind != "credential" || !bitly.Available || !bitly.Checks || !bitly.Runnable {
		t.Fatalf("bitly card wrong: %+v", bitly)
	}
	if len(bitly.Fields) != 1 || bitly.Fields[0].Name != "accessToken" || !bitly.Fields[0].Secret || !bitly.Fields[0].Required {
		t.Fatalf("bitly fields wrong: %+v", bitly.Fields)
	}
	if bitly.Logo != "/v1/provider/bitly/logo" || len(bitly.Actions) == 0 || bitly.Actions[0].Props != nil {
		t.Fatalf("bitly logo/actions wrong: %+v", bitly)
	}
	// Reading one connector says what each action takes, which is what an
	// agent needs before it runs one.
	var one providerView
	res := req(t, app, http.MethodGet, "/v1/provider/bitly", "acme", nil)
	if err := json.Unmarshal(res.Body, &one); err != nil || res.Code != http.StatusOK {
		t.Fatalf("get bitly: %d %s", res.Code, res.Body)
	}
	var create *opView
	for i := range one.Actions {
		if one.Actions[i].Name == "create_bitlink" {
			create = &one.Actions[i]
		}
	}
	if create == nil || len(create.Props) == 0 || create.Props[0].Name == "" {
		t.Fatalf("create_bitlink carries no inputs: %+v", one.Actions)
	}

	// Gmail signs in with Google. With no Google app registered here the card
	// offers the credential it also takes, and names the app it awaits.
	gmail := by["gmail"]
	if gmail.Kind != "credential" || gmail.Runnable {
		t.Fatalf("gmail card wrong: %+v", gmail)
	}

	// A piece that needs nothing is ready and has nothing to connect.
	if h := by["http"]; h.Kind != noneKind || !h.Available || h.Note != "" {
		t.Fatalf("http card wrong: %+v", h)
	}

	// A native connector keeps its own flow and gains the piece's logo and
	// actions.
	if s := by["slack"]; s.Kind != "oauth" || s.Logo != "/v1/provider/slack/logo" || len(s.Actions) < 10 {
		t.Fatalf("slack card wrong: kind=%s logo=%s actions=%d", s.Kind, s.Logo, len(s.Actions))
	}
}

// With the identity host's app registered, every piece that signs in there is
// one press, through the one OAuth engine.
func TestPieceOAuthUsesTheHostsApp(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "gid")
	t.Setenv("GOOGLE_CLIENT_SECRET", "gsecret")
	app := newApp(t, newKMS(t))
	by := listProviders(t, req(t, app, http.MethodGet, "/v1/provider", "acme", nil))
	if g := by["gmail"]; g.Kind != "oauth" || !g.Available {
		t.Fatalf("gmail should be one press with Google's app: %+v", g)
	}
	res := req(t, app, http.MethodPost, "/v1/provider/google-sheets/connect", "acme", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
	}
	var out connectOut
	_ = json.Unmarshal(res.Body, &out)
	if !strings.HasPrefix(out.AuthorizeURL, "https://accounts.google.com/") ||
		!strings.Contains(out.AuthorizeURL, "client_id=gid") ||
		!strings.Contains(out.AuthorizeURL, "redirect_uri=https%3A%2F%2Fapi.hanzo.ai%2Fv1%2Fprovider%2Fgoogle-sheets%2Fcallback") {
		t.Fatalf("authorize URL wrong: %s", out.AuthorizeURL)
	}
	if strings.Contains(out.AuthorizeURL, "gsecret") {
		t.Fatal("the client secret reached the authorize URL")
	}
}

// A pasted credential is checked by the piece's own validate() and sealed only
// when it passes; a refusal stores nothing.
func TestPieceCredentialIsCheckedThenSealed(t *testing.T) {
	resetFake(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	fake.Lock()
	fake.check = func(id, kind string, auth any) (bool, error) {
		if m, _ := auth.(map[string]any); m["accessToken"] == "good" {
			return true, nil
		}
		return true, errors.New("Invalid Access Token")
	}
	fake.Unlock()

	bad := req(t, app, http.MethodPost, "/v1/provider/bitly/connect", "acme", map[string]any{"fields": map[string]string{"accessToken": "bad"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("a refused credential want 400, got %d (%s)", bad.Code, bad.Body)
	}
	if strings.Contains(string(bad.Body), "bad") && strings.Contains(string(bad.Body), "accessToken") {
		t.Fatalf("the refusal echoed the credential: %s", bad.Body)
	}
	if _, err := kmsGet(mounted, kmsPath("acme", "bitly"), pieceSecret); err == nil {
		t.Fatal("a refused credential was sealed")
	}

	unknown := req(t, app, http.MethodPost, "/v1/provider/bitly/connect", "acme", map[string]any{"fields": map[string]string{"accessToken": "good", "extra": "x"}})
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("an undeclared field want 400, got %d", unknown.Code)
	}

	ok := req(t, app, http.MethodPost, "/v1/provider/bitly/connect", "acme", map[string]any{"fields": map[string]string{"accessToken": "good"}})
	if ok.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", ok.Code, ok.Body)
	}
	raw, err := kmsGet(mounted, kmsPath("acme", "bitly"), pieceSecret)
	if err != nil {
		t.Fatalf("sealed credential: %v", err)
	}
	var sealed map[string]any
	_ = json.Unmarshal(raw, &sealed)
	if sealed["type"] != "CUSTOM_AUTH" || sealed["props"].(map[string]any)["accessToken"] != "good" {
		t.Fatalf("sealed value is not the shape the piece reads: %s", raw)
	}
	by := listProviders(t, req(t, app, http.MethodGet, "/v1/provider", "acme", nil))
	if !by["bitly"].Connected {
		t.Fatal("bitly should read connected")
	}
	if other := listProviders(t, req(t, app, http.MethodGet, "/v1/provider", "globex", nil)); other["bitly"].Connected {
		t.Fatal("another org sees acme's connection")
	}
}

// A connected piece runs its action as the org, with the org's own credential
// in the shape the piece reads, and another org's run finds nothing to spend.
func TestPieceRunsWithTheOrgsCredential(t *testing.T) {
	resetFake(t)
	app := newApp(t, newKMS(t))
	if res := req(t, app, http.MethodPost, "/v1/provider/mailjet/connect", "acme",
		map[string]any{"fields": map[string]string{"username": "AC1", "password": "pw"}}); res.Code != http.StatusOK {
		t.Fatalf("connect mailjet: %d (%s)", res.Code, res.Body)
	}
	fake.Lock()
	fake.runOut = map[string]any{"sid": "SM1"}
	fake.Unlock()

	res := req(t, app, http.MethodPost, "/v1/provider/mailjet/run", "acme",
		map[string]any{"action": "send_email", "input": map[string]any{"body": "hi"}})
	if res.Code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", res.Code, res.Body)
	}
	var out runOut
	_ = json.Unmarshal(res.Body, &out)
	if !out.Ok {
		t.Fatalf("run answered %s", res.Body)
	}
	fake.Lock()
	got := fake.ran[len(fake.ran)-1]
	fake.Unlock()
	auth, _ := got.auth.(map[string]any)
	if got.org != "acme" || got.id != "mailjet" || got.action != "send_email" || auth["type"] != "BASIC_AUTH" || auth["username"] != "AC1" {
		t.Fatalf("run was handed %+v", got)
	}

	if res := req(t, app, http.MethodPost, "/v1/provider/mailjet/run", "globex",
		map[string]any{"action": "send_email"}); res.Code != http.StatusFailedDependency {
		t.Fatalf("an org that never connected want 424, got %d (%s)", res.Code, res.Body)
	}
	if res := req(t, app, http.MethodPost, "/v1/provider/mailjet/run", "acme",
		map[string]any{"action": "no_such_action"}); res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown action want 422, got %d", res.Code)
	}
	if res := req(t, app, http.MethodPost, "/v1/provider/mailjet/run", "",
		map[string]any{"action": "send_email"}); res.Code != http.StatusForbidden && res.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous run want 401/403, got %d", res.Code)
	}
}

// The plane op acts for the org the CALLER states, never one it names.
func TestPlaneRunActsForTheCaller(t *testing.T) {
	resetFake(t)
	app := newApp(t, newKMS(t))
	if res := req(t, app, http.MethodPost, "/v1/provider/calendly/connect", "acme",
		map[string]any{"token": "cal-key"}); res.Code != http.StatusOK {
		t.Fatalf("connect calendly: %d (%s)", res.Code, res.Body)
	}
	fake.Lock()
	fake.runOut = "done"
	fake.Unlock()
	ctx := cloud.For(context.Background(), "acme")
	out, err := planeRun(ctx, &client.PieceRunIn{Provider: "calendly", Action: pieceActions(t, "calendly")[0], Input: []byte(`{}`)})
	if err != nil || out.Error != "" || string(out.Output) != `"done"` {
		t.Fatalf("plane run: %v %+v", err, out)
	}
	fake.Lock()
	auth, _ := fake.ran[len(fake.ran)-1].auth.(map[string]any)
	fake.Unlock()
	if auth["secret_text"] != "cal-key" {
		t.Fatalf("plane run was handed %+v", auth)
	}
	if _, err := planeRun(context.Background(), &client.PieceRunIn{Provider: "calendly", Action: "x"}); err == nil {
		t.Fatal("a plane run with no org on the call ran")
	}
}

func pieceActions(t *testing.T, id string) []string {
	t.Helper()
	var out []string
	for name := range registry[id].Actions {
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatalf("%s has no actions", id)
	}
	return out
}

// A connector that needs nothing refuses a connect rather than pretending.
func TestNothingToConnect(t *testing.T) {
	app := newApp(t, newKMS(t))
	if res := req(t, app, http.MethodPost, "/v1/provider/http/connect", "acme", nil); res.Code != http.StatusConflict {
		t.Fatalf("connect http want 409, got %d (%s)", res.Code, res.Body)
	}
}

// Logos are public images served from the catalog, under a policy that runs
// nothing.
func TestLogoIsServedInert(t *testing.T) {
	app := newApp(t, newKMS(t))
	res := req(t, app, http.MethodGet, "/v1/provider/bitly/logo", "", nil)
	if res.Code != http.StatusOK || len(res.Body) < 100 || string(res.Body[1:4]) != "PNG" {
		t.Fatalf("logo want a PNG, got %d (%d bytes)", res.Code, len(res.Body))
	}
	if res := req(t, app, http.MethodGet, "/v1/provider/no-such-piece/logo", "", nil); res.Code != http.StatusNotFound {
		t.Fatalf("unknown logo want 404, got %d", res.Code)
	}
}

// A failed action never carries the credential back out, even when the call it
// made put the key in its URL.
func TestRunErrorsNeverCarryTheCredential(t *testing.T) {
	resetFake(t)
	app := newApp(t, newKMS(t))
	if res := req(t, app, http.MethodPost, "/v1/provider/calendly/connect", "acme",
		map[string]any{"token": "sk-live-secret"}); res.Code != http.StatusOK {
		t.Fatalf("connect: %d (%s)", res.Code, res.Body)
	}
	fake.Lock()
	fake.runErr = errors.New(`Get "https://api.calendly.com/x?key=sk-live-secret": dial tcp: refused`)
	fake.Unlock()
	res := req(t, app, http.MethodPost, "/v1/provider/calendly/run", "acme", map[string]any{"action": pieceActions(t, "calendly")[0]})
	if res.Code != http.StatusOK || strings.Contains(string(res.Body), "sk-live-secret") || !strings.Contains(string(res.Body), "[redacted]") {
		t.Fatalf("run error leaked or lost: %d %s", res.Code, res.Body)
	}
}

func TestOAuthApp(t *testing.T) {
	for in, want := range map[string]string{
		"https://accounts.google.com/o/oauth2/auth":                      "GOOGLE",
		"https://login.microsoftonline.com/common/oauth2/v2.0/authorize": "MICROSOFT",
		"https://app.hubspot.com/oauth/authorize":                        "HUBSPOT",
		"https://connect.squareup.com/oauth2/authorize":                  "SQUARE",
		"https://www.dropbox.com/oauth2/authorize":                       "DROPBOX",
		"https://login.xero.co.uk/identity/connect/authorize":            "XERO",
	} {
		if got := oauthApp(in); got != want {
			t.Errorf("oauthApp(%s) = %q, want %q", in, got, want)
		}
	}
	if got, ok := substitute("https://{cloud}/common/oauth2", map[string]string{"cloud": "login.microsoftonline.com"}); !ok || got != "https://login.microsoftonline.com/common/oauth2" {
		t.Errorf("fill = %q %v", got, ok)
	}
	if _, ok := substitute("https://{account}.snowflakecomputing.com", nil); ok {
		t.Error("a placeholder with no default filled")
	}
	if got, ok := substitute("{domain}/oauth/v2/auth", map[string]string{"domain": "https://accounts.zoho.com"}); !ok || got != "https://accounts.zoho.com/oauth/v2/auth" {
		t.Errorf("fill scheme = %q", got)
	}
}

func TestPieceValueShapes(t *testing.T) {
	secret := piece.Auth{Type: piece.SecretText}
	v, check, err := pieceValue(secret, VerifyInput{Token: "k"})
	if err != nil || v["secret_text"] != "k" || check != "k" {
		t.Fatalf("secret_text: %v %v %v", v, check, err)
	}
	basic := piece.Auth{Type: piece.BasicAuth}
	if _, _, err := pieceValue(basic, VerifyInput{Fields: map[string]string{"username": "u"}}); err == nil {
		t.Fatal("basic auth without a password passed")
	}
	custom := piece.Auth{Type: piece.CustomAuth, Props: []piece.Prop{
		{Name: "key", Type: "SECRET_TEXT", Required: true},
		{Name: "limit", Type: "NUMBER"},
		{Name: "region", Type: "STATIC_DROPDOWN", Default: "us"},
		{Name: "note", Type: "MARKDOWN"},
	}}
	v, check, err = pieceValue(custom, VerifyInput{Fields: map[string]string{"key": "k", "limit": "5"}})
	props := v["props"].(map[string]any)
	if err != nil || props["key"] != "k" || props["limit"] != 5.0 || props["region"] != "us" || check.(map[string]any)["key"] != "k" {
		t.Fatalf("custom: %v %v %v", v, check, err)
	}
	if _, _, err := pieceValue(custom, VerifyInput{Fields: map[string]string{"limit": "5"}}); err == nil {
		t.Fatal("custom auth without its required field passed")
	}
	if _, _, err := pieceValue(custom, VerifyInput{Fields: map[string]string{"key": "k", "limit": "five"}}); err == nil {
		t.Fatal("a non-number passed as a number")
	}
}
