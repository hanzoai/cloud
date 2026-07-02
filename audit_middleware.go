package cloud

// The audit middleware — the ONE place every security-relevant request is
// recorded to the tamper-evident trail (decomplected: one function, every route).
//
// PLACEMENT (why it sits exactly where serve.go puts it). The pipeline is
// Recover → RequestID → Logger → SanitizeIdentity → AuditTrail → BillingGate →
// subsystems. AuditTrail runs:
//   - AFTER SanitizeIdentity, so the actor/isAdmin it records come from a
//     VALIDATED IAM principal (the sanitized X-User-* headers), never a raw
//     client header. The request being audited cannot forge its own actor.
//   - BEFORE BillingGate and every subsystem, so it WRAPS the whole handler
//     chain and observes the FINAL outcome — including a 402/503 billing denial
//     and a 403 admin-guard denial (both security-relevant) — via the response
//     status after Continue(), exactly like the Logger middleware reads it.
//
// WHAT IT CAPTURES: metadata only — actor, action (method+route family),
// resource, source ip, user agent, request id, auth context, and the outcome
// (result/status/reason). It NEVER reads the request or response BODY, so a
// secret in a POST body can never reach a record through this path. before/after
// diffs are the job of explicit emit points (audit.Recorder.Append with a
// redacted diff), not this middleware.
//
// WHAT IT RECORDS (the coverage predicate, auditable in one place — see
// isSecurityRelevant): every mutating request (POST/PUT/PATCH/DELETE), every
// /v1/admin/* request (read or write — admin reads are AC-relevant), and every
// auth-failure outcome (401/403) on ANY method (a denied GET is an access-control
// event). Safe, unauthenticated reads (a 200 GET on a public route) are NOT
// audited — that is request-log noise, not a security event, and auditing it
// would bury the signal and balloon the trail.
//
// FAIL MODE (AU-5): if the trail write fails on a request we decided to audit,
// the CLIENT gets a fail-closed 503 rather than a success it can rely on — the
// AU-5 "response to an audit logging process failure" is to interrupt, not to
// operate silently unlogged. A write to local SQLite is sub-millisecond, so this
// is a real integrity stance, not a latency tax. When no Recorder is configured
// the middleware is a no-op passthrough (an unconfigured deployment is never
// blocked), exactly like BillingGate.
//
// PRECISE SEMANTIC (do not over-read the 503). This is POST-RESPONSE audit: the
// handler has already run when the record is written, so a 503 here means "this
// event could not be RECORDED", NOT "the action did not execute". PREVENTION is
// the access-control layer's job and runs BEFORE the action — SanitizeIdentity
// (identity can't be forged) + the per-route admin guard both execute inside
// c.Next() ahead of any side effect. The audit trail's job is DETECTION and
// ACCOUNTABILITY (tamper-evident record of what happened), which it does. On a
// persistent audit-store outage every mutation returns 503 (loud, logged), so
// the system degrades to read-only rather than mutating unaudited — the intended
// compliance posture.
//
// PANIC BOUND. If a handler PANICS, the outermost middleware.Recover catches it
// and renders 500 with a full stack trace (loud, never silent); the panic unwinds
// PAST this middleware's post-c.Next() code, so a panicking request is not written
// to the trail. This is an accepted bound, not an evasion: an attacker cannot turn
// a panic into a SUCCESSFUL-but-unaudited mutation (a panic yields 500, not a
// completed action), and every panic is already captured by Recover's logging.
// Normal outcomes — including billing 402/503 and admin 403 denials, which return
// through c.Next() rather than panicking — are always audited.

import (
	"errors"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// AuditTrail returns the audit middleware bound to rec. A nil rec makes it a
// no-op passthrough so callers always Use() it unconditionally.
func AuditTrail(rec *audit.Recorder) zip.Handler {
	if rec == nil {
		return func(c *zip.Ctx) error { return c.Next() }
	}
	return func(c *zip.Ctx) error {
		// Capture the pre-decision inputs BEFORE running the chain: the request
		// context is recycled by Fiber after the handler returns, so identity and
		// request fields must be read now (mirrors BillingGate capturing usage by
		// value pre-Record).
		method := c.Method()
		path := c.Path()

		err := c.Next()

		// Resolve the EFFECTIVE status. A handler may set it on the response
		// directly (c.Status(...).JSON(...)) OR return a *zip.HTTPError that the
		// framework's error handler renders AFTER this middleware unwinds — in the
		// latter case the response still reads 200 here, so the returned error is
		// the authoritative source of a 401/403. Prefer the error's status when it
		// carries one; this is what makes admin-guard denials (which return
		// ErrForbidden) get audited as 403.
		status := effectiveStatus(c.Fiber().Response().StatusCode(), err)
		if !isSecurityRelevant(method, path, status) {
			return err // not an audited event; pass the handler result through.
		}

		record := audit.Record{
			Actor:    actorFromCtx(c),
			Action:   method + " " + routeFamily(path),
			Resource: resourceFromPath(path),
			Auth:     authFromCtx(c),
			Outcome:  outcomeOf(status, err),
			SourceIP: ClientIP(c),
			// User-Agent is client-controlled free text; a misconfigured/malicious
			// client could embed a bearer token in it. Scrub credential-shaped runs
			// (and cap length) so the UA can never carry a secret into the record.
			UserAgent: scrubFreeText(c.Header("User-Agent")),
			RequestID: c.RequestID(),
			Method:    method,
			// Path is scrubbed of any credential-shaped segment: Hanzo routes use
			// identifiers (:name/:slug/:id), not secrets, but a token that ever
			// rides in the path (an hk-/sk-/pk-/fw_/hz_ key) must never be recorded
			// verbatim. resourceFromPath applies the same scrub to the resource id.
			Path: scrubCredentialSegments(path),
		}

		if _, aerr := rec.Append(c.Context(), record); aerr != nil {
			// AU-5: could not record a security-relevant event — fail the request
			// closed. Do NOT leak the audit error to the client; log it loud.
			c.Log().Error("audit append failed — failing request closed",
				"path", path, "method", method, "err", aerr)
			return c.JSON(503, map[string]any{
				"error": map[string]string{
					"code":    "audit_unavailable",
					"message": "Request could not be securely recorded",
				},
			})
		}
		return err
	}
}

// isSecurityRelevant is the coverage predicate — the ONE place that decides which
// requests enter the audit trail (AC/AU scope). Kept tiny and total so the
// coverage matrix is reviewable at a glance. Order matters and is deliberate:
// the security-relevant conditions are checked FIRST and are unconditional, so
// the health-probe exemption can NEVER be used to evade audit of a mutation or a
// denial (a POST/DELETE, or any 401/403, is always audited whatever the path).
//   - any auth-failure outcome (401/403) on any method (a denied access attempt),
//   - any /v1/admin/* request (admin reads are access-control-relevant),
//   - any mutating request (POST/PUT/PATCH/DELETE).
//
// Only then, a genuine liveness probe (a GET to an exact health route) is
// exempted — it is neither a mutation, a denial, nor an admin call, so it is pure
// request-log noise. The exemption matches EXACT probe paths, never an arbitrary
// path that merely ends in "/health" (which a wildcard/attacker-named segment
// like POST /v1/admin/orgs/x/health could otherwise abuse to slip past audit).
func isSecurityRelevant(method, path string, status int) bool {
	// Unconditional security signals — never suppressed by any path shape. A
	// denial, an admin call, or a mutation is ALWAYS audited, whatever the path
	// (so a wildcard/attacker-named "/health" tail cannot evade it).
	if status == 401 || status == 403 {
		return true
	}
	if strings.HasPrefix(path, "/v1/admin/") {
		return true
	}
	if isMutation(method) {
		return true
	}
	// Everything left is a safe read (non-mutating, non-admin, non-denied). None
	// are audited — they are request-log noise, not security events. (Liveness
	// probes fall here too; there is no separate case because the answer is the
	// same: not recorded.)
	return false
}

// isMutation reports whether a method changes state.
func isMutation(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// actorFromCtx builds the Actor from the sanitized identity headers, gating
// ANTI-FORGERY of the recorded actor on a VALIDATED principal.
//
// The authoritative "this request carried a validated principal" signal is a
// non-empty X-User-Id (c.User()): SanitizeIdentity sets X-User-Id ONLY from a
// JWT it verified, and strips any client-supplied copy on ingress. A request
// with no principal — anonymous, OR one bearing an INVALID/garbage bearer that
// failed validation — has an empty c.User().
//
// In that unvalidated case the org header is NOT trustworthy: SanitizeIdentity's
// Phase-1 residual restores a client-supplied X-Org-Id for the data path, so an
// anonymous attacker could send X-Org-Id: victim-org and, if we recorded it,
// forge a FALSE ATTRIBUTION (an event stamped with a victim's org). So when there
// is no validated sub, the actor is left EMPTY — the record stands as an honest
// anonymous event identified by SourceIP, never mis-attributed to a claimed org.
//
// With a validated sub, org/sub/email all reflect the verified principal and are
// recorded authoritatively.
func actorFromCtx(c *zip.Ctx) audit.Actor {
	if !principal.Validated(c) {
		// No validated principal — do not trust the client-asserted org.
		return audit.Actor{}
	}
	return audit.Actor{
		Org:   strings.TrimSpace(c.Org()),
		Sub:   strings.TrimSpace(c.User()),
		Email: strings.TrimSpace(c.UserEmail()),
	}
}

// authFromCtx records HOW the caller authenticated and the VALIDATED admin bit.
// IsAdmin comes from c.IsAdmin() (the sanitized X-User-IsAdmin, true only for a
// verified global admin), never a raw header. Method is inferred from the
// presence/shape of a credential: an Authorization/X-Authorization bearer or a
// session cookie ⇒ "jwt" (or "api-key" for an opaque hk-/sk- token); none ⇒
// "none".
func authFromCtx(c *zip.Ctx) audit.AuthContext {
	return audit.AuthContext{
		Method:  authMethodOf(c),
		IsAdmin: c.IsAdmin(),
	}
}

// authMethodOf classifies the credential kind WITHOUT capturing it — it inspects
// only the token PREFIX (never stores the value). Order mirrors the sanitizer's
// extraction (bearer, then cookie).
func authMethodOf(c *zip.Ctx) string {
	auth := c.Header("Authorization")
	if auth == "" {
		auth = c.Header("X-Authorization")
	}
	if tok := bearerFromAuth(auth); tok != "" {
		if isAPIKey(tok) {
			return "api-key"
		}
		return "jwt"
	}
	if basicFromAuth(auth) != "" {
		return "basic"
	}
	for _, name := range cookieTokenNames {
		if c.Fiber().Cookies(name) != "" {
			return "jwt"
		}
	}
	return "none"
}

// effectiveStatus reconciles the response status with a returned error. If the
// handler returned a *zip.HTTPError (e.g. ErrForbidden), its Status is
// authoritative — the framework renders it after this middleware unwinds, so the
// live response status does not yet reflect it. A non-HTTPError returned error
// with a still-2xx response means the framework will render a 500. Otherwise the
// response status stands.
func effectiveStatus(respStatus int, err error) int {
	if err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status != 0 {
			return he.Status
		}
		// A non-HTTPError propagating up renders as 500, unless the handler already
		// set an explicit error status on the response.
		if respStatus < 400 {
			return 500
		}
	}
	return respStatus
}

// outcomeOf maps the final HTTP status + handler error to an audit Outcome.
// 2xx/3xx ⇒ success; 401/403 ⇒ deny; everything else (4xx/5xx) ⇒ error. The
// reason is a short, non-sensitive label derived from the status class — never a
// raw upstream error body (which could echo sensitive detail).
func outcomeOf(status int, err error) audit.Outcome {
	switch {
	case status == 401:
		return audit.Outcome{Result: "deny", Status: status, Reason: "unauthenticated"}
	case status == 403:
		return audit.Outcome{Result: "deny", Status: status, Reason: "forbidden"}
	case status >= 500:
		return audit.Outcome{Result: "error", Status: status, Reason: "server_error"}
	case status >= 400:
		return audit.Outcome{Result: "error", Status: status, Reason: "client_error"}
	default:
		return audit.Outcome{Result: "success", Status: status}
	}
}

// routeFamily reduces a concrete path to its stable route family for the action
// verb, dropping trailing high-cardinality id segments so "DELETE /v1/admin/
// orgs/acme" and "DELETE /v1/admin/orgs/globex" share the action "DELETE
// /v1/admin/orgs". The full concrete path is preserved separately in Record.Path.
func routeFamily(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	// Keep the leading "v1/<subsystem>/<noun>" and stop before an id-looking tail.
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if looksLikeID(s) {
			break
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return "/" + strings.Join(segs, "/")
	}
	return "/" + strings.Join(out, "/")
}

// resourceFromPath derives the {type,id} resource from a /v1/<subsystem>/<type>/
// [<id>] path. Type is the noun after the subsystem; ID is the following segment
// when it looks like an identifier. Best-effort — the Action verb + Path are the
// authoritative locator; this is a convenience for filtering by resource type.
func resourceFromPath(path string) audit.Resource {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	// segs: [v1, <subsystem>, <type>, <id?>, ...]
	if len(segs) < 3 {
		return audit.Resource{}
	}
	res := audit.Resource{Type: segs[2]}
	if len(segs) >= 4 && looksLikeID(segs[3]) {
		res.ID = scrubToken(segs[3])
	}
	return res
}

// scrubToken replaces a path segment that is a credential-shaped token with a
// fixed marker, so a secret that ever appears in a URL is never recorded
// verbatim. It catches, in increasing generality:
//   - a known API-key prefix (hk-/sk-/pk-/fw_/hz_ — what isAPIKey recognizes),
//     ALSO after percent-decoding, so hk%2DKEY can't slip the prefix check,
//   - a JWT (three base64url parts split by '.', starting eyJ),
//   - a long, high-entropy base64url/hex run (>=24) — a raw API key / access
//     token / hex secret that carries no telltale prefix.
//
// A normal identifier passes through unchanged: a UUID (5 hyphen-split groups,
// each short), a slug, a numeric id, a dotted model name — none is a long
// unbroken high-entropy blob. See TestScrubToken_NoFalsePositives.
//
// RED-review hardening (finding: scrub bypass): the entropy test now (a) accepts
// the FULL base64url alphabet incl. '-' and '_' (RFC 4648 §5), (b) does NOT
// require a digit (an all-alpha opaque key is still a secret), (c) percent-
// decodes first so %2D/%5F can't hide structure, and (d) drops the threshold to
// 24 (short enough for a 128-bit base64 or a 24-hex key, long enough that no
// human-readable slug reaches it).
func scrubToken(seg string) string {
	dec := percentDecode(seg)
	if isAPIKey(seg) || isAPIKey(dec) ||
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
// with no hex-letter runs >= hexChunkMinLen (a lowercase- or uppercase-only base32
// alphabet, e.g. abcdefghijkl-mnopqrstuvwx-…). Such a value is lexically
// indistinguishable from a hyphenated model id (deepseek-r1-distill-qwen-32b
// carries the SAME 24-char entropy budget), so it passes stage 2. This is NOT
// closable by any length/case/count rule without a word dictionary
// (over-engineering for a defense-in-depth URL/UA backstop). Mixed-case base64
// chunks AND small-hex chunks (md5/sha display grouping) ARE now caught
// (isWordLikeGroup). The residual does not widen exposure for any REAL credential:
// Hanzo keys are hk-/sk-/pk-/fw_/hz_-prefixed (isAPIKey, caught at any
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
//   - an all-hex-with-letters run (>= hexChunkMinLen chars, all [0-9a-fA-F], and
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
	if allHex && !allDigit && len(g) >= hexChunkMinLen {
		return false // hex-with-letters → hex secret chunk (not a version date).
	}
	return true
}

// hexChunkMinLen is the length at/above which an all-hex-with-letters group is
// treated as a secret chunk rather than a word. 4 is the tightest bound that does
// not over-scrub any real model-id group (RED-verified across 19 model ids) while
// catching hex secrets displayed in short groups (md5 "xxxx-xxxx", uuid-ish).
const hexChunkMinLen = 4

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

// scrubFreeText scrubs credential-shaped words out of client-controlled free
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
func scrubFreeText(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > maxUserAgentLen {
		s = s[:maxUserAgentLen]
	}
	var b strings.Builder
	b.Grow(len(s))
	start := -1 // start index of the current token run, or -1 in a delimiter run.
	flush := func(end int) {
		if start >= 0 {
			b.WriteString(scrubToken(s[start:end]))
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
// key glued by one of them (client@sk-live-KEY, app.sk-live-KEY, build#hk-KEY)
// otherwise stayed one token whose PREFIX was no longer sk-/hk-, so isAPIKey
// missed it. Splitting on them exposes the bare key to scrubToken. Real UA dots
// are numeric version separators (<24, safe) and are split harmlessly. A JWT
// (eyJ.h.p.s) is handled up-front by scrubToken via looksLikeJWT before any
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

// scrubCredentialSegments applies scrubToken to every segment of a path, so the
// recorded Path can never carry a credential even if a future route embeds one.
// Route nouns/ids pass through unchanged (they are not isAPIKey-shaped).
func scrubCredentialSegments(path string) string {
	if !strings.ContainsAny(path, "/") {
		return scrubToken(path)
	}
	segs := strings.Split(path, "/")
	changed := false
	for i, s := range segs {
		if scrubbed := scrubToken(s); scrubbed != s {
			segs[i] = scrubbed
			changed = true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(segs, "/")
}

// looksLikeID heuristically flags a path segment as a high-cardinality id (a
// uuid, a long hex/opaque token, or a numeric id) vs a fixed route noun. Used to
// collapse route families; false positives only make the action slightly more
// specific, never leak anything.
func looksLikeID(s string) bool {
	if s == "" {
		return false
	}
	if len(s) >= 16 { // long opaque/uuid-ish segment
		return true
	}
	allDigits := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	return allDigits && len(s) > 0
}
