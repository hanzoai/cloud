// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package meet

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// spacesWithPrincipal mints an attestation directly and asks the lobby's lane
// selector what it makes of it — the read-side twin of admitsWithPrincipal, and
// for the same reason: an API key principal cannot be signed, only minted.
func spacesWithPrincipal(t *testing.T, st state, p principal.Principal) (sp plane.Spaces, ok bool) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/probe", func(c *zip.Ctx) error {
		principal.Mint(c, p)
		sp, ok = st.spaces(c)
		return c.String(http.StatusOK, "ok")
	})
	if _, err := app.Test(httptest.NewRequest(http.MethodGet, "/probe", nil)); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return sp, ok
}

// read fetches the lobby with an optional bearer and returns the status + parsed body.
// A body that does not parse is a failure at the call site, not a nil deref later.
func read(t *testing.T, app *zip.App, bearer string) (int, lobby, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/meet/session", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /v1/meet/session: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out lobby
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("session body is not the lobby shape: %v\n%s", err, b)
		}
	}
	return resp.StatusCode, out, string(b)
}

// TestSessionRefusesACallerWithNothing pins the door. The lobby names the caller's
// own workspaces, so an unauthenticated read of it would be a tenant enumeration
// with no credential at all.
func TestSessionRefusesACallerWithNothing(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	for _, bearer := range []string{"", "not-a-token", "eyJhbGciOiJub25lIn0.e30."} {
		if code, _, body := read(t, app, bearer); code != http.StatusUnauthorized {
			t.Fatalf("bearer %q → %d, want 401\n%s", bearer, code, body)
		}
	}
}

// TestSessionAnswersFromTheSignedWorkspace is the HS256 lane: the token IS the
// answer, so the workspace it names and the account it carries come straight back
// and nothing is looked up.
func TestSessionAnswersFromTheSignedWorkspace(t *testing.T) {
	t.Setenv(wsEnv, "wss://live.hanzo.bot")
	app := mount(t, teamSecret, apiKey, apiSecret)
	tok := workspaceToken(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())

	code, out, body := read(t, app, tok)
	if code != http.StatusOK {
		t.Fatalf("session = %d, want 200\n%s", code, body)
	}
	if out.Identity != account {
		t.Errorf("identity = %q, want the token's account %q", out.Identity, account)
	}
	if out.WS != "wss://live.hanzo.bot" {
		t.Errorf("ws = %q, want the configured media address", out.WS)
	}
	if len(out.Workspaces) != 1 || out.Workspaces[0].UUID != workspaceA {
		t.Fatalf("workspaces = %+v, want exactly the signed workspace %s", out.Workspaces, workspaceA)
	}
}

// TestTheOfferAndTheGrantAgree is the property this route exists to hold: every
// workspace the lobby OFFERS is one getToken would actually mint for, and every
// one it withholds is one getToken would refuse. Without it a person is shown a
// room, types a name, and is told no — and the two answers drift the first time
// one of the role rules is edited alone.
func TestTheOfferAndTheGrantAgree(t *testing.T) {
	hour := time.Now().Add(time.Hour).Unix()
	app := mount(t, teamSecret, apiKey, apiSecret)

	for _, tc := range []struct {
		role    string
		offered bool
	}{
		{token.RoleOwner, true},
		{token.RoleAdmin, true},
		{token.RoleMember, true},
		{token.RoleGuest, false},
		{"", false}, // a session that never proved a role
	} {
		tok := workspaceToken(t, workspaceA, teamSecret, map[string]any{"role": tc.role}, hour)

		code, out, body := read(t, app, tok)
		if code != http.StatusOK {
			t.Fatalf("role %q: session = %d, want 200\n%s", tc.role, code, body)
		}
		if offered := len(out.Workspaces) == 1; offered != tc.offered {
			t.Errorf("role %q: offered=%v, want %v (%+v)", tc.role, offered, tc.offered, out.Workspaces)
		}
		// The same caller, the same room, at the mint.
		mintCode, _ := ask(t, app, roomIn(workspaceA), "", tok)
		if granted := mintCode == http.StatusOK; granted != tc.offered {
			t.Errorf("role %q: OFFER=%v but GRANT=%v — the lobby and the mint disagree",
				tc.role, tc.offered, granted)
		}
	}
}

// TestSessionOffersOnlyWhatTheCallerProved: a token bound to workspace A never
// yields workspace B. The room prefix is the whole tenant boundary, so a lobby
// that handed back a workspace the caller did not prove would be handing back the
// one string needed to name a room in it.
func TestSessionOffersOnlyWhatTheCallerProved(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	tok := workspaceToken(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
	_, out, _ := read(t, app, tok)
	for _, w := range out.Workspaces {
		if w.UUID == workspaceB {
			t.Fatalf("SECURITY: the lobby named a workspace the caller never proved: %+v", out.Workspaces)
		}
	}
}

// TestSessionIAMLaneFailsClosedWithNoAuthority mirrors the mint's posture: on the
// IAM lane the workspace rows live in another process, and a peer that cannot
// answer is a REFUSAL, never an empty list. The difference matters — an empty list
// renders "you have no workspaces", which is a lie when the truth is "team is
// down" and would send a person to ask for an invite they already have.
func TestSessionIAMLaneFailsClosedWithNoAuthority(t *testing.T) {
	iss := iamIssuer(t)
	app := mount(t, teamSecret, apiKey, apiSecret)
	iamTok := iss.Sign(t, iamtest.Claims{Sub: "11111111-2222-4333-8444-555555555555", Owner: "acme"})
	if code, _, body := read(t, app, iamTok); code != http.StatusUnauthorized {
		t.Fatalf("session with no team peer = %d, want 401 (refusal, not an empty answer)\n%s", code, body)
	}
}

// TestUnsetMediaAddressIsSaidPlainly. LIVEKIT_WS is a deployment fact the bundle
// deliberately does not carry, so when it is missing the honest answer is an empty
// string the client can act on — not a guessed host, and not a 503 that would also
// take out the published office client, which supplies its own address.
func TestUnsetMediaAddressIsSaidPlainly(t *testing.T) {
	t.Setenv(wsEnv, "")
	app := mount(t, teamSecret, apiKey, apiSecret)
	tok := workspaceToken(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())

	code, out, body := read(t, app, tok)
	if code != http.StatusOK {
		t.Fatalf("session = %d, want 200 — an unset media address is not a broken service\n%s", code, body)
	}
	if out.WS != "" {
		t.Errorf("ws = %q, want empty", out.WS)
	}
	// And the mint still works, because it never needed the address.
	if mintCode, _ := ask(t, app, roomIn(workspaceA), "", tok); mintCode != http.StatusOK {
		t.Errorf("getToken = %d with LIVEKIT_WS unset, want 200", mintCode)
	}
}

// TestSessionNamesNoKeyMaterial. The lobby is reachable on every public host with
// an ordinary session, so it must not become the place an operator's key file, the
// Secret behind it, or the signing key's name leaks.
func TestSessionNamesNoKeyMaterial(t *testing.T) {
	t.Setenv(wsEnv, "wss://live.hanzo.bot")
	app := mount(t, teamSecret, apiKey, apiSecret)
	tok := workspaceToken(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
	_, _, body := read(t, app, tok)
	for _, secret := range []string{apiSecret, teamSecret, apiKey, "keys.yaml", "livekit-keys", "SERVER_SECRET"} {
		if strings.Contains(body, secret) {
			t.Errorf("the lobby body names %q:\n%s", secret, body)
		}
	}
}

// TestSpacesIsRefusedForAMachine. Same rule as the mint: a machine credential
// carries no `sub`, and "which human is in this room" is not a question an API key
// gets to ask. Pinned separately because the lobby is a READ, and a read is where
// a gate quietly gets relaxed.
func TestSpacesIsRefusedForAMachine(t *testing.T) {
	st := state{teamSecret: teamSecret, apiKey: apiKey, apiSecret: apiSecret}
	iamIssuer(t)
	for _, p := range machinePrincipals {
		if _, ok := spacesWithPrincipal(t, st, p); ok {
			t.Fatalf("SECURITY: a principal with no human subject read the lobby: %+v", p)
		}
	}
}

// TestLobbyIsServedFromTheSameOrigin proves the wiring, not the bundle: Mount puts
// the client on /meet and /meet/<deep link>, beside the API it reads. The ui
// package's own test covers what those bytes are.
func TestLobbyIsServedFromTheSameOrigin(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	for _, path := range []string{"/meet", "/meet/", "/meet/" + workspaceA + "/standup"} {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if !strings.Contains(string(body), `<div id="root">`) {
			t.Errorf("GET %s did not serve the SPA shell:\n%s", path, truncate(string(body)))
		}
	}
}

// TestTheClientIsServedEvenUnconfigured. An unconfigured deployment still serves
// the client, which then renders the refusal /v1/meet/session gives it. A 404 here
// would say nothing at all about what is wrong.
func TestTheClientIsServedEvenUnconfigured(t *testing.T) {
	app := mount(t, "", "", "")
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/meet", nil))
	if err != nil {
		t.Fatalf("GET /meet: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /meet on an unconfigured deploy = %d, want 200", resp.StatusCode)
	}
}

// TestLobbyShapeIsTheOneThePlaneStates. The wire's workspace entries ARE
// plane.Space, so the client and the process that owns the rows describe a
// workspace with one set of names. A second local struct here would be the drift.
func TestLobbyShapeIsTheOneThePlaneStates(t *testing.T) {
	var l lobby
	l.Workspaces = []plane.Space{{UUID: "u", Name: "n", Role: "owner"}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"identity"`, `"name"`, `"ws"`, `"workspaces"`, `"uuid"`, `"role"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("the lobby wire is missing %s: %s", key, b)
		}
	}
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// TestNoAccountIsNoOffer closes the last way the lobby and the mint could
// disagree. mint refuses a token that carries no account — the account IS the
// identity the seat is taken under — so a lobby that offered a workspace off one
// would show a room, take the choice, and then decline it. token.Generate cannot
// mint this shape (it validates the account as a uuid), which is exactly why the
// invariant is written down at the offer instead of inferred from the grant.
func TestNoAccountIsNoOffer(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	tok := rawToken(t, map[string]any{
		"extra":     map[string]any{"role": token.RoleOwner},
		"account":   "",
		"workspace": workspaceA,
		"exp":       time.Now().Add(time.Hour).Unix(),
	})
	if code, _, body := read(t, app, tok); code != http.StatusUnauthorized {
		t.Fatalf("session with an account-less token = %d, want 401\n%s", code, body)
	}
	// And the mint refuses it too, which is the agreement being pinned.
	if mintCode, _ := ask(t, app, roomIn(workspaceA), "", tok); mintCode != http.StatusUnauthorized {
		t.Errorf("getToken with an account-less token = %d, want 401", mintCode)
	}
}

// rawToken signs an arbitrary payload with the team secret. token.Generate
// validates its inputs, so a shape it refuses to mint can only be built here —
// and a shape nothing mints is still a shape the server must decide about.
func rawToken(t *testing.T, payload map[string]any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"HS256"}`))
	signing := head + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(teamSecret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
