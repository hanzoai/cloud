package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"

	"github.com/zap-proto/zip"
)

// A person adds a Bluesky account from the app, posts to it, adds a second, ends
// one, and the other stays: the whole life of an account through the routes the
// app calls.
func TestAnAccountIsAddedPostedToAndEnded(t *testing.T) {
	srv := pds(t)
	app := newApp(t, newKMS(t))
	connect := func(handle string, org string, admin bool) httpResult {
		return cfReq(t, app, http.MethodPost, "/v1/provider/bluesky/connect", org, admin, map[string]any{
			"fields": map[string]string{"identifier": handle, "password": goodAppPassword, "server": srv.URL},
		})
	}

	// Connecting is an org admin's act.
	if r := connect("acme.bsky.social", "acme", false); r.Code != http.StatusForbidden {
		t.Fatalf("a member connected an account: %d %s", r.Code, r.Body)
	}
	r := connect("acme.bsky.social", "acme", true)
	if r.Code != http.StatusOK {
		t.Fatalf("connect = %d %s", r.Code, r.Body)
	}
	var connected struct {
		Connected bool   `json:"connected"`
		Provider  string `json:"provider"`
		Account   string `json:"account"`
	}
	_ = json.Unmarshal(r.Body, &connected)
	if !connected.Connected || connected.Provider != "bluesky" || connected.Account != "@acme.bsky.social" {
		t.Fatalf("connected = %+v", connected)
	}
	if strings.Contains(string(r.Body), goodAppPassword) {
		t.Fatal("the app password came back")
	}

	// A wrong password says why and stores nothing.
	r = cfReq(t, app, http.MethodPost, "/v1/provider/bluesky/connect", "acme", true, map[string]any{
		"fields": map[string]string{"identifier": "acme.bsky.social", "password": "wxyz-wxyz-wxyz-wxyz", "server": srv.URL},
	})
	if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), "did not accept that handle") || strings.Contains(string(r.Body), "wxyz") {
		t.Fatalf("a wrong password = %d %s", r.Code, r.Body)
	}

	// The card lists the account, and is a credential form with its fields.
	r = cfReq(t, app, http.MethodGet, "/v1/provider/bluesky", "acme", false, nil)
	var card providerView
	if err := json.Unmarshal(r.Body, &card); err != nil {
		t.Fatalf("%v: %s", err, r.Body)
	}
	if !card.Connected || card.Kind != "credential" || !card.Available || len(card.Accounts) != 1 || len(card.Fields) != 3 {
		t.Fatalf("card = %+v", card)
	}
	if strings.Contains(string(r.Body), goodAppPassword) {
		t.Fatal("the card shows the app password")
	}
	for _, f := range card.Fields {
		if f.Name == "password" && !f.Secret {
			t.Fatal("the password field is not masked")
		}
	}

	// Posting uses the sealed password and the sealed server.
	out, err := planePost(cloud.For(context.Background(), "acme"), &client.SocialPostIn{To: "bluesky", Text: "Hello Bluesky", Account: "did:plc:abc123"})
	if err != nil || out.Error != "" || out.URL != "https://bsky.app/profile/acme.bsky.social/post/3kxyz" {
		t.Fatalf("post = %+v %v", out, err)
	}
	if got := srv.took("POST /xrpc/com.atproto.server.createSession"); got[len(got)-1].json()["password"] != goodAppPassword {
		t.Fatalf("session = %v", got[len(got)-1].json())
	}
	// Another org holds none of it.
	if out := postAs(t, "globex", "bluesky", "Theirs"); !strings.Contains(out.Error, "Bluesky is not connected") {
		t.Fatalf("another org = %+v", out)
	}

	// A second account of the same network is its own row, and a post that
	// does not name one is refused.
	srv2 := newTLSStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if a.Key == "POST /xrpc/com.atproto.server.createSession" {
			say(w, `{"did":"did:plc:second","handle":"second.bsky.social","accessJwt":"jwt-2"}`)
			return
		}
		refuse(w, 404, "no")
	})
	swap(t, &blueskyClient, srv2.Client())
	if r := cfReq(t, app, http.MethodPost, "/v1/provider/bluesky/connect", "acme", true, map[string]any{
		"fields": map[string]string{"identifier": "second.bsky.social", "password": goodAppPassword, "server": srv2.URL},
	}); r.Code != http.StatusOK {
		t.Fatalf("second connect = %d %s", r.Code, r.Body)
	}
	if got := postAs(t, "acme", "bluesky", "Which one?"); !strings.Contains(got.Error, "name the account") {
		t.Fatalf("an unnamed account among two = %+v", got)
	}

	// Ending one account leaves the other connected.
	r = cfReq(t, app, http.MethodPost, "/v1/provider/bluesky/disconnect", "acme", true, map[string]any{"account": "did:plc:second"})
	if r.Code != http.StatusOK {
		t.Fatalf("disconnect one = %d %s", r.Code, r.Body)
	}
	left := Connections("acme", "bluesky")
	if len(left) != 1 || left[0].ExternalID != "did:plc:abc123" {
		t.Fatalf("left = %+v", left)
	}
	if _, err := kmsGet(mounted, custodyPath(registry["bluesky"], "acme", keyOf("did:plc:second")), accessSecret); err == nil {
		t.Fatal("the ended account's password is still sealed")
	}
	if _, err := kmsGet(mounted, custodyPath(registry["bluesky"], "acme", keyOf("did:plc:abc123")), accessSecret); err != nil {
		t.Fatalf("the other account lost its password: %v", err)
	}
	// An account the org does not hold disconnects nothing.
	if r := cfReq(t, app, http.MethodPost, "/v1/provider/bluesky/disconnect", "acme", true, map[string]any{"account": "did:plc:nobody"}); r.Code != http.StatusOK || len(Connections("acme", "bluesky")) != 1 {
		t.Fatalf("ending an account that is not connected = %d, left %d", r.Code, len(Connections("acme", "bluesky")))
	}

	// Disconnecting with no account ends what is left.
	if r := cfReq(t, app, http.MethodPost, "/v1/provider/bluesky/disconnect", "acme", true, map[string]any{}); r.Code != http.StatusOK {
		t.Fatalf("disconnect all = %d %s", r.Code, r.Body)
	}
	if left := Connections("acme", "bluesky"); len(left) != 0 {
		t.Fatalf("left = %+v", left)
	}
}

// The social app adds an account over the plane (client.IntegrationsConnect), not
// through the provider's own route, so the plane asks who is calling the way the
// route does: connecting is an org admin's act, a member's is refused, an admin of
// another org re-pointed at this one is no admin here, and an operator acting for
// the org is no admin of it.
func TestAnOrgAdminAddsAnAccountOverThePlane(t *testing.T) {
	srv := pds(t)
	newApp(t, newKMS(t))
	in := &client.IntegrationsConnectIn{Provider: "bluesky", Fields: client.Fields(map[string]string{
		"identifier": "acme.bsky.social", "password": goodAppPassword, "server": srv.URL,
	})}
	as := func(who zip.Caller) context.Context { return zip.WithCaller(context.Background(), who) }

	for name, ctx := range map[string]context.Context{
		"a member":                       as(zip.Caller{Org: "acme", User: "u-bob"}),
		"an admin of another org":        cloud.For(as(zip.Caller{Org: "globex", User: "u-eve", OrgAdmin: true}), "acme"),
		"an operator acting for the org": as(zip.Caller{Org: "acme", User: "u-ada", OrgAdmin: true, ActedBy: "admin/op"}),
	} {
		_, err := planeConnect(ctx, in)
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("%s connected an account: %v", name, err)
		}
		if got := Connections("acme", "bluesky"); len(got) != 0 {
			t.Fatalf("%s left a connection behind: %+v", name, got)
		}
	}

	out, err := planeConnect(as(zip.Caller{Org: "acme", User: "u-ada", OrgAdmin: true}), in)
	if err != nil {
		t.Fatalf("the org's admin was refused: %v", err)
	}
	if !out.Connected || out.Provider != "bluesky" || out.Account != "@acme.bsky.social" || out.ExternalID != "did:plc:abc123" {
		t.Fatalf("connected = %+v", out)
	}
	if got := Connections("acme", "bluesky"); len(got) != 1 || got[0].ExternalID != "did:plc:abc123" {
		t.Fatalf("connections = %+v", got)
	}
}

// The credential networks are cards a person fills in; the OAuth networks are
// cards a person presses. Both are listed with the state the app draws.
func TestEverySocialNetworkIsACardWithItsState(t *testing.T) {
	t.Setenv("X_CLIENT_ID", "")
	t.Setenv("X_CLIENT_SECRET", "")
	app := newApp(t, newKMS(t))
	for _, c := range []struct{ id, kind string }{
		{"bluesky", "credential"}, {"mastodon", "credential"}, {"telegram-bot", "credential"}, {"discord-webhook", "credential"},
	} {
		r := cfReq(t, app, http.MethodGet, "/v1/provider/"+c.id, "acme", false, nil)
		var card providerView
		if err := json.Unmarshal(r.Body, &card); err != nil || r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.id, r.Code, r.Body)
		}
		if card.Kind != c.kind || !card.Available || card.Connected || len(card.Fields) == 0 || card.Category != "Social" {
			t.Errorf("%s card = %+v", c.id, card)
		}
		for _, f := range card.Fields {
			if f.Name == "" || f.Label == "" {
				t.Errorf("%s has a field with no name or label: %+v", c.id, f)
			}
		}
	}
	for _, id := range []string{"x", "facebook", "instagram", "linkedin", "tiktok", "youtube", "threads", "pinterest", "reddit"} {
		r := cfReq(t, app, http.MethodGet, "/v1/provider/"+id, "acme", false, nil)
		var card providerView
		if err := json.Unmarshal(r.Body, &card); err != nil || r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", id, r.Code, r.Body)
		}
		if card.Kind != "oauth" {
			t.Errorf("%s card = %+v", id, card)
		}
	}
}

func TestEveryNetworkTheSocialPlaneNamesHasAPosterAndAConnector(t *testing.T) {
	for id := range socialNetworks {
		p, ok := posters[id]
		if !ok {
			t.Errorf("%s is a social network with no poster", id)
			continue
		}
		if registry[p.provider] == nil || p.provider != id {
			t.Errorf("%s posts through %q, which is not its own connector", id, p.provider)
		}
	}
	for id := range posters {
		if !socialNetworks[id] {
			t.Errorf("%s has a poster but its accounts are never listed", id)
		}
	}
}
