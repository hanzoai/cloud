// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package meet

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/team/token"
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

// TestSessionRefusesACallerWithNothing pins the endpoint. The lobby names the caller's
// own spaces, so an unauthenticated read of it would be a tenant enumeration
// with no credential at all.
func TestSessionRefusesACallerWithNothing(t *testing.T) {
	app := mount(t, apiKey, apiSecret)
	for _, bearer := range []string{"", "not-a-token", "eyJhbGciOiJub25lIn0.e30."} {
		if code, _, body := read(t, app, bearer); code != http.StatusUnauthorized {
			t.Fatalf("bearer %q → %d, want 401\n%s", bearer, code, body)
		}
	}
}

// TestSessionAnswersFromTheRows: the lobby names the spaces the MEMBERSHIP
// ROWS put this caller in, and the account those rows seat them under. Nothing in
// the answer is read off the caller's token beyond the subject it attests, because
// a token that could name its own space would be naming a tenant.
func TestSessionAnswersFromTheRows(t *testing.T) {
	t.Setenv(wsEnv, "wss://live.hanzo.bot")
	app := mount(t, apiKey, apiSecret)
	tok := access(t, ada)

	code, out, body := read(t, app, tok)
	if code != http.StatusOK {
		t.Fatalf("session = %d, want 200\n%s", code, body)
	}
	if out.Identity != account {
		t.Errorf("identity = %q, want the account on the rows %q", out.Identity, account)
	}
	if out.WS != "wss://live.hanzo.bot" {
		t.Errorf("ws = %q, want the configured media address", out.WS)
	}
	if len(out.Spaces) != 1 || out.Spaces[0].UUID != spaceA {
		t.Fatalf("spaces = %+v, want exactly the space the rows name (%s)", out.Spaces, spaceA)
	}
}

// TestTheOfferAndTheGrantAgree is the property this route exists to hold: every
// space the lobby OFFERS is one getToken would actually mint for, and every
// one it withholds is one getToken would refuse. Without it a person is shown a
// room, types a name, and is told no — and the two answers drift the first time
// one of the role rules is edited alone.
func TestTheOfferAndTheGrantAgree(t *testing.T) {
	for _, tc := range []struct {
		role    string
		offered bool
	}{
		{token.RoleOwner, true},
		{token.RoleAdmin, true},
		{token.RoleMember, true},
		{token.RoleGuest, false},
		{"", false}, // a row that names no role
	} {
		app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)),
			holds(map[string]string{spaceA: tc.role}))
		tok := access(t, ada)

		code, out, body := read(t, app, tok)
		if code != http.StatusOK {
			t.Fatalf("role %q: session = %d, want 200\n%s", tc.role, code, body)
		}
		if offered := len(out.Spaces) == 1; offered != tc.offered {
			t.Errorf("role %q: offered=%v, want %v (%+v)", tc.role, offered, tc.offered, out.Spaces)
		}
		// The same caller, the same room, at the mint.
		mintCode, _ := ask(t, app, roomIn(spaceA), "", tok)
		if granted := mintCode == http.StatusOK; granted != tc.offered {
			t.Errorf("role %q: OFFER=%v but GRANT=%v — the lobby and the mint disagree",
				tc.role, tc.offered, granted)
		}
	}
}

// TestSessionOffersOnlyWhatTheCallerProved: a token bound to space A never
// yields space B. The room prefix is the whole tenant boundary, so a lobby
// that handed back a space the caller did not prove would be handing back the
// one string needed to name a room in it.
func TestSessionOffersOnlyWhatTheCallerProved(t *testing.T) {
	app := mount(t, apiKey, apiSecret)
	tok := access(t, ada)
	_, out, _ := read(t, app, tok)
	for _, w := range out.Spaces {
		if w.UUID == spaceB {
			t.Fatalf("SECURITY: the lobby named a space the caller never proved: %+v", out.Spaces)
		}
	}
}

// TestSessionIAMLaneFailsClosedWithNoAuthority mirrors the mint's posture: on the
// IAM lane the space rows live in another process, and a peer that cannot
// answer is a REFUSAL, never an empty list. The difference matters — an empty list
// renders "you have no spaces", which is a lie when the truth is "team is
// down" and would send a person to ask for an invite they already have.
func TestSessionIAMLaneFailsClosedWithNoAuthority(t *testing.T) {
	// No authority: rows() falls back to the real peer, and this process has none.
	app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)), nil)
	iamTok := access(t, ada)
	if code, _, body := read(t, app, iamTok); code != http.StatusUnauthorized {
		t.Fatalf("session with no team peer = %d, want 401 (refusal, not an empty answer)\n%s", code, body)
	}
	// The SAME token against an authority that answers IS served, so the refusal
	// above is the missing peer and not the credential.
	answering := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)), anyone())
	if code, _, body := read(t, answering, access(t, ada)); code != http.StatusOK {
		t.Fatalf("session with an answering peer = %d, want 200\n%s", code, body)
	}
}

// TestUnsetMediaAddressIsSaidPlainly. LIVEKIT_WS is a deployment fact the bundle
// deliberately does not carry, so when it is missing the honest answer is an empty
// string the client can act on — not a guessed host, and not a 503 that would also
// take out the published office client, which supplies its own address.
func TestUnsetMediaAddressIsSaidPlainly(t *testing.T) {
	t.Setenv(wsEnv, "")
	app := mount(t, apiKey, apiSecret)
	tok := access(t, ada)

	code, out, body := read(t, app, tok)
	if code != http.StatusOK {
		t.Fatalf("session = %d, want 200 — an unset media address is not a broken service\n%s", code, body)
	}
	if out.WS != "" {
		t.Errorf("ws = %q, want empty", out.WS)
	}
	// And the mint still works, because it never needed the address.
	if mintCode, _ := ask(t, app, roomIn(spaceA), "", tok); mintCode != http.StatusOK {
		t.Errorf("getToken = %d with LIVEKIT_WS unset, want 200", mintCode)
	}
}

// TestSessionNamesNoKeyMaterial. The lobby is reachable on every public host with
// an ordinary session, so it must not become the place an operator's key file, the
// Secret behind it, or the signing key's name leaks.
func TestSessionNamesNoKeyMaterial(t *testing.T) {
	t.Setenv(wsEnv, "wss://live.hanzo.bot")
	app := mount(t, apiKey, apiSecret)
	tok := access(t, ada)
	_, _, body := read(t, app, tok)
	for _, secret := range []string{apiSecret, apiKey, "keys.yaml", "livekit-keys"} {
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
	st := state{apiKey: apiKey, apiSecret: apiSecret}
	iamIssuer(t)
	for _, p := range machinePrincipals {
		if _, ok := spacesWithPrincipal(t, st, p); ok {
			t.Fatalf("SECURITY: a principal with no human subject read the lobby: %+v", p)
		}
	}
}

// TestTheClientIsNotOnThisOrigin. The call client is its own image on its own
// host (ghcr.io/hanzoai/meet at meet.hanzo.ai), so this binary serves the mint
// and nothing else — /meet and every deep link under it reach no route here.
//
// It replaces the pair that measured the opposite (the bundle answered 200 on
// /meet, /meet/ and /meet/<space>/<room>), because a deletion that nothing
// measures is a deletion that comes back.
func TestTheClientIsNotOnThisOrigin(t *testing.T) {
	app := mount(t, apiKey, apiSecret)
	for _, path := range []string{"/meet", "/meet/", "/meet/" + spaceA + "/standup"} {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404 — the API origin does not serve the client:\n%s",
				path, resp.StatusCode, truncate(string(body)))
		}
		if strings.Contains(string(body), `<div id="root">`) {
			t.Errorf("GET %s served an SPA shell; the bundle left this binary", path)
		}
	}
}

// TestLobbyShapeIsTheOneThePlaneStates. The wire's space entries ARE
// plane.Space, so the client and the process that owns the rows describe a
// space with one set of names. A second local struct here would be the drift.
func TestLobbyShapeIsTheOneThePlaneStates(t *testing.T) {
	var l lobby
	l.Spaces = []plane.Space{{UUID: "u", Name: "n", Role: "owner"}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"identity"`, `"name"`, `"ws"`, `"spaces"`, `"uuid"`, `"role"`} {
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
// disagree. A seat is taken under the account on the membership row, and mint
// refuses a row that names none — so a lobby that offered a space off such a
// row would show a room, take the choice, and then decline it.
//
// The rows are the only place this shape can come from now, which is why the
// authority states it directly: a space with a role and no account.
func TestNoAccountIsNoOffer(t *testing.T) {
	unseated := &answers{
		row: func(string, string) plane.Member {
			return plane.Member{Member: true, Role: token.RoleOwner} // no account
		},
		list: plane.Spaces{Items: []plane.Space{{UUID: spaceA, Role: token.RoleOwner}}},
	}
	app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)), unseated)
	tok := access(t, ada)

	code, out, body := read(t, app, tok)
	if code != http.StatusOK {
		t.Fatalf("session = %d, want 200 — no account is an empty lobby, not a fault\n%s", code, body)
	}
	if len(out.Spaces) != 0 {
		t.Errorf("the lobby offered %+v off a row with no account — the mint refuses it", out.Spaces)
	}
	// And the mint does refuse it, which is the agreement being pinned.
	if mintCode, mintBody := ask(t, app, roomIn(spaceA), "", tok); mintCode != http.StatusUnauthorized {
		t.Errorf("getToken on a row with no account = %d %q, want 401", mintCode, mintBody)
	}
}
