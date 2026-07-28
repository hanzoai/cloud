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

// keyFileWith writes a LiveKit key file with the given raw body and returns its path.
func keyFileWith(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return p
}

// mount stands the subsystem up against a real key FILE, which is how production
// reads it (Secret livekit-keys/keys.yaml, mounted read-only) — not against env
// scalars, which is the shape that turned out not to exist in the cluster.
func mount(t *testing.T, team, key, secret string) *zip.App {
	t.Helper()
	body := ""
	if key != "" || secret != "" {
		body = key + ": " + secret + "\n"
	}
	return mountWithKeyFile(t, team, keyFileWith(t, body))
}

func mountWithKeyFile(t *testing.T, team, path string) *zip.App {
	t.Helper()
	t.Setenv("SERVER_SECRET", team)
	t.Setenv(keyFileEnv, path)
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
		{"empty key file", teamSecret, "", ""},
		{"api key with an empty secret", teamSecret, apiKey, ""},
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
	app := mountWithKeyFile(t, teamSecret, path)
	bearer := session(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
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
	t.Setenv("SERVER_SECRET", teamSecret)
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
func TestUnconfiguredReasonNeverReachesTheCaller(t *testing.T) {
	app := mount(t, teamSecret, "", "")
	code, body := ask(t, app, roomIn(workspaceA), "person-42", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", code)
	}
	for _, leak := range []string{"livekit-keys", "keys.yaml", "/etc/", "SERVER_SECRET", t.TempDir()} {
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
// Note on types: sigs.k8s.io/yaml coerces a scalar to the target type, so a secret
// written as a bare number decodes as its string form rather than erroring. That is
// not a divergence risk in practice — a bare-number secret would have to survive the
// LiveKit server's own startup first — and it is why the refusal table above tests
// emptiness and ambiguity, which are the failures that actually occur.
func TestKeyFileValuesAreByteExact(t *testing.T) {
	cases := []struct{ name, body, wantKey, wantSecret string }{
		{"plain scalars", "K: abc123\n", "K", "abc123"},
		{"quoted, internal spaces preserved", "K: \"a b c\"\n", "K", "a b c"},
		{"quoted, TRAILING space preserved", "K: \"abc \"\n", "K", "abc "},
		{"quoted, LEADING space preserved", "K: \" abc\"\n", "K", " abc"},
		{"base64-ish with padding", "APIxY9: aGVsbG8td29ybGQ=\n", "APIxY9", "aGVsbG8td29ybGQ="},
		{"secret containing a colon", "K: \"a:b\"\n", "K", "a:b"},
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
	app := mountWithKeyFile(t, teamSecret, keyFileWith(t, apiKey+": \""+padded+"\"\n"))
	bearer := session(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix())
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
