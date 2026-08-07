package audit

// Secret redaction for the before/after captured on a mutation.
//
// THE RULE. An audit record must NEVER contain a credential — no password,
// token, API key, private key, card number, or session secret. Two layers
// enforce this:
//
//  1. The HTTP middleware captures METADATA ONLY (actor/action/resource/outcome).
//     It NEVER reads a request or response body, so a secret in a POST body can
//     never reach a record through the automatic path. This is the primary
//     guarantee: the code that can't see a secret can't leak one.
//
//  2. An EXPLICIT emit point that supplies structured before/after (e.g. a config
//     change diff) runs it through Redact first. Redact walks the JSON and
//     replaces the VALUE of any key whose name matches the secret denylist with a
//     fixed marker, recursively. It is deny-by-key-name — the same allowlist
//     PATTERN cloud already uses for user-secret redaction — chosen because a
//     mutation diff has arbitrary shape and key-name matching is the robust,
//     well-understood control (vs. trying to detect "secret-looking" values).
//
// Redact is conservative: on any structural surprise it returns the redaction
// marker rather than the input, so a parser edge case fails CLOSED (no raw
// passthrough).

import (
	"encoding/json"
	"regexp"
	"strings"
)

// redactedMarker replaces every redacted value. A constant so tests and the
// query UI recognize it unambiguously.
const redactedMarker = "[REDACTED]"

// secretKeyParts are substrings that, when contained (case-insensitively) in a
// JSON object key, mark that key's value as secret. Kept as a small, auditable
// denylist of the credential-bearing field names that actually occur across the
// Hanzo surface (IAM, KMS, commerce, provider config). Matching is substring so
// "clientSecret", "api_key", "PRIVATE_KEY", "accessToken" all match.
var secretKeyParts = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"apikey",
	"api_key",
	"api-key",
	"authorization",
	"auth_token",
	"private_key",
	"privatekey",
	"privkey", // privkey, wgPrivKey
	"passphrase",
	"client_secret",
	"credential",
	"session",
	"cookie",
	"card", // card_number, cardNumber
	"cvv",
	"cvc",
	"pin",
	"ssn",
	"social_security", // socialSecurityNumber (lower-cased match covers camelCase)
	"socialsecurity",
	"otp",
	"mnemonic",
	"phrase", // seed_phrase, seedPhrase, recoveryPhrase
	"access_key",
	"secret_key",
	"refresh_token",
	"id_token",
	"bearer",
	"signing_key",
	"encryption_key",
}

// urlCredential matches the userinfo of a URL — the "user:password@" between the
// scheme and the host.
//
// This is the one credential the key denylist above cannot see, because it is in
// the VALUE and the key naming it is innocent: a clone URL arrives as
// {"cloneUrl": "https://x-access-token:<token>@github.com/org/repo"} (the shape
// apps/coding builds to check a repo out), and "cloneUrl" matches nothing in
// secretKeyParts. Every one of those characters would otherwise be recorded
// verbatim.
//
// Stripping on the value alone is safe HERE and nowhere else in this file,
// because userinfo in a URL is a credential BY CONSTRUCTION (RFC 3986 3.2.1) —
// a structural fact, not a guess that a string "looks secret". The deliberate
// choice this file already documents, to match key names rather than sniff
// values, is unchanged: this adds one structural rule, not a heuristic.
var urlCredential = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/?#\s@]+)@`)

// stripURLCredential replaces the secret half of any URL userinfo in s.
//
// The USER is kept and only the password is replaced, because the user names how
// the call authenticated ("x-access-token", "oauth2") and is worth reading in a
// trace, while the part after the colon is the credential. Userinfo with no colon
// IS the token — nothing there is a name — so it goes whole.
func stripURLCredential(s string) string {
	if !strings.Contains(s, "@") {
		return s // no userinfo is possible; skip the scan
	}
	return urlCredential.ReplaceAllStringFunc(s, func(m string) string {
		g := urlCredential.FindStringSubmatch(m)
		scheme, userinfo := g[1], g[2]
		if user, _, found := strings.Cut(userinfo, ":"); found {
			return scheme + user + ":" + redactedMarker + "@"
		}
		return scheme + redactedMarker + "@"
	})
}

// RedactText returns s with credentials removed, whether or not s is JSON.
//
// It exists because not everything worth recording is a structured diff. A tool
// call's ARGUMENTS are a JSON object and get the full treatment — the secret-key
// denylist plus the URL strip. A tool's RESULT is whatever the tool returned,
// commonly prose or a log tail, and running that through Redact would hand back
// the fail-closed marker for the whole thing: correct for a mutation diff that
// must parse, useless for text that was never meant to.
//
// So the shape decides the pass. JSON gets both; text gets the structural URL
// strip alone, which is the only rule that can be applied to a value with no keys
// to judge. Text is therefore redacted LESS than JSON, and that is a real limit
// rather than an oversight: a credential written into prose by the tool that
// returned it ("your token is X") is not something key-name matching can see.
// Callers that can supply structure should.
func RedactText(s string) string {
	if s == "" {
		return s
	}
	if json.Valid([]byte(s)) {
		return string(Redact(json.RawMessage(s)))
	}
	return stripURLCredential(s)
}

// isSecretKey reports whether a JSON key names a credential-bearing field.
func isSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, part := range secretKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// Redact returns a copy of the JSON value with every secret-keyed value replaced
// by the redaction marker, recursively through objects and arrays. Non-JSON or
// empty input yields nil (nothing to record). On a JSON parse error the input is
// dropped (returns the marker as a JSON string) rather than passed through —
// fail closed.
//
// Use it at any explicit emit point that supplies before/after:
//
//	audit.Emit(ctx, rec.WithChange(audit.Redact(before), audit.Redact(after)))
func Redact(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Unparseable — never echo it back verbatim; record a marker instead.
		b, _ := json.Marshal(redactedMarker)
		return b
	}
	cleaned := redactValue("", v)
	out, err := json.Marshal(cleaned)
	if err != nil {
		b, _ := json.Marshal(redactedMarker)
		return b
	}
	return out
}

// redactValue walks a decoded JSON value. key is the object key under which v
// sits (empty at the root and for array elements); when key is a secret key, the
// ENTIRE value v is replaced (whether it is a scalar, object, or array — a secret
// nested object is redacted whole). Otherwise objects/arrays are recursed.
func redactValue(key string, v any) any {
	if key != "" && isSecretKey(key) {
		return redactedMarker
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = redactValue(k, val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactValue("", val) // array elements inherit no key
		}
		return out
	case string:
		// The one credential a key name cannot reveal (see urlCredential).
		return stripURLCredential(t)
	default:
		return v
	}
}
