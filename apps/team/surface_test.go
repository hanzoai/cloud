package team

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// cast is the fixture every surface test drives: one space in org acme with an
// owner (Ada), a member (Bob) and a guest (Gus); a person in the SAME org who is
// in no space (Mal); and a person in ANOTHER org (Eve). Every request goes
// through the real HTTP stack, because every op here resolves its caller from
// the verified credential and a direct call would have none.
type cast struct {
	app                        *zip.App
	org, ws                    string
	ada, bob, gus, mal, eve    map[string]string
	adaID, bobID, gusID, malID string
	iam                        *planetest.Identity
}

const (
	adaID = "aaaaaaaa-0000-4000-8000-00000000000a"
	bobID = "bbbbbbbb-0000-4000-8000-00000000000b"
	gusID = "99999999-0000-4000-8000-000000000009"
	malID = "dddddddd-0000-4000-8000-00000000000d"
	eveID = "eeeeeeee-0000-4000-8000-00000000000e"
)

func newCast(t *testing.T) *cast {
	t.Helper()
	app := mountTeam(t)
	iam := planetest.ServeIdentity(t)
	iam.Named(adaID, "Ada Lovelace")
	iam.Named(bobID, "Bob Babbage")
	iam.Named(gusID, "Gus Guest")
	ctx := context.Background()
	ws, err := mounted.State.accounts.EnsureSpace(ctx, "acme", adaID, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := mounted.State.accounts.AddMember(ctx, "acme", ws.UUID, bobID, roleMember); err != nil {
		t.Fatal(err)
	}
	if err := mounted.State.accounts.AddMember(ctx, "acme", ws.UUID, gusID, roleGuest); err != nil {
		t.Fatal(err)
	}
	if _, err := mounted.State.accounts.EnsureSpace(ctx, "globex", eveID, "Globex"); err != nil {
		t.Fatal(err)
	}
	return &cast{
		app: app, org: "acme", ws: ws.UUID, iam: iam,
		ada: bearerFor(t, adaID, "acme"), bob: bearerFor(t, bobID, "acme"),
		gus: bearerFor(t, gusID, "acme"), mal: bearerFor(t, malID, "acme"),
		eve:   bearerFor(t, eveID, "globex"),
		adaID: adaID, bobID: bobID, gusID: gusID, malID: malID,
	}
}

// do drives one request and decodes a 2xx body into out (when out is non-nil).
func (c *cast) do(t *testing.T, method, path string, who map[string]string, body, out any) int {
	t.Helper()
	code, raw := call(t, c.app, method, path, who, body)
	if out != nil && code >= 200 && code < 300 && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %T: %v (%s)", method, path, out, err, raw)
		}
	}
	return code
}

// must is do that fails the test unless the status is want.
func (c *cast) must(t *testing.T, want int, method, path string, who map[string]string, body, out any) {
	t.Helper()
	code, raw := call(t, c.app, method, path, who, body)
	if code != want {
		t.Fatalf("%s %s = %d (%s), want %d", method, path, code, raw, want)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %T: %v (%s)", method, path, out, err, raw)
		}
	}
}

// q appends the space query every read names.
func (c *cast) q(path string) string { return path + "?space=" + url.QueryEscape(c.ws) }

// channel opens a channel as Ada and answers it.
func (c *cast) channel(t *testing.T, name string, private bool, members ...string) teamChannel {
	t.Helper()
	var r teamChannelOne
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/channels", c.ada,
		map[string]any{"name": name, "space": c.ws, "private": private, "members": members}, &r)
	return r.Channel
}

// say posts a message as `who` and answers it.
func (c *cast) say(t *testing.T, who map[string]string, channel, text string) teamMessage {
	t.Helper()
	var m teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/channels/"+channel+"/messages", who,
		map[string]any{"space": c.ws, "text": text}, &m)
	return m
}

// refusal is one caller who must be turned away, and how.
type refusal struct {
	name string
	who  func(c *cast) map[string]string
	want int
}

// strangers are the two refusals every addressed op must make: somebody in the
// org but not the space, and somebody in another org — both 404, so neither
// learns what exists.
var strangers = []refusal{
	{"non-member", func(c *cast) map[string]string { return c.mal }, http.StatusNotFound},
	{"cross-org", func(c *cast) map[string]string { return c.eve }, http.StatusNotFound},
}

// ctxT and errBoom keep the injected fakes short.
type ctxT = context.Context

var errBoom = errors.New("boom")

// TestNativeAppTokenIsATeamSession: an IAM access token minted for the native
// Team app is a team session with no deployment configuration — the typed ops
// resolve its caller and the event stream opens on it — while a token for some
// other app is still refused.
func TestNativeAppTokenIsATeamSession(t *testing.T) {
	iam := iamtest.New(t)
	c := newCast(t)
	mounted.State.trans.ident.verify = cloud.NewTokenValidator(oidctest.Discovery(iamtest.Issuer), oidctest.Client(iamtest.Issuer, iam.URL)).Validate
	account := enrolled(t, mounted.State.accounts, "acme", iamSub, "Nat")
	if err := mounted.State.accounts.AddMember(context.Background(), "acme", c.ws, account, roleMember); err != nil {
		t.Fatal(err)
	}
	native := homeIn("acme", iamSub)
	native.Aud = nativeAudience
	// The gateway asserts the tenant on every request it admits, the native
	// app's included; the bearer is what team verifies the session from.
	who := map[string]string{"Authorization": "Bearer " + iam.Sign(t, native), "X-Org-Id": "acme", "X-User-Id": account}

	var roster teamMembers
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/members"), who, nil, &roster)
	general := c.channel(t, "general", false, account)
	var m teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/channels/"+general.ID+"/messages", who,
		map[string]any{"space": c.ws, "text": "from the app"}, &m)
	if m.Author != account {
		t.Fatalf("author = %q, want the native caller's account %q", m.Author, account)
	}
	body := streamOnce(t, c, who, account, func() { c.say(t, c.ada, general.ID, "pushed") })
	if !strings.Contains(body, `"text":"pushed"`) {
		t.Fatalf("the native caller's stream = %q, want the pushed message", body)
	}

	foreign := homeIn("acme", iamSub)
	foreign.Aud = "hanzo-chat"
	if code := c.do(t, http.MethodGet, c.q("/v1/team/members"), map[string]string{
		"Authorization": "Bearer " + iam.Sign(t, foreign), "X-Org-Id": "acme", "X-User-Id": account}, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("a token for another app = %d, want 401", code)
	}
}
