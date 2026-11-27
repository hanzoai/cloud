package provider

// plane_test.go proves the three properties the provider plane owns and no
// vendor test can: every registered provider says how it can be connected, the
// org and user planes never resolve one another's providers, and an anonymous
// caller gets a status instead of a stack trace.
//
// Every table here is derived from the registry. A hand-written list of ids
// would have to be edited each time a provider is registered, and that edit is
// exactly the one nobody makes — which is how 42 of the providers came to
// render a card with no badge and no way to connect.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// modeOrder is providerModes' fixed append order AND the whole vocabulary a card
// may carry. Asserting a provider's modes are a SUBSEQUENCE of it settles three
// things at once: the vocabulary is closed, no mode repeats, and the order is
// stable, so a card's badges do not reshuffle between reads.
var modeOrder = []string{"managed", "oauth", "apikey", "device", "mcp", "none"}

// subsequence reports whether got appears in modeOrder's order, each at most once.
func subsequence(got []string) bool {
	i := 0
	for _, m := range got {
		for i < len(modeOrder) && modeOrder[i] != m {
			i++
		}
		if i == len(modeOrder) {
			return false
		}
		i++
	}
	return true
}

// TestModesAreReadOffWhatAProviderDeclares pins the mapping, and with it the
// registry sweep below: the first case is the shape the sweep must reject, so a
// sweep that cannot fail is itself a failure here.
func TestModesAreReadOffWhatAProviderDeclares(t *testing.T) {
	configured := func() bool { return true }
	// An app credential this deployment HOLDS. Empty is a different case and has
	// its own row: managed asks the creds, so a Creds stub answering nothing is
	// not a managed provider however its Configured answers.
	creds := func() OAuthConfig { return OAuthConfig{ClientID: "an-app-we-hold"} }
	noCreds := func() OAuthConfig { return OAuthConfig{} }
	verify := func(context.Context, VerifyInput) (*ExchangeResult, error) { return nil, nil }
	for _, c := range []struct {
		name string
		p    Provider
		want []string
	}{
		{"declaring nothing offers no way in", Provider{ID: "x"}, nil},
		{"app creds with no consent flow are not managed", Provider{Creds: creds, Configured: configured}, nil},
		{"a consent flow with no app credential is not managed", Provider{Creds: noCreds, Configured: configured, Authorize: func(OAuthConfig, string, string) (string, error) { return "", nil }}, []string{"oauth"}},
		{"app creds this deployment holds, with a consent flow", Provider{Creds: creds, Configured: configured, Authorize: func(OAuthConfig, string, string) (string, error) { return "", nil }}, []string{"managed", "oauth"}},
		{"an apikey provider with no consent flow is not managed", Provider{Kind: apiKeyKind, Creds: creds, Configured: configured, Verify: verify}, []string{"apikey"}},
		{"cloudflare's shape: dual path, and managed only with an app credential", Provider{Kind: apiKeyKind, Creds: creds, Configured: configured, Authorize: func(OAuthConfig, string, string) (string, error) { return "", nil }, Verify: verify}, []string{"managed", "oauth", "apikey"}},
		{"the same dual path with no app credential offers only the token", Provider{Kind: apiKeyKind, Creds: noCreds, Configured: configured, Authorize: func(OAuthConfig, string, string) (string, error) { return "", nil }, Verify: verify}, []string{"oauth", "apikey"}},
		{"app creds nobody configured", Provider{Creds: noCreds, Configured: func() bool { return false }, Authorize: func(OAuthConfig, string, string) (string, error) { return "", nil }}, []string{"oauth"}},
		{"the org's consent leg", Provider{Authorize: func(OAuthConfig, string, string) (string, error) { return "", nil }}, []string{"oauth"}},
		{"the user plane's adopted bundle", Provider{Adopt: func(context.Context, Bundle) (*ExchangeResult, error) { return nil, nil }}, []string{"oauth"}},
		{"a credential the customer supplies", Provider{Verify: verify}, []string{"apikey"}},
		{"a keyVerify connector with no Kind", Provider{Verify: verify, Kind: ""}, []string{"apikey"}},
		{"device sign-in", Provider{Device: &Device{}}, []string{"device"}},
		{"an mcp server", Provider{Kind: mcpKind}, []string{"mcp"}},
		{"everything at once, in order", Provider{
			Creds: creds, Configured: configured, Authorize: func(OAuthConfig, string, string) (string, error) { return "", nil },
			Adopt:  func(context.Context, Bundle) (*ExchangeResult, error) { return nil, nil },
			Verify: verify, Device: &Device{}, Kind: mcpKind,
		}, modeOrder[:len(modeOrder)-1]}, // one Kind: mcp, so never none
		{"nothing to connect", Provider{Kind: noneKind}, []string{"none"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := providerModes(&c.p)
			if len(got) != len(c.want) || (len(got) > 0 && !slices.Equal(got, c.want)) {
				t.Fatalf("modes want %v, got %v", c.want, got)
			}
		})
	}
}

// TestEveryProviderSaysHowToConnectIt is the regression guard. It runs with NO
// provider credentials in env, which is the strict case: "managed" is the one
// mode this deployment's own app credentials can supply, so an unconfigured
// process drops it and what remains is what the provider itself declares. A
// provider that answers nothing here renders a card a reader cannot act on.
func TestEveryProviderSaysHowToConnectIt(t *testing.T) {
	for id, p := range registry {
		t.Run(id, func(t *testing.T) {
			modes := providerModes(p)
			if len(modes) == 0 {
				t.Fatalf("provider %q offers no way in: declare an Authorize/Adopt (oauth), a Verify (apikey), a Device, or the mcp Kind", id)
			}
			if !subsequence(modes) {
				t.Fatalf("provider %q modes %v are not %v in order, once each", id, modes, modeOrder)
			}
			if !slices.Equal(modes, providerModes(p)) {
				t.Fatalf("provider %q modes are not stable across reads: %v", id, modes)
			}
		})
	}
}

// TestBothCatalogsPublishEveryProviderWithItsModes proves the derivation reaches
// the wire on all three published catalog paths, and that the two planes between
// them account for every registered provider — the count invariant, without a
// number to keep up to date.
func TestBothCatalogsPublishEveryProviderWithItsModes(t *testing.T) {
	app := newApp(t, newKMS(t))

	cards := func(res httpResult) []providerView {
		t.Helper()
		if res.Code != http.StatusOK {
			t.Fatalf("catalog want 200, got %d (%s)", res.Code, res.Body)
		}
		var out struct {
			Providers []providerView `json:"providers"`
		}
		if err := json.Unmarshal(res.Body, &out); err != nil {
			t.Fatalf("catalog body: %v (%s)", err, res.Body)
		}
		return out.Providers
	}

	check := func(where string, card providerView, wantUser bool) {
		t.Helper()
		p, ok := registry[card.ID]
		if !ok {
			t.Fatalf("%s carries %q, which is in no registry", where, card.ID)
		}
		if (p.Scope == userScope) != wantUser {
			t.Fatalf("%s carries %q from the other plane", where, card.ID)
		}
		// A null or empty modes array is the bug this test exists for: the
		// console renders every card from this field alone.
		if len(card.Modes) == 0 {
			t.Fatalf("%s publishes %q with no modes", where, card.ID)
		}
		if !slices.Equal(card.Modes, providerModes(p)) {
			t.Fatalf("%s publishes %q as %v, derivation says %v", where, card.ID, card.Modes, providerModes(p))
		}
	}

	org := cards(req(t, app, http.MethodGet, "/v1/provider", "acme", nil))
	for _, card := range org {
		check("/v1/provider", card, false)
		// The single-provider view is its own route; it must agree with the list.
		one := req(t, app, http.MethodGet, "/v1/provider/"+card.ID, "acme", nil)
		if one.Code != http.StatusOK {
			t.Fatalf("GET /v1/provider/%s want 200, got %d (%s)", card.ID, one.Code, one.Body)
		}
		var got providerView
		if err := json.Unmarshal(one.Body, &got); err != nil {
			t.Fatalf("GET /v1/provider/%s body: %v (%s)", card.ID, err, one.Body)
		}
		if !slices.Equal(got.Modes, card.Modes) {
			t.Fatalf("GET /v1/provider/%s modes %v disagree with the list's %v", card.ID, got.Modes, card.Modes)
		}
	}

	user := cards(as(t, app, http.MethodGet, cpListPath(), "acme", userUUID, nil))
	for _, card := range user {
		check(cpListPath(), card, true)
	}

	if len(org)+len(user) != len(registry) {
		t.Fatalf("the two catalogs publish %d+%d cards for %d registered providers — one plane is dropping providers",
			len(org), len(user), len(registry))
	}
}

// TestNeitherPlaneResolvesTheOthersProviders sweeps the WHOLE registry both
// ways. A user-scoped provider must be unreachable on every org route, and an
// org provider unreachable on every user route — including the write paths,
// which are the ones that would seal a credential into the wrong custody
// subtree.
func TestNeitherPlaneResolvesTheOthersProviders(t *testing.T) {
	app := newApp(t, newKMS(t))

	type route struct {
		method, path string
		body         any
	}
	for id, p := range registry {
		t.Run(id, func(t *testing.T) {
			routes := []route{
				{http.MethodGet, "/v1/provider/" + id, nil},
				{http.MethodPost, "/v1/provider/" + id + "/connect", nil},
				{http.MethodPost, "/v1/provider/" + id + "/verify", nil},
				{http.MethodPost, "/v1/provider/" + id + "/disconnect", nil},
			}
			if p.Scope != userScope {
				routes = []route{
					{http.MethodGet, tokenPath(id + ":default"), nil},
					{http.MethodPost, refreshPath(id + ":default"), nil},
					{http.MethodDelete, connPath(id + ":default"), nil},
					{http.MethodPost, devStartPath(id), nil},
					{http.MethodPost, devPollPath(id, "deadbeef"), nil},
					{http.MethodPost, credPath(id), map[string]any{"token": "never-reaches-a-provider"}},
				}
			}
			for _, rt := range routes {
				res := as(t, app, rt.method, rt.path, "acme", userUUID, rt.body)
				if res.Code != http.StatusNotFound {
					t.Fatalf("%s %s want 404, got %d (%s)", rt.method, rt.path, res.Code, res.Body)
				}
				// The HANDLER refused it, not the router. A route that is not
				// registered at all answers 404 with the router's own "Not
				// Found", which would make this whole sweep pass vacuously.
				if !bytes.Contains(res.Body, []byte("unknown provider")) {
					t.Fatalf("%s %s answered the router's 404, not the plane's: %s", rt.method, rt.path, res.Body)
				}
			}
		})
	}
}

// TestAnonymousIsRefusedWithAStatus proves every documented route answers a
// caller with no validated principal 401 — never 5xx, and never a 404 that
// would tell an anonymous prober which ids exist. Identity is checked before
// the registry, so a known org provider, a known user provider and an id in no
// registry are indistinguishable from outside.
func TestAnonymousIsRefusedWithAStatus(t *testing.T) {
	app := newApp(t, newKMS(t))

	refused := func(method, path string) {
		t.Helper()
		res := req(t, app, method, path, "", nil)
		if res.Code >= http.StatusInternalServerError {
			t.Fatalf("%s %s anonymous must not fault: %d (%s)", method, path, res.Code, res.Body)
		}
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous want 401, got %d (%s)", method, path, res.Code, res.Body)
		}
	}

	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/provider"},
		{http.MethodGet, "/v1/connection"},
		{http.MethodGet, cpListPath()},
	} {
		refused(rt.method, rt.path)
	}

	// slack is org-scoped, openai user-scoped, and the third is in no registry.
	for _, id := range []string{"slack", "openai", "no-such-provider"} {
		for _, rt := range []struct{ method, path string }{
			{http.MethodGet, "/v1/provider/" + id},
			{http.MethodPost, "/v1/provider/" + id + "/connect"},
			{http.MethodPost, "/v1/provider/" + id + "/disconnect"},
			{http.MethodPost, "/v1/provider/" + id + "/verify"},
			{http.MethodGet, tokenPath(id + ":default")},
			{http.MethodPost, refreshPath(id + ":default")},
		} {
			refused(rt.method, rt.path)
		}
	}
}
