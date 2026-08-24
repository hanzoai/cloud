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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	accountapp "github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	apiKey     = "APIabcdef123456"
	apiSecret  = "a-real-livekit-secret"
	account    = "550e8400-e29b-41d4-a716-446655440000"
	workspaceA = "11111111-1111-4111-8111-111111111111"
	workspaceB = "22222222-2222-4222-8222-222222222222"
	// org is the tenant a session names and the one the rows are read under; ada
	// is the IAM `sub` those rows are about. They are separate values because they
	// are separate facts: the org scopes the read, the subject identifies who.
	org = "acme"
	ada = "ada@acme.test"
	bob = "bob@acme.test"
)

// roomIn builds a room name exactly as the office client does:
// "<workspaceUuid>_<roomName>_<roomId>".
func roomIn(ws string) string { return ws + "_standup_room-7" }

// keyFileWith writes a LiveKit key file with the given raw body and returns its path.
func keyFileWith(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return p
}

// keyBody renders a one-pair LiveKit key file, or the EMPTY file that models a mounted
// Secret with no key in it. Separate from mount because a test that asserts on the key
// file's PATH has to create the file itself, and rebuilding this two-line rule at that
// call site is how the two drift.
func keyBody(key, secret string) string {
	if key == "" && secret == "" {
		return ""
	}
	return key + ": " + secret + "\n"
}

// mount stands the subsystem up against a real key FILE, which is how production
// reads it (Secret livekit-keys/keys.yaml, mounted read-only) — not against env
// scalars, which is the shape that turned out not to exist in the cluster.
func mount(t *testing.T, key, secret string) *zip.App {
	t.Helper()
	return mountWithKeyFile(t, keyFileWith(t, keyBody(key, secret)))
}

func mountWithKeyFile(t *testing.T, path string) *zip.App {
	t.Helper()
	return mountWith(t, path, holds(map[string]string{workspaceA: token.RoleMember}))
}

// mountWith serves a state carrying the given workspace authority, in front of
// the REAL identity boundary.
//
// Both are the fixture, and neither is optional. meet holds no key that verifies
// a caller — IAM does — so an app mounted with no boundary can only ever refuse;
// and the membership rows live in apps/team, so an app mounted with no authority
// refuses everyone the boundary admits. An endpoint test that omitted either would
// pass on a 401 it never earned.
func mountWith(t *testing.T, path string, rows roster) *zip.App {
	t.Helper()
	iamIssuer(t)
	t.Setenv(keyFileEnv, path)
	st := load()
	st.authority = rows
	sharedKey(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: iamtest.Issuer, JWKSURL: jwksURL}))
	app.Use(cloud.Bridge())
	if err := serve(app, cloud.Deps{}, st); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return app
}

// access mints the IAM access token a signed-in person actually arrives with:
// signed by the per-test issuer and verified by cloud's own validator, so the
// claim mapping being exercised is the production one rather than a stub's idea
// of it.
func access(t *testing.T, sub string) string {
	t.Helper()
	if issuer == nil {
		t.Fatal("no issuer — mount (or iamIssuer) has not run, so this token verifies against nothing")
	}
	return issuer.Sign(t, iamtest.Claims{Sub: sub, Owner: org, Orgs: homeOrg})
}

// homeOrg is the signed membership set whose FIRST entry is the caller's home org.
// The boundary refuses org scope without it — a token that names no home is what a
// machine credential looks like — so it is part of what "a signed-in person" means
// here rather than an extra a test may forget.
var homeOrg = []map[string]any{{"org": org, "role": "member"}}

func ask(t *testing.T, app *zip.App, room, id, bearer string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(request{RoomName: room, ID: id, ParticipantName: "Ada"})
	req := httptest.NewRequest(http.MethodPost, "/v1/meet/getToken", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(req)
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
	app := mount(t, apiKey, apiSecret)
	bearer := access(t, ada)
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
	if claims["sub"] != account {
		t.Errorf("sub = %v, want the SIGNED account %q (never the body's _id)", claims["sub"], account)
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
	app := mount(t, apiKey, apiSecret)
	bearer := access(t, ada)
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
	// The signing key must never appear in something handed to a browser.
	if strings.Contains(tok, apiSecret) {
		t.Fatal("a signing key leaked into the minted token")
	}
}

// ── admission ────────────────────────────────────────────────────────────────

// TestMintRefusals is the fail-closed table. Every row must be refused; a 200 in any
// of them is an unauthorized person in a meeting.
//
// Two things decide a seat now and they are in two different processes: IAM's
// signature says WHO the caller is, and the workspace rows say what they may do.
// Each row below breaks exactly one of them, and the positive control at the end is
// what keeps the whole table from passing because nothing is ever admitted.
func TestMintRefusals(t *testing.T) {
	member := func(t *testing.T) string { return access(t, ada) }
	cases := []struct {
		name   string
		room   string
		rows   roster // nil ⇒ the ordinary deployment: ada is a member of workspace A
		bearer func(t *testing.T) string
	}{
		{"no bearer at all", roomIn(workspaceA), nil, func(*testing.T) string { return "" }},
		{"not a token", roomIn(workspaceA), nil, func(*testing.T) string { return "not-a-jwt" }},
		// Signed by an issuer this deployment publishes no keys for. IAM's signature
		// is the ONLY thing that can produce a principal here, so this is the whole
		// forgery surface — there is no second key that also admits.
		{"forged: signed by another issuer", roomIn(workspaceA), anyone(), func(t *testing.T) string {
			return iamtest.New(t).Sign(t, iamtest.Claims{Sub: ada, Owner: org, Orgs: homeOrg})
		}},
		{"expired session", roomIn(workspaceA), anyone(), func(t *testing.T) string {
			return issuer.Sign(t, iamtest.Claims{Sub: ada, Owner: org, Orgs: homeOrg, Exp: time.Now().Add(-time.Hour)})
		}},
		// A MACHINE credential: an org and a user, and no `sub`. LiveKit seats
		// whatever identity it is handed and EVICTS the live participant on a
		// duplicate, so "which human is in this room" is not a question an API key
		// gets to ask. The authority says yes to everything, so only the rule refuses.
		{"machine credential carries no subject", roomIn(workspaceA), anyone(), func(t *testing.T) string {
			return issuer.Sign(t, iamtest.Claims{Owner: org, Orgs: homeOrg, PreferredUsername: "sk-key-user"})
		}},
		// THE tenant boundary: the rows put ada in workspace A and the room names B.
		// Room names are client-chosen, so this is the only thing stopping
		// cross-workspace eavesdropping.
		{"member of another workspace", roomIn(workspaceB), nil, member},
		// A leading segment that is empty, and a name with no separator at all.
		// Neither names a workspace this caller holds.
		{"room with an empty workspace segment", "_standup_1", nil, member},
		{"separator-less room", "lobby", nil, member},
		// The role is the ROWS' answer now, never a claim the caller carried. A guest
		// is a reduced principal, and a seat in a colleague's meeting is not a
		// reduced-session privilege.
		{"guest role", roomIn(workspaceA), holds(map[string]string{workspaceA: token.RoleGuest}), member},
		// FAIL-CLOSED on an unproven role: a row that names none has not shown this
		// caller is a member.
		{"no role on the row", roomIn(workspaceA), holds(map[string]string{workspaceA: ""}), member},
		{"unknown future role", roomIn(workspaceA), holds(map[string]string{workspaceA: "observer"}), member},
		// The authority ANSWERED, and the answer was no row.
		{"not a member of anything", roomIn(workspaceA), holds(nil), member},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := c.rows
			if rows == nil {
				rows = holds(map[string]string{workspaceA: token.RoleMember})
			}
			app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)), rows)
			code, body := ask(t, app, c.room, "person-42", c.bearer(t))
			if code != http.StatusUnauthorized {
				t.Fatalf("got %d %q, want 401", code, body)
			}
		})
	}
	// The positive control. Without it every row above passes on a deployment that
	// admits nobody at all, which is precisely what this fixture looked like before
	// it had an authority to answer.
	t.Run("control: the ordinary member IS admitted", func(t *testing.T) {
		app := mount(t, apiKey, apiSecret)
		if code, body := ask(t, app, roomIn(workspaceA), "person-42", access(t, ada)); code != http.StatusOK {
			t.Fatalf("got %d %q, want 200 — the refusals above prove nothing if nobody is ever admitted", code, body)
		}
	})
}

// TestTheSecondBearerAuthorityIsClosed.
//
// meet used to verify its callers itself, against apps/team's HS256 session key.
// A token naming a workspace and a role WAS the authorization, so anyone holding
// that one symmetric key — apps/team, anything with the Secret, anything that ever
// logged it — could mint a join token for any room in any tenant. The key is gone
// from this package and so is the lane.
//
// The old suite asserted such a token was ADMITTED; these are the same tokens at
// the same two endpoints with the opposite expectation, so restoring the lane fails
// here instead of passing.
func TestTheSecondBearerAuthorityIsClosed(t *testing.T) {
	app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)), anyone())
	hour := time.Now().Add(time.Hour).Unix()
	for _, secret := range []string{"a-real-team-secret", "secret"} {
		tok, err := token.Generate(account, workspaceA, map[string]any{"role": token.RoleOwner}, hour, secret)
		if err != nil {
			t.Fatalf("token.Generate: %v", err)
		}
		if code, body := ask(t, app, roomIn(workspaceA), "person-42", tok); code != http.StatusUnauthorized {
			t.Fatalf("SECURITY: an HS256 workspace session minted a join token: %d %q", code, body)
		}
		if code, _, body := read(t, app, tok); code != http.StatusUnauthorized {
			t.Fatalf("SECURITY: an HS256 workspace session read the lobby: %d %q", code, body)
		}
	}
}

// TestMintFailsClosedUnconfigured: a missing key is a 503, and the route still
// exists (never a 404) so the failure is attributable to this subsystem.
func TestMintFailsClosedUnconfigured(t *testing.T) {
	cases := []struct{ name, key, secret string }{
		{"empty key file", "", ""},
		{"api key with an empty secret", apiKey, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := mount(t, c.key, c.secret)
			bearer := access(t, ada)
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
	app := mount(t, apiKey, apiSecret)
	bearer := access(t, ada)
	if code, _ := ask(t, app, "", "person-42", bearer); code != http.StatusBadRequest {
		t.Errorf("empty roomName = %d, want 400", code)
	}
	// An empty _id is NO LONGER an error: the identity comes from the token, so the
	// body's person ref is ignored entirely (see TestIdentityComesFromTheToken).
	if code, _ := ask(t, app, roomIn(workspaceA), "", bearer); code != http.StatusOK {
		t.Errorf("empty _id = %d, want 200 — the body's _id is not load-bearing", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/meet/getToken", strings.NewReader("not json"))
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, _ := app.Test(req)
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

// ── the signing material ─────────────────────────────────────────────────────

// TestKeyFileIsTheLiveKitFormat: the file is a YAML apiKey->apiSecret map, which is
// what the LiveKit server's --key-file takes. Reading the SAME file the server
// validates against is the whole point — one representation cannot drift out of sync
// with itself, and a second copy in env would mint tokens that verify against nothing.
func TestKeyFileIsTheLiveKitFormat(t *testing.T) {
	// Comments and surrounding blank lines are normal in a real key file.
	path := keyFileWith(t, "# livekit keys\n\n"+apiKey+": "+apiSecret+"\n")
	key, secret, err := readKeys(path)
	if err != nil {
		t.Fatalf("readKeys: %v", err)
	}
	if key != apiKey || secret != apiSecret {
		t.Fatalf("readKeys = (%q,%q), want (%q,%q)", key, secret, apiKey, apiSecret)
	}
	// And it mints against that pair end to end.
	app := mountWithKeyFile(t, path)
	bearer := access(t, ada)
	code, tok := ask(t, app, roomIn(workspaceA), "person-42", bearer)
	if code != http.StatusOK {
		t.Fatalf("mint = %d %s, want 200", code, tok)
	}
	if claims := verify(t, tok, apiSecret); claims["iss"] != apiKey {
		t.Errorf("iss = %v, want the api key from the file", claims["iss"])
	}
}

// TestKeyFileRefusals is the LOUD-failure table. Every row must fail with a reason
// that names the file and the Secret, because the alternative — the bare zero value
// this used to return — is a permanent 503 with nothing in the log to chase. That is
// exactly how a Secret that is EMPTY in the cluster nearly shipped.
func TestKeyFileRefusals(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty file", ""},
		{"comments only", "# nothing here\n"},
		{"api key with no secret", apiKey + ": \"\"\n"},
		{"secret with no api key", "\"\": " + apiSecret + "\n"},
		// AMBIGUOUS: map iteration is random, so picking one would choose differently
		// per process start and fail at the media edge intermittently.
		{"two api keys", apiKey + ": " + apiSecret + "\nAPIsecond: another-secret\n"},
		{"not a map", "- just\n- a list\n"},
		{"whitespace-only secret", apiKey + ": \"   \"\n"},
		// RESTORED. This case failed once and the failure was information: it proved
		// the parser was coercing scalars. Deleting it (and keeping a comment that
		// claimed the opposite) turned a caught bug into a false assurance. With
		// yaml.v3 a duplicated key is a hard error, which is what the LiveKit server
		// does with the same bytes.
		{"duplicate api key", apiKey + ": v1\n" + apiKey + ": v2\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := keyFileWith(t, c.body)
			key, secret, err := readKeys(path)
			if err == nil {
				t.Fatalf("readKeys accepted %q -> (%q,%q); want a refusal", c.body, key, secret)
			}
			// The reason has to be actionable: it names the file AND the Secret.
			for _, want := range []string{path, "livekit-keys", "keys.yaml"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("reason %q does not name %q", err, want)
				}
			}
			// And it must land as an unusable state, not a half-configured one.
			st := state{reason: err.Error()}
			if st.ready() {
				t.Error("a state with a reason reports ready")
			}
		})
	}
}

// TestMissingKeyFileIsLoud: the file absent entirely (Secret not mounted, or mounted
// optional and not present) must name the path and the Secret, not fail silently.
func TestMissingKeyFileIsLoud(t *testing.T) {
	t.Setenv(keyFileEnv, filepath.Join(t.TempDir(), "absent", "keys.yaml"))
	st := load()
	if st.ready() {
		t.Fatal("load() reports ready with no key file")
	}
	for _, want := range []string{"livekit-keys", "keys.yaml", "cannot read"} {
		if !strings.Contains(st.reason, want) {
			t.Errorf("reason %q does not mention %q", st.reason, want)
		}
	}
}

// TestUnconfiguredReasonNeverReachesTheCaller: the 503 is reachable with no
// credential at all, so it must state the fact and not enumerate our secret plumbing.
// The reason belongs in the operator's log, which Mount writes.
//
// The leak set asserts the WHOLE reason string, not a list of fragments it happens to
// contain today. Fragments are a proxy that a reworded reason silently escapes; the
// reason itself is the actual property ("this string does not reach the caller") and it
// holds for wordings nobody has written yet. The fragments stay as well, because they
// also catch a body that assembles the plumbing without quoting the reason verbatim.
//
// `t.TempDir()` used to sit in this set and asserted NOTHING: called here it mints a
// FRESH directory, never the one holding keys.yaml, so the element could not fail. An
// assertion that cannot fail is worse than a missing one — it reads as coverage. The
// non-empty guard below is what stops the same class of bug returning, because the
// dangerous shape of this test is a reason that is "" (strings.Contains(body, "") is
// always true, so an empty reason would flip it from vacuous to always-failing).
func TestUnconfiguredReasonNeverReachesTheCaller(t *testing.T) {
	path := keyFileWith(t, "")
	app := mountWithKeyFile(t, path)
	reason := load().reason // same env as the mount above, so this IS the reason it logged
	if reason == "" {
		t.Fatal("the fixture is CONFIGURED — there is no reason to withhold and this test proves nothing")
	}
	code, body := ask(t, app, roomIn(workspaceA), "person-42", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", code)
	}
	for _, leak := range []string{"livekit-keys", "keys.yaml", "/etc/", path, reason} {
		if strings.Contains(body, leak) {
			t.Errorf("the 503 body leaks %q: %s", leak, body)
		}
	}
	if !strings.Contains(strings.ToLower(body), "not configured") {
		t.Errorf("the 503 body does not say the office is not configured: %s", body)
	}
}

// TestGrantRefusesEmptySigningKey is the crypto-boundary assertion, and it is NOT
// redundant with the ready() gate. crypto/hmac accepts an empty key and returns a
// well-formed MAC, so without this check an empty signing key produces a token that
// LOOKS correct, verifies under the empty key, and is refused by LiveKit — the exact
// silent degradation an empty Secret in the cluster would have caused. Blanking the
// key must be an error, never a token.
func TestGrantRefusesEmptySigningKey(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		st   state
	}{
		{"both empty", state{}},
		{"empty secret", state{apiKey: apiKey}},
		{"empty api key", state{apiSecret: apiSecret}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, err := c.st.grant(roomIn(workspaceA), "person-42", "Ada", now)
			if err == nil {
				t.Fatalf("grant minted %q with empty signing material; want a refusal", tok)
			}
			if tok != "" {
				t.Errorf("grant returned a token alongside its error: %q", tok)
			}
		})
	}
	// The positive control: the SAME call with real material does mint, so the test
	// above is discriminating between empty and present — not just always failing.
	st := state{apiKey: apiKey, apiSecret: apiSecret}
	if tok, err := st.grant(roomIn(workspaceA), "person-42", "Ada", now); err != nil || tok == "" {
		t.Fatalf("grant with real material = (%q, %v), want a token", tok, err)
	}
}

// TestKeyFileValuesAreByteExact is the property that decides whether a minted token
// verifies: the pair we sign with must be identical to the pair the LiveKit server
// read from the same bytes. So readKeys must NOT normalize — no trimming, no
// re-casing, no unquoting beyond what YAML itself does.
//
// This is the assertion that would have caught the TrimSpace I originally wrote: the
// api key is LiveKit's `iss` and the secret IS the HMAC key, so a single stripped
// space mints a token that looks perfect and verifies nowhere.
//
// The parser is gopkg.in/yaml.v3 into map[string]string — the exact library and target
// type the LiveKit server uses (livekit/pkg/config), so byte-exactness is by
// construction rather than by hope. It matters: measured on real input,
// sigs.k8s.io/yaml turned 0123456789 into "1.2345679e+08", yes into "true", 0x1f into
// "31", and silently kept the LAST of a duplicated key. Every one of those mints a
// token that verifies nowhere while the boot log says "mounted".
func TestKeyFileValuesAreByteExact(t *testing.T) {
	cases := []struct{ name, body, wantKey, wantSecret string }{
		{"plain scalars", "K: abc123\n", "K", "abc123"},
		{"quoted, internal spaces preserved", "K: \"a b c\"\n", "K", "a b c"},
		{"quoted, TRAILING space preserved", "K: \"abc \"\n", "K", "abc "},
		{"quoted, LEADING space preserved", "K: \" abc\"\n", "K", " abc"},
		{"base64-ish with padding", "APIxY9: aGVsbG8td29ybGQ=\n", "APIxY9", "aGVsbG8td29ybGQ="},
		{"secret containing a colon", "K: \"a:b\"\n", "K", "a:b"},
		// The scalars sigs.k8s.io/yaml mangled. Each of these is a token that would
		// have minted cleanly and verified nowhere.
		{"leading-zero digits stay a string", "K: 0123456789\n", "K", "0123456789"},
		{"yes is not a bool", "K: yes\n", "K", "yes"},
		{"no is not a bool", "K: no\n", "K", "no"},
		{"exponent notation stays literal", "K: 1e5\n", "K", "1e5"},
		{"hex notation stays literal", "K: 0x1f\n", "K", "0x1f"},
		{"double zero stays literal", "K: 00\n", "K", "00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, secret, err := readKeys(keyFileWith(t, c.body))
			if err != nil {
				t.Fatalf("readKeys: %v", err)
			}
			if key != c.wantKey {
				t.Errorf("api key = %q, want %q (byte-exact)", key, c.wantKey)
			}
			if secret != c.wantSecret {
				t.Errorf("api secret = %q, want %q (byte-exact)", secret, c.wantSecret)
			}
		})
	}
}

// TestSigningUsesTheFilesSecretVerbatim closes the loop end to end: a secret with a
// trailing space must sign with THAT secret, so a verifier holding the untrimmed value
// accepts and one holding the trimmed value does not.
func TestSigningUsesTheFilesSecretVerbatim(t *testing.T) {
	const padded = "sekrit-with-trailing-space "
	app := mountWithKeyFile(t, keyFileWith(t, apiKey+": \""+padded+"\"\n"))
	bearer := access(t, ada)
	code, tok := ask(t, app, roomIn(workspaceA), "person-42", bearer)
	if code != http.StatusOK {
		t.Fatalf("mint = %d %s, want 200", code, tok)
	}
	verify(t, tok, padded) // fatals unless the untrimmed secret is the signing key
	// And the trimmed variant must NOT verify — otherwise this test proves nothing.
	parts := strings.Split(tok, ".")
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(padded)))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if hmac.Equal([]byte(parts[2]), []byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))) {
		t.Fatal("the trimmed secret also verifies — the test cannot distinguish trimming")
	}
}

// ── the tenant boundary, at the level that enforces it ───────────────────────

// admitsOn drives one request through THE REAL IDENTITY BOUNDARY and runs
// st.admits against its live context — which is pooled and recycled the moment the
// handler returns, so the call has to happen inside it.
//
// boundary selects what a test is modelling, and the distinction is the whole
// point of these cases:
//
//   - true  — cloud.IdentityMiddleware installed, exactly as Serve installs it.
//     Client-sent identity headers are STRIPPED and the attestation is minted from
//     the token or not at all.
//   - false — no boundary, which is what apps/meet's own plugin main actually runs.
//     Nothing strips anything, so every identity header on the wire is the
//     client's. A lane that reads one here is reading whatever was typed.
func admitsOn(t *testing.T, st state, room, auth string, headers map[string]string, boundary bool) (j joiner, ok bool) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if boundary {
		app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: iamtest.Issuer, JWKSURL: jwksURL}))
	}
	app.Use(cloud.Bridge())
	app.Get("/probe", func(c *zip.Ctx) error {
		j, ok = st.admits(c, room)
		return c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return j, ok
}

// jwksURL is where the per-test issuer publishes, and issuer is the issuer itself.
// Package-level rather than threaded through every helper because these tests set
// environment (t.Setenv), which forbids t.Parallel — so exactly one issuer is alive
// at a time and the pair cannot be read from the wrong test.
var (
	jwksURL string
	issuer  *iamtest.Issuer0
)

// iamIssuer stands up the signing issuer these tests mint IAM tokens with.
func iamIssuer(t *testing.T) *iamtest.Issuer0 {
	t.Helper()
	iss := iamtest.New(t)
	jwksURL, issuer = iss.URL, iss
	t.Cleanup(func() { jwksURL, issuer = "", nil })
	return iss
}

// TestAdmitsAsksAboutTheRoomsOwnWorkspace tests `admits`, NOT the workspace()
// helper. That distinction is the whole point: TestWorkspaceOfRoom pins the parse
// in isolation and constrains nothing about how admits USES it, so mutating the
// comparison from exact-segment to prefix survived the entire suite.
//
// Room names are chosen by the client, so the segment admits asks the rows about is
// the ONLY thing standing between a workspace member and a room in someone else's
// workspace. Asserting WHICH workspace was asked about is what pins that: a prefix
// match would ask about the caller's own workspace for every room whose name merely
// begins with it.
func TestAdmitsAsksAboutTheRoomsOwnWorkspace(t *testing.T) {
	iamIssuer(t)
	member := "Bearer " + access(t, ada)
	// A UUID cannot be a proper prefix of another UUID, but the room's segment 0 is
	// arbitrary client text. A PREFIX comparison would admit all of these.
	for _, room := range []string{
		workspaceA + "x_standup_1",             // one extra char before the separator
		workspaceA + "-evil_standup_1",         // suffixed segment
		workspaceA + workspaceA + "_standup_1", // segment 0 starts with the real uuid
	} {
		a := holds(map[string]string{workspaceA: token.RoleMember})
		st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
		if _, ok := admitsOn(t, st, room, member, nil, true); ok {
			t.Errorf("admitted room %q for workspace %q — segment 0 is not an exact match", room, workspaceA)
		}
		if a.saw == workspaceA {
			t.Errorf("room %q made the rows answer about %q — the room's segment is not what was asked",
				room, workspaceA)
		}
	}
	// The exact segment is admitted, so the test discriminates rather than always failing.
	a := holds(map[string]string{workspaceA: token.RoleMember})
	st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
	if _, ok := admitsOn(t, st, roomIn(workspaceA), member, nil, true); !ok {
		t.Fatal("refused the exact-workspace room; the check is not discriminating")
	}
	// And the converse direction: a member of A cannot enter B's room.
	if _, ok := admitsOn(t, st, roomIn(workspaceB), member, nil, true); ok {
		t.Error("a member of workspace A was admitted to a workspace B room")
	}
}

// TestAdmitsRefusesARoomThatNamesNoWorkspace is load-bearing on its own: without
// the workspace=="" refusal, any room whose name STARTS with '_' has an empty
// leading segment, and an authority asked about the workspace named "" could answer
// for it.
//
// The authority here says YES to everything, so the refusal can only come from
// meet's own rule — and the ask count proves it happened BEFORE the rows were
// consulted, which is the difference between refusing and refusing for the right
// reason.
func TestAdmitsRefusesARoomThatNamesNoWorkspace(t *testing.T) {
	iamIssuer(t)
	member := "Bearer " + access(t, ada)
	for _, room := range []string{"_standup_1", "_", "_anything"} {
		a := anyone()
		st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
		if _, ok := admitsOn(t, st, room, member, nil, true); ok {
			t.Errorf("a room with an empty workspace segment was admitted: %q", room)
		}
		if a.asked != 0 {
			t.Errorf("room %q reached the rows; the empty-segment rule is not what refused it", room)
		}
	}
	// A real room IS admitted by that same authority, so the refusals are about the
	// name and not about rooms in general.
	st := state{apiKey: apiKey, apiSecret: apiSecret, authority: anyone()}
	if _, ok := admitsOn(t, st, roomIn(workspaceA), member, nil, true); !ok {
		t.Fatal("a member was refused a real room; the test is not discriminating")
	}
}

// TestMultipleApiKeysSelectByName: a LiveKit key file is a map because a server may
// hold several keys. Ambiguity is refused, but LIVEKIT_API_KEY resolves it — so a
// multi-key file is an operator setting, not a permanent outage.
func TestMultipleApiKeysSelectByName(t *testing.T) {
	body := "APIfirst: secret-one\nAPIsecond: secret-two\n"
	path := keyFileWith(t, body)

	// No selector ⇒ refused, and the message lists what is available AND names the
	// env var to set, so the log is actionable rather than just negative.
	_, _, err := readKeys(path)
	if err == nil {
		t.Fatal("a two-key file was accepted with no selector")
	}
	for _, want := range []string{"APIfirst", "APIsecond", apiKeyEnv, "livekit-keys"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}

	// Selector ⇒ that exact pair, and signing uses it end to end.
	t.Setenv(apiKeyEnv, "APIsecond")
	key, secret, err := readKeys(path)
	if err != nil {
		t.Fatalf("readKeys with a selector: %v", err)
	}
	if key != "APIsecond" || secret != "secret-two" {
		t.Fatalf("selected (%q,%q), want (APIsecond, secret-two)", key, secret)
	}
	app := mountWithKeyFile(t, path)
	bearer := access(t, ada)
	code, tok := ask(t, app, roomIn(workspaceA), "person-42", bearer)
	if code != http.StatusOK {
		t.Fatalf("mint = %d %s, want 200", code, tok)
	}
	if claims := verify(t, tok, "secret-two"); claims["iss"] != "APIsecond" {
		t.Errorf("iss = %v, want APIsecond", claims["iss"])
	}

	// A selector naming a key the file does not have is refused, and the refusal must
	// SAY SO. Dropping the membership check does not open a hole — the blank-value
	// check catches it downstream — but it degrades the message to a generic "empty api
	// key or secret", which sends an operator hunting the Secret's contents instead of
	// the one env var that is wrong. Asserting the message keeps the diagnostic honest,
	// and is what makes that mutation observable at all.
	t.Setenv(apiKeyEnv, "APIabsent")
	_, _, err = readKeys(path)
	if err == nil {
		t.Fatal("a selector naming an absent key was accepted")
	}
	for _, want := range []string{apiKeyEnv, "APIabsent", "APIfirst", "APIsecond"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q — an operator cannot tell which knob is wrong", err, want)
		}
	}
}

// TestHealthSurfacesDegradation: "meet is unconfigured" has to be a dashboard fact, not
// a grep of a rotated boot log — and ready:false IS that fact. This test asserted the
// health body also carried state.reason, which named the key-file path and the Secret on
// an endpoint that takes no credential and answers on five public hosts. That was a leak
// and a second posture in a file that deliberately keeps the reason out of the getToken
// 503; the reason belongs in the boot log, which Mount writes at ERROR.
func TestHealthSurfacesDegradation(t *testing.T) {
	app := mount(t, "", "")
	req := httptest.NewRequest(http.MethodGet, "/v1/meet/health", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /v1/meet/health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("health = %d, want 503 when unconfigured", resp.StatusCode)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("health body not JSON: %s", b)
	}
	if got["ready"] != false || got["status"] != "degraded" {
		t.Errorf("health = %v, want ready:false status:degraded", got)
	}
	// The reason must NOT be here (TestHealthLeaksNothingUnauthenticated covers the
	// full leak set); ready:false is the whole signal a probe or dashboard needs.
	if _, present := got["error"]; present {
		t.Errorf("health body carries the internal reason on an unauthenticated endpoint: %v", got)
	}
	// Configured ⇒ 200 + ready, so the probe distinguishes.
	ok := mount(t, apiKey, apiSecret)
	req2 := httptest.NewRequest(http.MethodGet, "/v1/meet/health", nil)
	resp2, _ := ok.Test(req2)
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("configured health = %d, want 200", resp2.StatusCode)
	}
}

// TestIdentityComesFromTheRowsNotTheBody: LiveKit uses `sub` as the participant
// identity and EJECTS an existing participant on a duplicate. So a caller-supplied
// identity let any member of a workspace kick a colleague out of a call and
// impersonate them to the room. The account on the membership row is the one
// identity the caller cannot choose.
func TestIdentityComesFromTheRowsNotTheBody(t *testing.T) {
	app := mount(t, apiKey, apiSecret)
	bearer := access(t, ada)
	room := roomIn(workspaceA)

	// Claim a colleague's person ref in the body. It must not reach the token.
	const victim = "person-victim-0001"
	body, _ := json.Marshal(request{RoomName: room, ID: victim, ParticipantName: "Impostor"})
	req := httptest.NewRequest(http.MethodPost, "/v1/meet/getToken", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint = %d %s, want 200", resp.StatusCode, raw)
	}
	claims := verify(t, string(raw), apiSecret)
	if claims["sub"] == victim {
		t.Fatal("the body's _id became the LiveKit identity — a member can eject and impersonate a colleague")
	}
	if claims["sub"] != account {
		t.Fatalf("sub = %v, want the account on the row %q", claims["sub"], account)
	}
	// Two different people in the same room get DIFFERENT identities, so a legitimate
	// second participant is not ejected as a duplicate. The account is the ROWS'
	// answer, so two callers collide only if the rows say they are one person.
	const other = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	two := &answers{row: func(_, subject string) plane.Member {
		seat := account
		if subject == bob {
			seat = other
		}
		return plane.Member{Member: true, Role: token.RoleMember, Account: seat}
	}}
	second := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)), two)
	_, body2 := ask(t, second, room, victim, access(t, bob))
	if c2 := verify(t, body2, apiSecret); c2["sub"] != other {
		t.Errorf("second participant sub = %v, want %q", c2["sub"], other)
	}
}

// TestHealthLeaksNothingUnauthenticated: /v1/meet/health takes no credential and is
// reachable on five public hosts, so ready:false is the whole signal. The reason — which
// names the key file and the Secret — belongs in the boot log. Keeping it here while
// deliberately withholding it from the getToken 503 would have been two postures in one
// file.
func TestHealthLeaksNothingUnauthenticated(t *testing.T) {
	for _, tc := range []struct {
		name       string
		key, given string
		wantCode   int
	}{
		{"unconfigured", "", "", http.StatusServiceUnavailable},
		{"configured", apiKey, apiSecret, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := keyFileWith(t, keyBody(tc.key, tc.given))
			app := mountWithKeyFile(t, path)
			req := httptest.NewRequest(http.MethodGet, "/v1/meet/health", nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantCode {
				t.Errorf("health = %d, want %d", resp.StatusCode, tc.wantCode)
			}
			// A non-empty reason and a 503 are the SAME fact — state.reason IS
			// "not configured" (state.ready is `reason == ""`), so pinning the
			// correspondence keeps the leak set below honest in both directions. It
			// also forbids the trap: appending an EMPTY reason to the leak set would
			// make strings.Contains(body, "") true and fail every configured case, so
			// the element is only added when it can actually discriminate.
			leaks := []string{"livekit-keys", "keys.yaml", "/etc/", apiKey, apiSecret, path}
			reason := load().reason
			if degraded := tc.wantCode == http.StatusServiceUnavailable; (reason != "") != degraded {
				t.Fatalf("reason %q and status %d disagree about whether meet is configured", reason, tc.wantCode)
			} else if degraded {
				leaks = append(leaks, reason)
			}
			for _, leak := range leaks {
				if strings.Contains(string(b), leak) {
					t.Errorf("health body leaks %q: %s", leak, b)
				}
			}
		})
	}
}

// TestForgedIdentityHeadersBuyNothing is the F1 regression, and it is the reason
// these tests run the real boundary.
//
// meet used to select its IAM lane on `c.Org() != "" && c.User() != ""` — two
// HEADERS. In a process with no identity boundary installed nothing strips them,
// and apps/meet's own plugin main is exactly such a process. So any caller could
// name themselves, take the lane, and be issued a LiveKit seat under a chosen
// identity — and LiveKit EVICTS an existing participant on a duplicate `sub`, so
// the forgery ejected a colleague from a live call and impersonated them to the
// room.
//
// The lane now selects on the boundary's own attestation, which no header can
// create. Both shapes are pinned: with no boundary the headers are inert, and with
// the boundary they are stripped before anything reads them.
func TestForgedIdentityHeadersBuyNothing(t *testing.T) {
	iamIssuer(t)
	forged := map[string]string{
		"X-Org-Id":  org,
		"X-User-Id": "11111111-2222-4333-8444-555555555555",
	}
	// The authority says yes to everything, so a header that reached it would buy a
	// seat. Nothing else can refuse these calls.
	for _, boundary := range []bool{false, true} {
		st := state{apiKey: apiKey, apiSecret: apiSecret, authority: anyone()}
		if j, ok := admitsOn(t, st, roomIn(workspaceA), "", forged, boundary); ok {
			t.Fatalf("SECURITY (boundary=%v): forged identity headers bought a seat as %q", boundary, j.account)
		}
	}
	// And they do not displace a real IAM session presented alongside them: the
	// headers are simply not an input, in either direction.
	st := state{apiKey: apiKey, apiSecret: apiSecret, authority: anyone()}
	if _, ok := admitsOn(t, st, roomIn(workspaceA), "Bearer "+access(t, ada), forged, true); !ok {
		t.Fatal("forged headers displaced a valid IAM session")
	}
}

// TestAnUnreachableAuthorityRefusesAtTheDoor: a REAL IAM access token through the
// REAL boundary does produce a principal — and the seat still depends on an answer
// from the process that owns the workspace rows. When that process cannot answer,
// the refusal must come from the missing answer and never from an assumption of
// membership.
//
// The discriminating control is the SAME token against an authority that answers.
// Without it this passes on any deployment that admits nobody, which is exactly
// what this file's fixture used to be.
func TestAnUnreachableAuthorityRefusesAtTheDoor(t *testing.T) {
	iamIssuer(t)
	member := "Bearer " + access(t, ada)

	answering := state{apiKey: apiKey, apiSecret: apiSecret, authority: anyone()}
	if _, ok := admitsOn(t, answering, roomIn(workspaceA), member, nil, true); !ok {
		t.Fatal("an authority that answers refused a member; the test is not discriminating")
	}
	// No authority at all: rows() falls back to the real peer, and there is no peer
	// in this process.
	silent := state{apiKey: apiKey, apiSecret: apiSecret}
	if _, ok := admitsOn(t, silent, roomIn(workspaceA), member, nil, true); ok {
		t.Fatal("a caller was seated with no answer from the workspace rows")
	}
	// A room that names no workspace is refused. NOT "before anything is asked" —
	// strings.Cut returns the whole string when there is no separator, so this is
	// asked about as a workspace named "no-separator", which nobody has. The
	// authority here is the ordinary one, which holds workspace A and nothing else;
	// roster_test.go's TestTheIAMLaneNeverWidensTheRoom pins WHICH name was asked.
	real := state{apiKey: apiKey, apiSecret: apiSecret, authority: holds(map[string]string{workspaceA: token.RoleMember})}
	if _, ok := admitsOn(t, real, "no-separator", member, nil, true); ok {
		t.Fatal("a room naming a workspace nobody holds was admitted")
	}
}

// TestPrivilegedIsFailClosed pins the role predicate the IAM lane grants on. It is
// the same vocabulary token.Privileged reads, over a role the SERVER read: a guest
// is reduced, and an absent or unrecognised role is not privileged, so a role added
// to the invite set tomorrow starts without a seat in a colleague's meeting.
func TestPrivilegedIsFailClosed(t *testing.T) {
	for _, role := range []string{token.RoleOwner, token.RoleAdmin, token.RoleMember, " owner "} {
		if !privileged(role) {
			t.Errorf("privileged(%q) = false, want true", role)
		}
	}
	for _, role := range []string{"guest", "", "   ", "GUEST", "Owner", "auditor"} {
		if privileged(role) {
			t.Errorf("privileged(%q) = true — an unproven role must not confer a seat", role)
		}
	}
}

// TestMachineCredentialIsNotAPerson is the F6/F7 regression.
//
// The identity boundary stamps an org AND a user for an sk- API key, so a lane
// selected on "has an org and a user" put a MACHINE on the lane whose whole
// question is which human is in this room — and LiveKit seats a participant under
// whatever identity it is handed, evicting the live one on a duplicate. A key
// principal carries no `sub`, so requiring one refuses it structurally rather than
// by trying to enumerate credential kinds.
// machinePrincipals is the shape a NON-HUMAN caller has after the boundary: an
// attested org and user, and no subject. One list, shared by the mint's gate and
// the lobby's read, so relaxing one of them is not something that can pass while
// the other still holds.
var machinePrincipals = []principal.Principal{
	{Org: "acme", User: "sk-key-user"},              // API key: no sub
	{Org: "acme", User: "hanzo/robot", Subject: ""}, // client_credentials
	{Org: "", User: "u", Subject: "has-a-sub"},      // no tenant
}

func TestMachineCredentialIsNotAPerson(t *testing.T) {
	st := state{apiKey: apiKey, apiSecret: apiSecret}
	iamIssuer(t)
	for _, p := range machinePrincipals {
		if _, ok := admitsWithPrincipal(t, st, roomIn(workspaceA), p); ok {
			t.Fatalf("SECURITY: a principal with no human subject was admitted: %+v", p)
		}
	}
}

// admitsWithPrincipal mints an attestation directly — the one way to model what the
// boundary produces for a credential kind this test cannot mint (an API key is
// resolved against IAM, not signed). principal.Mint is the boundary's own call, so
// this exercises exactly the value admits() reads.
func admitsWithPrincipal(t *testing.T, st state, room string, p principal.Principal) (j joiner, ok bool) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/probe", func(c *zip.Ctx) error {
		principal.Mint(c, p)
		j, ok = st.admits(c, room)
		return c.String(http.StatusOK, "ok")
	})
	if _, err := app.Test(httptest.NewRequest(http.MethodGet, "/probe", nil)); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return j, ok
}

// sharedKey provisions the anti-forgery key this package's mount requires: the group
// gate verifies a token the account process minted, so a deployment carries the one
// value from KMS and a mount without it refuses (apps/account, Shared).
func sharedKey(t *testing.T) {
	t.Helper()
	t.Setenv(accountapp.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
}
