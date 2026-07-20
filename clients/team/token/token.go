// Package token mints and verifies the HS256 JWTs that the team SPA, the
// /v1/team/account API and the /v1/team/transactor data plane all share.
//
// The wire format is byte-compatible with the platform `jwt-simple` HS256 tokens
// (foundations/core/packages/token/src/token.ts): header
// {"typ":"JWT","alg":"HS256"}, a COMPACT JSON payload whose keys appear in
// the fixed order {extra, account, workspace, sub, exp, nbf} with
// undefined/empty keys omitted, base64url WITHOUT padding, and a raw
// HMAC-SHA256 signature over `header.payload`.
//
// account and workspace, when present, MUST be UUIDs — the upstream
// `generateToken` throws otherwise and the transactor client refuses a
// workspace token that is missing either, so we enforce the same invariant
// at mint time.
//
// The secret is the shared `SERVER_SECRET` (synced from KMS in production).
// One secret, one wire format — every team surface signs and verifies through
// this package. Ported VERBATIM from github.com/hanzoai/team-go/pkg/token.
package token

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// clockSkew is the temporal-claim tolerance: exp/nbf are enforced with this much
// slack so a small clock drift between minting and verifying hosts never rejects
// a live session at the boundary.
const clockSkew = 60 * time.Second

// legacyExp is the acceptance deadline for tokens minted BEFORE the exp rollout,
// which carry no `exp` claim (the payload has no iat either, so a per-token grace
// cannot be computed — only a fixed cutoff can honor them). Until this instant a
// missing exp verifies (existing live sessions keep working); after it a missing
// exp is ErrExpired, closing the immortal-token window for good. Deliberately a
// constant, never an env knob.
var legacyExp = time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)

// now is the clock Decode enforces temporal claims against; a variable only so
// tests can pin it across the legacyExp cutoff.
var now = time.Now

// header is the constant jwt-simple HS256 header, pre-encoded. jwt-simple
// emits exactly {"typ":"JWT","alg":"HS256"} (typ before alg); we hardcode the
// bytes so the segment is identical down to key order.
var header = base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"HS256"}`))

// Token is the decoded payload. Field order is the jwt-simple emit order so
// marshalling reproduces the upstream byte layout; `omitempty` drops the keys
// jwt-simple leaves `undefined`. account carries no omitempty: it is always
// present (the upstream payload always includes `account`).
type Token struct {
	Extra     map[string]any `json:"extra,omitempty"`
	Account   string         `json:"account"`
	Workspace string         `json:"workspace,omitempty"`
	Sub       string         `json:"sub,omitempty"`
	Exp       int64          `json:"exp,omitempty"`
	Nbf       int64          `json:"nbf,omitempty"`
}

// ErrMalformed is returned when a token is not three base64url segments.
var ErrMalformed = errors.New("token: malformed")

// ErrSignature is returned when HMAC verification fails.
var ErrSignature = errors.New("token: signature mismatch")

// ErrExpired is returned by Decode(verify=true) when the token's `exp` has passed.
var ErrExpired = errors.New("token: expired")

// ErrNotYetValid is returned by Decode(verify=true) when the token's `nbf` is in
// the future.
var ErrNotYetValid = errors.New("token: not yet valid")

// ErrNoSecret is returned when signing or verifying is attempted with an empty
// secret. There is NO fallback literal: an unset SERVER_SECRET must surface as a
// hard error, never as a token silently signed with a public value.
var ErrNoSecret = errors.New("token: empty secret")

// Generate mints an HS256 token for account (always) and workspace (optional —
// pass "" for an account/login token that is not yet scoped to a workspace).
// extra is folded in as the leading `extra` claim when non-empty (e.g.
// {"org":"acme"}). exp is the expiry as a unix-second timestamp — pass 0 to mint
// a token WITHOUT an `exp` claim (the wire-golden / non-expiring case). Both ids
// are validated as UUIDs.
func Generate(account, workspace string, extra map[string]any, exp int64, secret string) (string, error) {
	if err := requireUUID("account", account); err != nil {
		return "", err
	}
	if workspace != "" {
		if err := requireUUID("workspace", workspace); err != nil {
			return "", err
		}
	}
	if secret == "" {
		return "", ErrNoSecret
	}
	payload, err := marshalCompact(Token{Extra: extra, Account: account, Workspace: workspace, Exp: exp})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	signing := header + "." + body
	return signing + "." + sign(signing, secret), nil
}

// Decode verifies (when verify is true) and parses an HS256 token. When verify is
// true it enforces the temporal claims with clockSkew tolerance: a token whose
// `exp` has passed is rejected (ErrExpired), a token whose `nbf` is in the future
// is rejected (ErrNotYetValid), and a token with NO `exp` is honored only until
// legacyExp (the pre-rollout grace) then rejected. This closes the
// permanent-replay window on a captured token (the transactor token rides in the
// URL path — log-prone — so a bounded lifetime is the mitigation until the front
// moves it off the URL, #60).
func Decode(tok, secret string, verify bool) (*Token, error) {
	h, body, sig, err := split(tok)
	if err != nil {
		return nil, err
	}
	if verify {
		if secret == "" {
			return nil, ErrNoSecret
		}
		if !hmac.Equal([]byte(sig), []byte(sign(h+"."+body, secret))) {
			return nil, ErrSignature
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("token: bad payload: %w", err)
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("token: decode payload: %w", err)
	}
	if verify {
		ts := now()
		exp := t.Exp
		if exp == 0 {
			exp = legacyExp.Unix() // pre-rollout token: honored until the fixed cutoff
		}
		if ts.Add(-clockSkew).Unix() >= exp {
			return nil, ErrExpired
		}
		if t.Nbf != 0 && ts.Add(clockSkew).Unix() < t.Nbf {
			return nil, ErrNotYetValid
		}
	}
	return &t, nil
}

func split(tok string) (h, body, sig string, err error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", ErrMalformed
	}
	return parts[0], parts[1], parts[2], nil
}

func sign(signing, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// marshalCompact encodes v the way JSON.stringify does: compact and WITHOUT
// Go's default HTML escaping of <, > and &, which JSON.stringify never applies —
// disabling it keeps the bytes identical to the upstream signer.
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func requireUUID(field, v string) error {
	if err := uuid.Validate(v); err != nil {
		return fmt.Errorf("token: invalid %s uuid: %q", field, v)
	}
	return nil
}
