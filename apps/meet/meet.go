// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package meet is the CONTROL plane for the virtual office: it decides who may
// join which room, and says so by minting a short-lived LiveKit access token.
//
//	POST /v1/meet/getToken  {roomName, _id, participantName}  ->  the token, as text
//
// The MEDIA plane is not here and never will be. Audio, video and screen share ride
// a direct browser↔LiveKit WebRTC connection (LIVEKIT_WS = wss://live.hanzo.bot);
// media is not a thing to proxy through an API binary. What moved into this binary
// is the ONE decision a server has to make about a call — may this caller join this
// room — which needs the team session secret and the LiveKit signing key, and needs
// no pod of its own to hold them.
//
// TWO KEYS, TWO ROLES, and they never mix:
//
//   - SERVER_SECRET verifies the CALLER. It is the HS256 key clients/team signs
//     session tokens with, so "is this a real member of this workspace" is answered
//     against the same signature the rest of /v1/team trusts. It arrives as env from
//     the KMS-synced `team-secrets`.
//   - The LiveKit api key/secret signs the ANSWER, and it is read from the SAME
//     keys.yaml file the LiveKit server itself validates against (Secret
//     `livekit-keys`, mounted read-only). ONE representation of that material, so it
//     cannot drift: a second copy projected into env would mint tokens that look
//     perfect and are refused at the media edge, which is the silent failure this
//     whole package is trying not to have.
//
// Missing or ambiguous material is a 503, LOUDLY: the reason names the file and the
// Secret in the log at boot, while the caller gets an unadorned "not configured" (an
// unauthenticated 503 is not the place to enumerate our secret plumbing).
package meet

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/zap-proto/zip"
	"gopkg.in/yaml.v3"
)

// ttl is how long a minted join token is good for. Ten minutes: long enough to
// complete a join handshake on a bad connection, short enough that a leaked token
// is worthless before anyone can use it. The client re-mints per join, so this is
// not a session length — a call that has already started is a LiveKit connection
// and does not re-check the token.
const ttl = 10 * time.Minute

// keyFileEnv overrides where keys.yaml is read from. It exists because the LiveKit
// server takes the same knob (--key-file), so the path is a deployment fact on both
// sides of the pair rather than a constant on one — and because a test must be able
// to point at a temp file.
const keyFileEnv = "LIVEKIT_KEY_FILE"

// keyFile is where the manifest mounts Secret `livekit-keys`. Same file, same Secret,
// same content the LiveKit server reads.
const keyFile = "/etc/livekit-keys/keys.yaml"

// apiKeyEnv names WHICH api key in keys.yaml to sign with, for the case where the file
// declares more than one. Unset is correct and normal for a single-key file.
const apiKeyEnv = "LIVEKIT_API_KEY"

// state is meet's own data: the caller-verifying key, the answer-signing pair, and
// the reason it is unusable when it is. reason is the ONE flag — a non-empty reason
// IS "not configured", so there is no way for the two to disagree.
type state struct {
	teamSecret string // SERVER_SECRET — verifies the caller's team session
	apiKey     string // LiveKit api key    — the `iss` LiveKit matches on
	apiSecret  string // LiveKit api secret — signs the minted token
	reason     string // why this is unusable; empty means usable
}

// ready reports whether meet can mint. Fail-closed: an unconfigured deploy refuses
// every mint rather than issuing a token nobody can verify.
func (s state) ready() bool { return s.reason == "" }

// load assembles the signing material and, when it cannot, says exactly why. Every
// failure path produces a reason naming the file or env var an operator has to fix —
// this used to return a bare zero value, which made a misconfigured deploy an
// indistinguishable permanent 503 with nothing in the log to chase.
func load() state {
	secret := os.Getenv("SERVER_SECRET")
	if secret == "" || secret == "secret" {
		// The upstream public default is treated as absent for the reason
		// clients/team's resolveSecret does: a known key lets anyone mint a session
		// naming any workspace — here, a join token for a room they were never in.
		return state{reason: "SERVER_SECRET is unset or the public default literal (K8s Secret team-secrets, key SERVER_SECRET)"}
	}
	path := os.Getenv(keyFileEnv)
	if path == "" {
		path = keyFile
	}
	key, apiSecret, err := readKeys(path)
	if err != nil {
		return state{reason: err.Error()}
	}
	return state{teamSecret: secret, apiKey: key, apiSecret: apiSecret}
}

// readKeys parses a LiveKit key file: a YAML map of apiKey -> apiSecret, which is the
// format the LiveKit server's --key-file takes. It returns the single pair, or an
// error naming the file and Secret.
//
// EXACTLY ONE entry is required. Zero is unconfigured. More than one is AMBIGUOUS,
// and ambiguity here is refused rather than resolved: Go map iteration is random, so
// "just take the first" would pick a different key per process start, and a token
// signed under a key the caller's room was not provisioned for fails at the media
// edge intermittently — the worst possible failure shape. If a deployment ever needs
// several api keys, the code that chooses between them has to be written on purpose.
func readKeys(path string) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("cannot read the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml): %v", path, err)
	}
	// gopkg.in/yaml.v3 into map[string]string — the EXACT library and target type the
	// LiveKit server decodes this file with (livekit/pkg/config: `Keys
	// map[string]string`, yaml.v3), so we and the verifier read identical bytes to
	// identical strings by construction.
	//
	// This was sigs.k8s.io/yaml, which is NOT equivalent: it routes YAML through JSON
	// and coerces a scalar to the target type, so measured on real input it turned
	//   0123456789 -> "1.2345679e+08"      yes -> "true"      0x1f -> "31"
	//   1e5        -> "100000"             no  -> "false"     00   -> "0"
	// and silently took the LAST of a duplicated key. Any of those mints a token that
	// verifies nowhere while the log says "mounted" — the exact silent failure this
	// file claims to prevent. yaml.v3 preserves all of them verbatim and REFUSES a
	// duplicate key outright, which we get for free by using the right library.
	var keys map[string]string
	if err := yaml.Unmarshal(raw, &keys); err != nil {
		return "", "", fmt.Errorf("cannot parse the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) as a YAML apiKey->apiSecret map: %v", path, err)
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names) // stable messages; no decision depends on map order
	if len(names) == 0 {
		return "", "", fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) declares no api key", path)
	}
	// A LiveKit key file is a MAP because a server may hold several keys. When it
	// does, LIVEKIT_API_KEY names which one this binary signs with. Selecting by name
	// is the only safe way to resolve the ambiguity: Go map order is random, so
	// "take the first" would pick differently per process start and produce tokens
	// that fail at the media edge intermittently.
	key := ""
	if want := strings.TrimSpace(os.Getenv(apiKeyEnv)); want != "" {
		if _, found := keys[want]; !found {
			return "", "", fmt.Errorf("%s names api key %q, which the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) does not declare (it has: %s)", apiKeyEnv, want, path, strings.Join(names, ", "))
		}
		key = want
	} else if len(names) > 1 {
		return "", "", fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) declares %d api keys (%s); set %s to name which one to sign with — refusing to pick", path, len(names), strings.Join(names, ", "), apiKeyEnv)
	} else {
		key = names[0]
	}
	apiSecret := keys[key]
	// Blank-ish is refused, but the values are returned BYTE-EXACT — deliberately not
	// trimmed. The only property that matters is that the pair we sign with is
	// identical to the pair the LiveKit server read from these same bytes. Trimming
	// would silently diverge from any reader that does not trim: the api key is the
	// `iss` LiveKit matches on, and the secret IS the signing key, so one stripped
	// space produces tokens that mint perfectly and verify nowhere. Consistency with
	// the other reader beats tidiness.
	if strings.TrimSpace(key) == "" || strings.TrimSpace(apiSecret) == "" {
		return "", "", fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) has an empty api key or secret", path)
	}
	return key, apiSecret, nil
}

// Mount wires /v1/meet/* onto app. The route is registered even when unconfigured so
// the surface always answers under its OWN name with an honest 503, rather than
// falling through to some other subsystem's catch-all and reporting a 404 for a
// service that exists but has no keys.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("meet.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("meet.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "meet"), State: load()}

	// The path suffix is the CALLER's, not ours. The office client POSTs
	// concatLink(LOVE_ENDPOINT, '/getToken') from a published bundle, so with
	// LOVE_ENDPOINT=/v1/meet the wire lands here. Renaming it means shipping a new
	// front image, not editing a manifest.
	app.Post("/v1/meet/getToken", cloud.Handle(s, mint))

	// /v1/meet/health makes "the office is unconfigured" a SIGNAL rather than a grep.
	// A boot log line is invisible to a dashboard and rotates away; this is the same
	// contract every other subsystem exposes, so the existing probe/alerting surface
	// picks it up with no new machinery.
	//
	// It carries ready:false and NOT the reason. This route takes no credential and is
	// reachable on five public hosts, so it is not an operator surface — a health path
	// under /v1/* is only as private as the edge in front of it, and this edge makes it
	// public. ready:false is the whole dashboard fact; the reason, which names the key
	// file and the Secret, stays in the boot log where the operator already is. Same
	// posture as the getToken 503 (see health, below) — one file, one answer.
	app.Get("/v1/meet/health", cloud.Handle(s, health))

	if !s.State.ready() {
		// ERROR, not warn, and it names the file/Secret to fix. A subsystem that can
		// never serve a single request is not a warning — and the previous version of
		// this line said only "not all set", which is exactly why a Secret that was
		// empty in the cluster could have shipped as a permanent, silent 503.
		s.Log.Error("meet subsystem UNCONFIGURED — POST /v1/meet/getToken will 503 on every call until this is fixed; the office (video/audio rooms) is down",
			"reason", s.State.reason, "prefix", "/v1/meet")
		return nil
	}
	// apiKey is an identifier, not a secret (it is the public `iss` of every minted
	// token), so logging it is what lets an operator confirm the binary and the
	// LiveKit server agree on which key pair is in play. The secret is never logged.
	s.Log.Info("meet subsystem mounted", "prefix", "/v1/meet", "ttl", ttl.String(), "livekitApiKey", s.State.apiKey)
	return nil
}

// health reports whether meet can mint, and when it cannot, why. 503 + ready:false so
// the degraded state is legible to a probe and to a dashboard, not just to whoever
// greps the boot log.
func health(s *cloud.Service[state], c *zip.Ctx) error {
	res := map[string]any{"service": "meet", "status": "ok"}
	if !s.State.ready() {
		// ready:false IS the dashboard fact, and it is all a probe needs. The REASON —
		// which names the key-file path and the Secret — stays in the boot log, because
		// this endpoint takes no credential and is reachable on five public hosts. The
		// api key is withheld for the same reason; it is not secret, but an unauthed
		// caller has no business enumerating which key pair this binary signs with.
		// (Leaking it here while deliberately keeping it out of the getToken 503 would
		// have been two postures in one file.)
		res["status"], res["ready"] = "degraded", false
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["ready"] = true
	return c.JSON(http.StatusOK, res)
}

// request is the office client's wire. `_id` is the SPA's person ref — accepted because
// the published bundle sends it, and IGNORED because the participant identity now comes
// from the signed token (see mint). participantName is a display name only.
type request struct {
	RoomName        string `json:"roomName"`
	ID              string `json:"_id"`
	ParticipantName string `json:"participantName"`
}

// mint answers POST /v1/meet/getToken: verify the caller belongs to the room's
// workspace, then hand back a join token for exactly that room.
//
// The response is the RAW token as text/plain, not JSON. That is the caller's
// contract — the office client reads it with res.text() — and it is also the honest
// shape: the body is one opaque string, so wrapping it in an object would add a
// envelope neither side needs.
func mint(s *cloud.Service[state], c *zip.Ctx) error {
	st := s.State
	if !st.ready() {
		// The CALLER gets the fact, not the plumbing: this 503 is reachable without
		// any credential, so it must not enumerate our file paths and Secret names.
		// The full reason went to the log at boot (Mount), which is where an operator
		// is looking.
		return zip.Errorf(http.StatusServiceUnavailable, "meet: the office is not configured")
	}
	var req request
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return zip.ErrBadRequest("malformed request body")
	}
	room := strings.TrimSpace(req.RoomName)
	if room == "" {
		return zip.ErrBadRequest("roomName required")
	}
	t, ok := st.admits(room, c.Header("Authorization"))
	if !ok {
		return zip.Errorf(http.StatusUnauthorized, "not a member of this room's workspace")
	}
	// THE IDENTITY IS THE TOKEN'S, NOT THE BODY'S. LiveKit uses `sub` as the
	// participant identity and EJECTS an existing participant on a duplicate — so
	// minting with a caller-supplied `_id` let any member of a workspace kick a
	// colleague out of a call by claiming their identity, and impersonate them to
	// everyone else in the room. Upstream did this too; it is still wrong. The signed
	// account is the one identity the caller cannot choose.
	//
	// The body's `_id` (the SPA's person ref) is deliberately ignored rather than
	// checked: verifying it belongs to the caller would need the person<->account
	// mapping from clients/team, whereas the token already carries an identity that IS
	// the caller. One fewer seam, and no lookup to get wrong.
	identity := strings.TrimSpace(t.Account)
	if identity == "" {
		return zip.Errorf(http.StatusUnauthorized, "token carries no account")
	}
	tok, err := st.grant(room, identity, strings.TrimSpace(req.ParticipantName), time.Now())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "meet: mint failed")
	}
	return c.String(http.StatusOK, tok)
}

// workspace is the workspace a room belongs to. Room names are minted client-side as
// "<workspaceUuid>_<roomName>_<roomId>", so the workspace is the leading segment.
// This is the ONLY thing binding a room to a tenant, which is why admits compares it
// against the SIGNED workspace claim and not against anything in the body.
func workspace(room string) string {
	ws, _, _ := strings.Cut(room, "_")
	return ws
}

// admits decides whether the bearer may join room. Every clause is a refusal; there
// is no branch that admits by default.
//
//   - the token must VERIFY against SERVER_SECRET (signature, exp, nbf) — so a forged
//     or stale session is not a member;
//   - its SIGNED workspace claim must equal the room's workspace prefix — this is the
//     tenant boundary. Without it, any member of any workspace could mint a join token
//     for any room in any other workspace by naming it;
//   - an empty workspace claim is refused outright, so a session token that is not
//     bound to a workspace cannot match a room that has no separator in its name;
//   - the token must carry a PRIVILEGED workspace role (token.Privileged, the one
//     predicate that reads the signed extra.role). A guest is a reduced principal and
//     a seat in a colleague's meeting is not a reduced-session privilege. This used to
//     compare extra.readonly/extra.guest — claims NOTHING in this repo mints, so the
//     check was inert and every guest was admitted. selectWorkspace now signs the real
//     workspace role, and an ABSENT role is unprivileged, so a token that has not
//     proven a role is refused rather than assumed to be a member.
func (s state) admits(room, auth string) (*token.Token, bool) {
	raw := bearer(auth)
	if raw == "" {
		return nil, false
	}
	t, err := token.Decode(raw, s.teamSecret, true)
	if err != nil {
		return nil, false
	}
	if t.Workspace == "" || t.Workspace != workspace(room) {
		return nil, false
	}
	if !t.Privileged() {
		return nil, false
	}
	return t, true
}

// bearer extracts the token from an "Authorization: Bearer <t>" header (scheme
// case-insensitive). Empty when absent or not a bearer.
func bearer(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// video is LiveKit's VideoGrant, narrowed to the two fields a join token needs. The
// grant is deliberately minimal: roomJoin into ONE named room. Every capability
// LiveKit defaults on for a joiner (publish, subscribe) follows from that; every
// capability it does not (roomAdmin, roomCreate, roomList, recorder, ingressAdmin)
// stays off because it is not named here. A token that cannot express a privilege
// cannot leak it.
type video struct {
	RoomJoin bool   `json:"roomJoin"`
	Room     string `json:"room"`
}

// claims is the LiveKit access-token payload: RFC 7519 registered claims plus
// LiveKit's grant object. The shape is LiveKit's, not ours — it must match what the
// media server verifies, so the field set here mirrors livekit/protocol's
// auth.tokenClaims (iss=apiKey, sub=identity, iat/nbf/exp, name, video).
type claims struct {
	Iss   string `json:"iss"`
	Sub   string `json:"sub"`
	Iat   int64  `json:"iat"`
	Nbf   int64  `json:"nbf"`
	Exp   int64  `json:"exp"`
	Name  string `json:"name,omitempty"`
	Video video  `json:"video"`
}

// header is the fixed JOSE header for every token this package mints. HS256 is not a
// choice here — it is what LiveKit verifies a shared-secret token with. It is a
// constant rather than a field precisely so no request can influence the algorithm:
// there is no code path that could be talked into `alg: none`.
var header = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// grant mints the join token: a compact HS256 JWT over the claims above.
//
// This composes stdlib crypto/hmac + crypto/sha256 rather than taking a JWT library,
// for two reasons. First, clients/team/token already establishes this exact idiom for
// the platform's own HS256 tokens, and a second way to make a JWT in one binary is a
// second way to get it wrong. Second, this side only ever SIGNS: the verifier is the
// LiveKit server, so the whole class of bugs a JWT library earns its keep against —
// alg confusion, `alg: none`, non-constant-time comparison — has no code path here.
// The primitives themselves are stdlib; nothing cryptographic is hand-rolled.
func (s state) grant(room, identity, name string, now time.Time) (string, error) {
	// Defense in depth at the CRYPTO boundary, not just at the gate. crypto/hmac
	// accepts an empty key and returns a perfectly well-formed MAC, so an empty
	// signing key does not fail — it silently produces a token that verifies under
	// the empty key and under nothing the LiveKit server holds. Refusing here means
	// removing the ready() check upstream still cannot mint an unverifiable token.
	if s.apiKey == "" || s.apiSecret == "" {
		return "", errors.New("meet: refusing to sign with an empty LiveKit api key or secret")
	}
	payload, err := json.Marshal(claims{
		Iss:   s.apiKey,
		Sub:   identity,
		Iat:   now.Unix(),
		Nbf:   now.Unix(),
		Exp:   now.Add(ttl).Unix(),
		Name:  name,
		Video: video{RoomJoin: true, Room: room},
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(s.apiSecret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
