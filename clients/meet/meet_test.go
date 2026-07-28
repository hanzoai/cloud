// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/team/token"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	teamSecret = "a-real-team-secret"
	apiKey     = "APIabcdef123456"
	apiSecret  = "a-real-livekit-secret"
	account    = "550e8400-e29b-41d4-a716-446655440000"
	workspaceA = "11111111-1111-4111-8111-111111111111"
	workspaceB = "22222222-2222-4222-8222-222222222222"
)

// roomIn builds a room name exactly as the office client does:
// "<workspaceUuid>_<roomName>_<roomId>".
func roomIn(ws string) string { return ws + "_standup_room-7" }

func mount(t *testing.T, team, key, secret string) *zip.App {
	t.Helper()
	t.Setenv("SERVER_SECRET", team)
	t.Setenv("LIVEKIT_API_KEY", key)
	t.Setenv("LIVEKIT_API_SECRET", secret)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func session(t *testing.T, ws, secret string, extra map[string]any, exp int64) string {
	t.Helper()
	tok, err := token.Generate(account, ws, extra, exp, secret)
	if err != nil {
		t.Fatalf("token.Generate: %v", err)
	}
	return tok
}

func ask(t *testing.T, app *zip.App, room, id, bearer string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(request{RoomName: room, ID: id, ParticipantName: "Ada"})
	req := httptest.NewRequest(http.MethodPost, "/v1/meet/getToken", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST /v1/meet/getToken: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// ── the token LiveKit will see ───────────────────────────────────────────────

// verify is an INDEPENDENT re-implementation of what the LiveKit server does with an
// incoming token: split it, recompute the HMAC over header.payload with the shared
// api secret, and constant-time compare. It deliberately does not call grant's
// helpers — a test that reuses the code under test to check the code under test
// proves only self-consistency. If this passes, LiveKit accepts the signature.
func verify(t *testing.T, tok, secret string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(want)) {
		t.Fatal("signature does not verify under the LiveKit api secret")
	}
	var head map[string]any
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("header not base64url: %v", err)
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatalf("header not JSON: %v", err)
	}
	if head["alg"] != "HS256" {
		t.Errorf("alg = %v, want HS256", head["alg"])
	}
	var payload map[string]any
	raw, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload not base64url: %v", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	return payload
}

// TestMintProducesVerifiableJoinToken pins EVERY claim LiveKit reads, against the
// shape in livekit/protocol auth.tokenClaims (iss=apiKey, sub=identity, iat/nbf/exp,
// name, video). A drift in any one of them is a token the media server rejects, which
// on a live cluster looks like "the call button does nothing".
func TestMintProducesVerifiableJoinToken(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	bearer := session(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
	room := roomIn(workspaceA)

	before := time.Now()
	code, tok := ask(t, app, room, "person-42", bearer)
	if code != http.StatusOK {
		t.Fatalf("mint = %d %s, want 200", code, tok)
	}
	claims := verify(t, tok, apiSecret)

	if claims["iss"] != apiKey {
		t.Errorf("iss = %v, want the api key %q", claims["iss"], apiKey)
	}
	if claims["sub"] != "person-42" {
		t.Errorf("sub = %v, want the participant identity", claims["sub"])
	}
	if claims["name"] != "Ada" {
		t.Errorf("name = %v, want Ada", claims["name"])
	}
	grant, ok := claims["video"].(map[string]any)
	if !ok {
		t.Fatalf("video grant missing: %v", claims)
	}
	if grant["roomJoin"] != true {
		t.Errorf("video.roomJoin = %v, want true", grant["roomJoin"])
	}
	if grant["room"] != room {
		t.Errorf("video.room = %v, want %q", grant["room"], room)
	}

	// TTL: ten minutes from mint, with nbf already valid.
	exp, _ := claims["exp"].(float64)
	nbf, _ := claims["nbf"].(float64)
	if d := time.Unix(int64(exp), 0).Sub(before); d < 9*time.Minute || d > 11*time.Minute {
		t.Errorf("exp is %s out, want ~10m", d)
	}
	if time.Unix(int64(nbf), 0).After(time.Now()) {
		t.Errorf("nbf %v is in the future — the token is not yet valid when issued", nbf)
	}
}

// TestGrantCarriesNoAdminPrivilege: the grant must name roomJoin and a room, and
// NOTHING else. LiveKit treats every privilege as opt-in by presence, so this is the
// least-privilege assertion — a token that never mentions roomAdmin cannot confer it,
// and a leaked join token stays a join token.
func TestGrantCarriesNoAdminPrivilege(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	bearer := session(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
	_, tok := ask(t, app, roomIn(workspaceA), "person-42", bearer)
	claims := verify(t, tok, apiSecret)

	grant := claims["video"].(map[string]any)
	for _, priv := range []string{"roomAdmin", "roomCreate", "roomList", "roomRecord", "recorder", "ingressAdmin", "agent", "hidden"} {
		if _, present := grant[priv]; present {
			t.Errorf("grant names %q; a join token must not carry it", priv)
		}
	}
	if len(grant) != 2 {
		t.Errorf("grant has %d fields (%v), want exactly roomJoin+room", len(grant), grant)
	}
	// The team session secret must never appear in something handed to a browser.
	if strings.Contains(tok, teamSecret) || strings.Contains(tok, apiSecret) {
		t.Fatal("a signing key leaked into the minted token")
	}
}

// ── admission ────────────────────────────────────────────────────────────────

// TestMintRefusals is the fail-closed table. Every row must be refused; a 200 in any
// of them is an unauthorized person in a meeting.
func TestMintRefusals(t *testing.T) {
	hour := time.Now().Add(time.Hour).Unix()
	cases := []struct {
		name   string
		room   string
		bearer func(t *testing.T) string
	}{
		{"no bearer at all", roomIn(workspaceA), func(t *testing.T) string { return "" }},
		{"not a token", roomIn(workspaceA), func(t *testing.T) string { return "not-a-jwt" }},
		{"forged: signed with another key", roomIn(workspaceA), func(t *testing.T) string {
			return session(t, workspaceA, "attacker-secret", nil, hour)
		}},
		{"expired session", roomIn(workspaceA), func(t *testing.T) string {
			return session(t, workspaceA, teamSecret, nil, time.Now().Add(-time.Hour).Unix())
		}},
		// THE tenant boundary: a real member of workspace B naming a room in
		// workspace A. Room names are client-chosen, so this is the only thing
		// stopping cross-workspace eavesdropping.
		{"member of another workspace", roomIn(workspaceA), func(t *testing.T) string {
			return session(t, workspaceB, teamSecret, nil, hour)
		}},
		{"session not bound to any workspace", roomIn(workspaceA), func(t *testing.T) string {
			return session(t, "", teamSecret, nil, hour)
		}},
		// A room name with no separator: workspace(room) is the whole string, and an
		// unbound session must still not match it.
		{"separator-less room, unbound session", "lobby", func(t *testing.T) string {
			return session(t, "", teamSecret, nil, hour)
		}},
		{"guest session", roomIn(workspaceA), func(t *testing.T) string {
			return session(t, workspaceA, teamSecret, map[string]any{"guest": "true"}, hour)
		}},
		{"readonly session", roomIn(workspaceA), func(t *testing.T) string {
			return session(t, workspaceA, teamSecret, map[string]any{"readonly": "true"}, hour)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := mount(t, teamSecret, apiKey, apiSecret)
			code, body := ask(t, app, c.room, "person-42", c.bearer(t))
			if code != http.StatusUnauthorized {
				t.Fatalf("got %d %q, want 401", code, body)
			}
		})
	}
}

// TestMintRejectsPublicDefaultTeamSecret: with the upstream default "secret" as the
// team key, anyone can mint a session naming any workspace — so the subsystem must
// treat that key as absent and refuse everything (503), not verify against it.
func TestMintRejectsPublicDefaultTeamSecret(t *testing.T) {
	app := mount(t, "secret", apiKey, apiSecret)
	bearer := session(t, workspaceA, "secret", nil, time.Now().Add(time.Hour).Unix())
	if code, body := ask(t, app, roomIn(workspaceA), "person-42", bearer); code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %q, want 503 — the public default key must never verify a caller", code, body)
	}
}

// TestMintFailsClosedUnconfigured: a missing key of any kind is a 503, and the route
// still exists (never a 404) so the failure is attributable to this subsystem.
func TestMintFailsClosedUnconfigured(t *testing.T) {
	cases := []struct{ name, team, key, secret string }{
		{"no team secret", "", apiKey, apiSecret},
		{"no livekit key", teamSecret, "", apiSecret},
		{"no livekit secret", teamSecret, apiKey, ""},
		{"nothing configured", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := mount(t, c.team, c.key, c.secret)
			bearer := session(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
			code, body := ask(t, app, roomIn(workspaceA), "person-42", bearer)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("got %d %q, want 503", code, body)
			}
		})
	}
}

// TestMintRequiresRoomAndIdentity: LiveKit refuses a join grant with no identity, so
// an empty _id is a 400 here rather than a token that cannot work. Both checks run
// BEFORE admission, so a malformed request never reaches the verifier.
func TestMintRequiresRoomAndIdentity(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	bearer := session(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
	if code, _ := ask(t, app, "", "person-42", bearer); code != http.StatusBadRequest {
		t.Errorf("empty roomName = %d, want 400", code)
	}
	if code, _ := ask(t, app, roomIn(workspaceA), "", bearer); code != http.StatusBadRequest {
		t.Errorf("empty _id = %d, want 400", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/meet/getToken", strings.NewReader("not json"))
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, _ := app.Fiber().Test(req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", resp.StatusCode)
	}
}

// TestWorkspaceOfRoom pins the room→workspace parse against the client's format.
func TestWorkspaceOfRoom(t *testing.T) {
	cases := map[string]string{
		workspaceA + "_standup_room-7": workspaceA,
		workspaceA + "_a_b_c":          workspaceA, // extra separators stay in the room part
		"lobby":                        "lobby",    // no separator: the whole name
		"":                             "",
	}
	for room, want := range cases {
		if got := workspace(room); got != want {
			t.Errorf("workspace(%q) = %q, want %q", room, got, want)
		}
	}
}
