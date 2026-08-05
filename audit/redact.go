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
	default:
		return v
	}
}
