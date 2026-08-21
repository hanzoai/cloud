// Package scrub answers one question — what does a credential look like in text,
// and how is one removed — and it is a LEAF so that every renderer can ask.
//
// It lived in package cloud, which is ~400 packages, so the only way to reach the
// rule was to link the world. Two places wanted it and could not pay that: the
// fleet's MCP door (fleet), whose whole point is that the host stays light, and
// apps/admission, which kept its own copy of the key prefixes with a comment
// asking whoever changes one to remember the other. A rule with two copies has
// two answers; this is the one home.
//
// The shape test is what distinguishes this from key-NAME redaction (audit.Redact):
// an upstream that answers "Invalid API key: sk-…" gives no field name to key on,
// so only the value's own shape can find it.
package scrub

import (
	"net/url"
	"strings"
)

func Token(seg string) string {
	dec := percentDecode(seg)
	if IsKey(seg) || IsKey(dec) ||
		looksLikeJWT(seg) || looksLikeJWT(dec) ||
		looksLikeHighEntropyToken(seg) || looksLikeHighEntropyToken(dec) {
		return "[REDACTED-TOKEN]"
	}
	return seg
}

// percentDecode best-effort URL-decodes s so a percent-encoded credential
// (hk%2DKEY, sk%5Flive%5F…) is normalized before the credential tests run. On a
// malformed escape it returns s unchanged (the raw form is then tested as-is).
func percentDecode(s string) string {
	// Decode repeatedly (bounded) so a NESTED encoding (%252D -> %2D -> -) is
	// fully normalized before the credential tests run. Stop when a pass makes no
	// change, on a malformed escape, or after a small cap (defeats a decode bomb).
	for i := 0; i < 3 && strings.Contains(s, "%"); i++ {
		dec, err := url.PathUnescape(s)
		if err != nil || dec == s {
			break
		}
		s = dec
	}
	return s
}

// looksLikeJWT reports whether s is a JSON Web Token OR a JWT header segment. A
// full JWT is three base64url parts split by '.', header starting "eyJ". But when
// free text (a UA) is tokenized on '.', a dotted JWT splits into parts; a real
// header part is >=24 chars (caught by the high-entropy run), yet to be safe we
// ALSO flag any lone segment starting with the canonical base64url header prefix
// "eyJ" (which decodes to '{"') regardless of length — a JWT header can never be
// a legitimate resource id, so redacting it has no false-positive cost.
func looksLikeJWT(s string) bool {
	if !strings.HasPrefix(s, "eyJ") {
		return false
	}
	// A full token (two dots) or a bare header segment — either way, redact.
	return true
}

// highEntropyMinLen is the length at/above which an UNBROKEN run of base64/hex
// chars is treated as an opaque secret. 24 covers a 128-bit base64 token, a
// 24-nibble hex key, and short API keys, while every human-readable path
// segment/slug/model-name stays under it once split on its separators — no
// single run of a name like "text-embedding-3-large" reaches 24.
const highEntropyMinLen = 24

// looksLikeHighEntropyToken reports whether s CONTAINS an unbroken run of
// >= highEntropyMinLen base64/hex chars — the shape of a raw API key / access
// token / hex secret — UNLESS s is a structured human identifier.
//
// The run alphabet is [A-Za-z0-9_+/-] — the base64 alphabets (url-safe §5 '-”_'
// and standard §4 '+”/') and hex. '-' is INCLUDED so a url-safe-base64 token
// that embeds '-' is still caught by its run (excluding '-' left an ~11% bypass
// for 32-byte url-safe tokens whose '-' happened to break every 24-run — measured).
//
// TWO-STAGE DETECTION:
//
//  1. UNCONDITIONAL run scan — a >= highEntropyMinLen UNBROKEN run over
//     [A-Za-z0-9_+/-] flags the value REGARDLESS of the structured-id exemption.
//     This catches every raw secret WITHOUT internal separators (hex, base64) —
//     the realistic "a client bug put a raw key in the URL" case — at 100%. A
//     structured identifier never has a 24-char unbroken run, so this stage never
//     over-scrubs one.
//
//  2. STRUCTURED-ID EXEMPTION for the rest (values with separators that DON'T have
//     a 24-run): exempt a clearly hyphen-joined human id, judged by LEXICAL
//     content — every group WORD-LIKE (single-case word or decimal number), which
//     a mixed-case base64 chunk or a long hex-with-letters chunk is NOT (those are
//     redacted). Shape alone is attacker-satisfiable (RED found a 3x12-chunked
//     secret slipped a shape-only check); the lexical test rejects the common
//     secret encodings.
//
// ACCEPTED RESIDUAL BOUND (documented, per RED review): the ONLY residual is a
// secret deliberately chunked into >=3 SINGLE-CASE-ALPHABETIC groups of <=12 chars
// with no hex-letter runs >= HexChunkMinLen (a lowercase- or uppercase-only base32
// alphabet, e.g. abcdefghijkl-mnopqrstuvwx-…). Such a value is lexically
// indistinguishable from a hyphenated model id (deepseek-r1-distill-qwen-32b
// carries the SAME 24-char entropy budget), so it passes stage 2. This is NOT
// closable by any length/case/count rule without a word dictionary
// (over-engineering for a defense-in-depth URL/UA backstop). Mixed-case base64
// chunks AND small-hex chunks (md5/sha display grouping) ARE now caught
// (isWordLikeGroup). The residual does not widen exposure for any REAL credential:
// Hanzo keys carry a pk-/sk- prefix (isAPIKey, caught at any
// length/shape), JWTs are eyJ-prefixed (looksLikeJWT), and request/response BODIES
// are never read. It is an adversary DELIBERATELY base32-chunking their OWN secret
// into a URL path to seed an admin-only audit row — contrived, low-value. The
// realistic accidental leak (an unbroken raw key) is caught by stage 1.
//
// A canonical UUID is exempt (its longest run is 12; the check documents intent).
func looksLikeHighEntropyToken(s string) bool {
	if len(s) < highEntropyMinLen {
		return false
	}
	// Stage 1 — unconditional: a long unbroken run is always a secret.
	if hasHighEntropyRun(s) {
		return true
	}
	// Stage 2 — separated values: redact unless it's a lexical structured id.
	if isUUID(s) || isStructuredID(s) {
		return false
	}
	// A >=24-length value with separators, not a UUID, not a structured id — e.g.
	// "sk.live.LONGSECRET…" dotted, or a chunk pattern that is not word-like.
	return true
}

// hasHighEntropyRun reports whether s contains an unbroken run of
// >= highEntropyMinLen high-entropy chars where the run alphabet EXCLUDES '-'
// (and '.'): a real separator breaks the run. This is stage 1 — it fires only on
// a genuinely UNBROKEN opaque blob (a raw hex/base64 key with no separators), so
// it never catches a hyphenated identifier (which stage 2 then classifies). A
// url-safe-base64 secret that embeds '-' still trips because the run on ONE side
// of the hyphen is >= 24 (verified: "AbCdEf-GhIjKl_MnOpQrStUvWxYz012345" -> 27).
func hasHighEntropyRun(s string) bool {
	run := 0
	for _, r := range s {
		if isUnbrokenTokenChar(r) {
			run++
			if run >= highEntropyMinLen {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// isUnbrokenTokenChar is the stage-1 run alphabet: base64/hex MINUS '-' (and the
// implicit exclusion of '.', space, etc.). '_' '+' '/' are kept — they appear
// inside opaque tokens and are not identifier separators.
func isUnbrokenTokenChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') || r == '_' || r == '+' || r == '/'
}

// idPartMaxLen bounds a hyphen-group length in the structured-id exemption. 12
// covers the longest word in real model ids ("embedding", "20241022", "preview")
// while a raw secret's random hyphen groups routinely exceed it.
const idPartMaxLen = 12

// isStructuredID reports whether s is a hyphen-joined human identifier (a model
// name, slug): >= 3 hyphen groups where EVERY group is non-empty, <= idPartMaxLen
// chars, and WORD-LIKE. The word-like test is the anti-bypass core — a raw secret
// chunk cannot satisfy it — so the exemption is not attacker-satisfiable by shape.
func isStructuredID(s string) bool {
	groups := strings.Split(s, "-")
	if len(groups) < 3 {
		return false
	}
	for _, g := range groups {
		if g == "" || len(g) > idPartMaxLen || !isWordLikeGroup(g) {
			return false
		}
	}
	return true
}

// isWordLikeGroup reports whether a hyphen group looks like a model-id token (a
// dictionary word or a decimal number) rather than a random secret chunk. Two
// lexical signals reject a secret chunk:
//   - MIXED CASE (both upper and lower letters) — base64 tokens are dense
//     mixed-case; real model tokens are single-case ("sonnet", "Instruct", "3").
//   - an all-hex-with-letters run (>= HexChunkMinLen chars, all [0-9a-fA-F], and
//     not all-digits) — a hex secret chunk ("dead", "beef", "cafebabe"); a version
//     date ("20241022", all digits) is NOT hex-with-letters, so it stays
//     word-like. The threshold is 4: RED re-review verified that NO real model-id
//     group is all-hex-with-letters of length >= 4, so 4 (vs the prior 8) closes
//     the small-hex-chunk leak (md5/sha shown in "xxxx-xxxx" display grouping)
//     with zero model-id over-scrub.
func isWordLikeGroup(g string) bool {
	var hasUpper, hasLower, allHex, allDigit bool = false, false, true, true
	for _, r := range g {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		}
		if r < '0' || r > '9' {
			allDigit = false
		}
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			allHex = false
		}
	}
	if hasUpper && hasLower {
		return false // dense mixed-case → base64 secret chunk, not a word.
	}
	if allHex && !allDigit && len(g) >= HexChunkMinLen {
		return false // hex-with-letters → hex secret chunk (not a version date).
	}
	return true
}

// HexChunkMinLen is the length at/above which an all-hex-with-letters group is
// treated as a secret chunk rather than a word. 4 is the tightest bound that does
// not over-scrub any real model-id group (RED-verified across 19 model ids) while
// catching hex secrets displayed in short groups (md5 "xxxx-xxxx", uuid-ish).
const HexChunkMinLen = 4

// isUUID reports whether s is a canonical 8-4-4-4-12 hex UUID (case-insensitive).
// Used to exempt uuids from the high-entropy secret test — a uuid is a legitimate
// resource id, not a credential.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// maxUserAgentLen caps the recorded User-Agent so an oversized UA can neither
// bloat the trail nor smuggle a long payload. 512 chars covers every real UA.
const maxUserAgentLen = 512

// FreeText scrubs credential-shaped words out of client-controlled free
// text (the User-Agent) and caps its length. It tokenizes on a broad delimiter
// superset — whitespace and the punctuation that commonly glues a token into a
// UA/header value (= ; , : / ( ) [ ] { } " ' < > | and backslash) — scrubs each
// token, and rebuilds the string preserving the exact delimiters between tokens.
//
// RED-review hardening (finding: UA tokenizer split on too few delimiters, and
// strings.ReplaceAll was substring-fragile): this walks the string in one pass
// (token-run, delimiter-run, …) and replaces each credential token IN PLACE, so a
// secret delimited by ':' '/' '(' etc. is caught and a token that is a substring
// of another is never mis-replaced. A normal UA ("Mozilla/5.0 (Macintosh …)") is
// unchanged because none of its words is credential-shaped.
func FreeText(s string) string {
	if len(s) > maxUserAgentLen {
		s = s[:maxUserAgentLen]
	}
	return Text(s)
}

// Text scrubs credential-shaped words out of free text, leaving every other
// byte alone. It is the same walk FreeText performs, without that one's
// User-Agent length cap — the cap is a policy about what a trail records, not
// part of the rule for what a credential looks like, and braiding the two meant
// the only shared scrubber for prose also truncated it.
//
// The rule is Token's, so a value is judged by its SHAPE. That is what the
// key-name scrubbers cannot do: an upstream that answers "Invalid API key: sk-…"
// gives no field name to key on, so the credential rides out in the message. Any
// surface relaying text it did not author — an upstream refusal, a tool's log
// tail — passes it through here first.
func Text(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	start := -1 // start index of the current token run, or -1 in a delimiter run.
	flush := func(end int) {
		if start >= 0 {
			b.WriteString(Token(s[start:end]))
			start = -1
		}
	}
	for i, r := range s {
		if isFreeTextDelimiter(r) {
			flush(i)
			b.WriteRune(r)
		} else if start < 0 {
			start = i
		}
	}
	flush(len(s))
	return b.String()
}

// isFreeTextDelimiter reports whether r separates tokens in free text (a UA /
// header value). Deliberately broad so a credential can't hide behind an unusual
// separator.
//
// RED re-review (UA bypass persists): '.' '@' '#' '~' are INCLUDED — a prefixed
// key glued by one of them (client@sk-live-KEY, app.sk-live-KEY, build#pk-KEY)
// otherwise stayed one token whose PREFIX was no longer sk-/pk-, so isAPIKey
// missed it. Splitting on them exposes the bare key to Token. Real UA dots
// are numeric version separators (<24, safe) and are split harmlessly. A JWT
// (eyJ.h.p.s) is handled up-front by Token via looksLikeJWT before any
// tokenizer runs on a URL path segment; in free text a dotted JWT will split,
// but each ~40-char base64 part is itself a >=24 high-entropy run, so every part
// is still redacted.
func isFreeTextDelimiter(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '=', ';', ',', ':', '/', '\\',
		'(', ')', '[', ']', '{', '}', '"', '\'', '<', '>', '|', '&', '?',
		'.', '@', '#', '~':
		return true
	}
	return false
}

// scrubCredentialSegments applies Token to every segment of a path, so the
// recorded Path can never carry a credential even if a future route embeds one.

// Prefixes is every opaque-key spelling the estate recognizes at the door, and
// this is the ONE authority for it. It used to live in package cloud with a note
// asking apps/admission to keep its copy in step by hand; a leaf means neither
// has to remember.
var Prefixes = []string{"pk-", "sk-"}

// IsKey reports whether tok is an opaque, backend-validated key rather than a
// JWT, so a caller can skip JWT parsing for it and the scrub can recognise one on
// sight.
func IsKey(tok string) bool {
	for _, p := range Prefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}
