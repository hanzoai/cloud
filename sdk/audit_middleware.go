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
// /v1/admin/* request (read or write — admin reads are AC-relevant), every
// auth-failure outcome (401/403) on ANY method (a denied GET is an access-control
// event), and every PRIVILEGED request — any request made with platform authority
// (principal.Super), in the admin org or inside another tenant — including a
// successful READ. Safe reads by anyone else (a 200 GET on a normal route) are NOT
// audited — that is request-log noise, not a security event, and auditing it would
// bury the signal and balloon the trail. Neither is a site's telemetry beacon (POST
// /v1/event, /v1/event/replay) that is not denied and carries no platform authority:
// the event plane is its record.
//
// The privileged clause is the one that makes an admin's read of another tenant's
// KMS secret leave a record: actor + their home org + the target org + the
// secret PATH (never the value — this middleware never reads bodies) + outcome.
//
// FAIL MODE (AU-5): if the trail write fails on a request we decided to audit,
// the CLIENT gets a fail-closed 503 rather than a success it can rely on — the
// AU-5 "response to an audit logging process failure" is to interrupt, not to
// operate silently unlogged. A write to local SQLite is sub-millisecond, so this
// is a real integrity stance, not a latency tax. When no Recorder is configured
// the middleware is a no-op passthrough (an unconfigured deployment is never
// blocked), exactly like BillingGate.
//
// A CHANGE IS RECORDED BEFORE IT RUNS. Every mutation is written to the trail as
// begun before the handler is entered — a REST mutation here, an envelope's (MCP,
// the call plane) by the authorizer once zip names its operation (Ahead) — and a
// trail that cannot take that record refuses the request with nothing applied. The
// outcome record, with the same request id, is written after the chain runs, the
// only time the outcome is known; if that one cannot be written, the begun record
// already names who changed what, and the client keeps the answer it was given
// rather than a 503 that would say an applied change failed. A read is recorded
// once, after it runs, and a read whose record cannot be written answers 503
// instead of its data. PREVENTION is still the access-control layer's job —
// SanitizeIdentity and the per-route admin guard run inside c.Next() ahead of any
// side effect; the trail's job is that no privileged change lands unrecorded.
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
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/scrub"
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

		// Whether this request ACTS ON someone else's tenant, captured BEFORE the
		// chain runs for the same reason as method/path: the ctx is recycled after
		// the handler returns. One value serves both uses below — the coverage
		// predicate and the recorded Actor.Home.
		home := crossOrgHome(c)
		// Whether this request carries platform authority, captured before the chain
		// for the same reason: every such request is a privileged action (AC-6(9)).
		// A support session is one too — a SuperAdmin at the keyboard behind the
		// person's token — so every request it makes is recorded, reads included,
		// and every row names the operator (actorFromCtx).
		_, imp := principal.Impersonated(c)
		super := principal.Super(c) || imp

		// A CHANGE IS WRITTEN AHEAD. The record below is written after the chain
		// ran, which is the only time the outcome is known — so on its own, a
		// mutation whose record could not be written had ALREADY happened when the
		// client was told 503: a privileged change could land with no record at all.
		// So every mutation is first recorded as begun, before anything runs, and a
		// trail that cannot take that record refuses the request with nothing
		// applied. The outcome record follows as before.
		//
		// A REST mutation is known from its method here. An envelope (/mcp, the call
		// plane) names its operation only once zip dispatches it, so the authorizer
		// writes it then (Ahead, called by Rule as it allows the op), and an
		// envelope carrying a read writes nothing ahead.
		ahead := false
		begin := func(did, didPath string) error {
			if ahead || !isMutation(did) || (telemetry(did, didPath) && !super) {
				return nil
			}
			r := audit.Record{
				Actor:     actorFromCtx(c, home),
				Action:    did + " " + RouteFamily(didPath),
				Resource:  resourceFromPath(didPath),
				Auth:      authFromCtx(c),
				Outcome:   audit.Outcome{Result: begun},
				SourceIP:  geo.IP(c),
				UserAgent: scrubFreeText(c.Header("User-Agent")),
				RequestID: c.RequestID(),
				Method:    method,
				Path:      scrubCredentialSegments(path),
			}
			if _, err := rec.Append(c.Context(), r); err != nil {
				c.Log().Error("audit append failed before the change — refused, nothing applied",
					"path", path, "method", did, "err", err)
				return errUnrecorded
			}
			ahead = true
			return nil
		}
		if !envelope(path) {
			if err := begin(method, path); err != nil {
				return unrecorded(c)
			}
		}
		c.Fiber().Locals(aheadSlot{}, func(op zip.Op) error {
			did, didPath := op.Method, op.Path
			if Matched(c) == op.Path {
				did, didPath = method, path
			}
			return begin(did, didPath)
		})

		err := c.Next()

		// WHICH OPERATION RAN, not which door it came through. Everything above
		// reads the path the TRANSPORT carried, and for a typed op that is only
		// the operation over REST: over MCP it is /mcp and over the call plane it
		// is /.well-known/zip/op/<name>. Rule notes the operation as it passes
		// (note.go), so when the matched route is not the operation's own, the
		// operation is what the record is judged and written from.
		//
		// The concrete request path is left alone below. Method and Path describe
		// the REQUEST and are still true; Action and Resource describe what was
		// DONE, and only those move. Over REST nothing moves at all — the matched
		// route IS the operation's route — so no existing record changes shape.
		did, didPath := method, path
		if op, ok := Noted(c); ok && Matched(c) != op.Path {
			did, didPath = op.Method, op.Path
		}

		// Resolve the EFFECTIVE status. A handler may set it on the response
		// directly (c.Status(...).JSON(...)) OR return a *zip.HTTPError that the
		// framework's error handler renders AFTER this middleware unwinds — in the
		// latter case the response still reads 200 here, so the returned error is
		// the authoritative source of a 401/403. Prefer the error's status when it
		// carries one; this is what makes admin-guard denials (which return
		// ErrForbidden) get audited as 403.
		status := EffectiveStatus(c.Fiber().Response().StatusCode(), err)
		if !isSecurityRelevant(did, didPath, status, super) {
			return err // not an audited event; pass the handler result through.
		}

		record := audit.Record{
			Actor:    actorFromCtx(c, home),
			Action:   did + " " + RouteFamily(didPath),
			Resource: resourceFromPath(didPath),
			Auth:     authFromCtx(c),
			Outcome:  outcomeOf(status, err),
			SourceIP: geo.IP(c),
			// User-Agent is client-controlled free text; a misconfigured/malicious
			// client could embed a bearer token in it. Scrub credential-shaped runs
			// (and cap length) so the UA can never carry a secret into the record.
			UserAgent: scrubFreeText(c.Header("User-Agent")),
			RequestID: c.RequestID(),
			Method:    method,
			// Path is scrubbed of any credential-shaped segment: Hanzo routes use
			// identifiers (:name/:slug/:id), not secrets, but a token that ever
			// rides in the path (a pk-/sk- key) must never be recorded
			// verbatim. resourceFromPath applies the same scrub to the resource id.
			Path: scrubCredentialSegments(path),
		}

		if _, aerr := rec.Append(c.Context(), record); aerr != nil {
			if ahead {
				// The change was recorded as begun before it ran, so the trail
				// already names who did what. Answering 503 now would tell the
				// client an applied change failed; the response it got stands.
				c.Log().Error("audit append failed after the change — its begun record stands",
					"path", path, "method", method, "err", aerr)
				return err
			}
			// AU-5: could not record a security-relevant event — fail the request
			// closed. Do NOT leak the audit error to the client; log it loud.
			c.Log().Error("audit append failed — failing request closed",
				"path", path, "method", method, "err", aerr)
			return unrecorded(c)
		}
		return err
	}
}

// begun is the result a change is recorded with before it runs; its outcome record,
// with the same request id, follows.
const begun = "begun"

// errUnrecorded refuses a change the trail could not record before it ran.
var errUnrecorded = zip.Errorf(http.StatusServiceUnavailable, "Request could not be securely recorded")

// unrecorded is the fail-closed answer to a request the trail could not record.
func unrecorded(c *zip.Ctx) error {
	return c.JSON(http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{
			"code":    "audit_unavailable",
			"message": "Request could not be securely recorded",
		},
	})
}

// envelope reports a path that carries an operation named in its body — MCP and
// the call plane — whose method says nothing about whether it changes anything.
func envelope(path string) bool {
	return path == "/mcp" || strings.HasPrefix(path, "/mcp/") || strings.HasPrefix(path, "/.well-known/zip/")
}

// aheadSlot is the request-scoped slot AuditTrail leaves for the authorizer: the
// writer of a change's begun record, for an envelope whose operation only zip knows.
type aheadSlot struct{}

// Ahead records the change op is about to make, before it runs, on the trail of the
// request that carried it; a trail that cannot take it is the refusal. Rule calls it
// once every other question has allowed the op. With no request, or no trail, there
// is nothing to write and it allows.
func Ahead(ctx context.Context, op zip.Op) error {
	c, live := Request(ctx)
	if !live {
		return nil
	}
	write, ok := c.Fiber().Locals(aheadSlot{}).(func(zip.Op) error)
	if !ok {
		return nil
	}
	return write(op)
}

// isSecurityRelevant is the coverage predicate — the ONE place that decides which
// requests enter the audit trail (AC/AU scope). Kept tiny and total so the
// coverage matrix is reviewable at a glance. Order matters and is deliberate:
// the security-relevant conditions are checked FIRST and are unconditional, so
// the health-probe exemption can NEVER be used to evade audit of a mutation or a
// denial (a POST/DELETE, or any 401/403, is always audited whatever the path).
//   - any auth-failure outcome (401/403) on any method (a denied access attempt),
//   - any PRIVILEGED request (made with platform authority, read or write),
//   - any /v1/admin/* request (admin reads are access-control-relevant),
//   - any mutating request (POST/PUT/PATCH/DELETE).
//
// Only then, a genuine liveness probe (a GET to an exact health route) is
// exempted — it is neither a mutation, a denial, nor an admin call, so it is pure
// request-log noise. The exemption matches EXACT probe paths, never an arbitrary
// path that merely ends in "/health" (which a wildcard/attacker-named segment
// like POST /v1/admin/orgs/x/health could otherwise abuse to slip past audit).
func isSecurityRelevant(method, path string, status int, super bool) bool {
	// Unconditional security signals — never suppressed by any path shape. A
	// denial, an admin call, or a mutation is ALWAYS audited, whatever the path
	// (so a wildcard/attacker-named "/health" tail cannot evade it).
	if status == 401 || status == 403 {
		return true
	}
	// A PRIVILEGED action is always audited — read or write, success or failure,
	// in the admin org or inside another tenant.
	//
	// Platform authority is a property of the CALLER, not of the route: a
	// SuperAdmin reading another tenant's data is a plain GET on a plain tenant
	// route, and a SuperAdmin reading the whole fleet is a GET on a surface whose
	// prefix says nothing. Neither is a mutation, neither is under /v1/admin/, and
	// neither is a denial, so every other clause would call them not security
	// relevant — the highest-value reads in the fleet would be the ones that leave
	// no record.
	//
	// It is deliberately NOT scoped to secrets or to any subsystem. KMS keeps
	// knowing nothing about audit, audit keeps knowing nothing about routes, and
	// every subsystem gets the coverage at once.
	//
	// Volume is bounded by construction: SuperAdmin is owner == "admin", a handful
	// of named people provisioned in the reserved org.
	if super {
		return true
	}
	if strings.HasPrefix(path, "/v1/admin/") {
		return true
	}
	// A site's beacon is not a security event. It records what a visitor did and
	// changes nothing anyone authorizes on; the event plane is its record, a receipt
	// per batch. Auditing it put every public page view behind a synchronous write to
	// the one chain, so a stalled trail refused analytics at the edge (/v1/event and
	// /v1/event/replay answered 503 on 2026-10-30). A denied beacon is still recorded
	// above, and so is one sent with platform authority.
	if telemetry(method, path) {
		return false
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

// telemetryDoors are the event plane's ingest paths (apps/event: the canonical
// endpoint and the replay snapshots).
var telemetryDoors = []string{"/v1/event", "/v1/event/replay"}

// telemetry reports a beacon: a POST to one of telemetryDoors.
func telemetry(method, path string) bool {
	return method == http.MethodPost && slices.Contains(telemetryDoors, strings.TrimSuffix(path, "/"))
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
// In that unvalidated case the org header carries no weight: the boundary writes
// X-Org-Id only for a principal it validated, so any org beside an empty user is
// a claim rather than a fact, and an audit record is exactly the place a claim
// must not become one. So with no validated sub the actor is left
// EMPTY — the record stands as an honest anonymous event identified by SourceIP,
// attributed to nobody rather than to whoever the request named.
//
// With a validated sub, org/sub/email all reflect the verified principal and are
// recorded authoritatively.
// home is the actor's own org, already resolved by crossOrgHome and non-empty
// ONLY for a cross-org action; it is passed in rather than re-derived so the
// coverage decision and the recorded record can never disagree about whether
// this request was an impersonation.
func actorFromCtx(c *zip.Ctx, home string) audit.Actor {
	if !principal.Validated(c) {
		// No validated principal — do not trust the client-asserted org.
		return audit.Actor{}
	}
	return audit.Actor{
		Org:   strings.TrimSpace(c.Org()),
		Sub:   strings.TrimSpace(c.User()),
		Email: strings.TrimSpace(c.UserEmail()),
		Home:  home,
		Act:   trailAct(Operator(c)),
		Agent: agentOf(c),
	}
}

// crossOrgHome returns the actor's HOME org when this request acts on a
// DIFFERENT org — a platform SuperAdmin org-switch — and "" otherwise. The
// non-empty return is therefore both the answer to "is this an impersonation?"
// and the value to record, so there is one fact and one place it comes from.
//
// WHY THE TWO ORGS DIFFER AT ALL. SanitizeIdentity mints X-User-Owner (the home
// org, from the validated membership claim) DISTINCTLY from X-Org-Id (the
// effective org). For every single-org caller the two are equal.
//
// DIVERGENCE ALONE IS NO LONGER IMPERSONATION. It once was: the only principal
// who could act under an org that was not their home org was a SuperAdmin
// switching in, so `home != eff` was exactly the impersonation signal. Since
// membership-based org switching landed, ANY ordinary member of two orgs
// diverges the moment they work in their second one — which is a normal Tuesday,
// not an impersonation. Reading divergence as impersonation therefore did two
// harmful things: it told auditors that routine multi-org work was a platform
// admin acting inside a tenant, and it wrote a Home-bearing row for every
// switched request including plain 200 GETs, burying the real events in volume.
//
// So the predicate is the SPECIFIC fact it always meant to capture: the actor is a
// SuperAdmin (principal.Super — owner == "admin", the one predicate). A non-empty
// Home is, by construction, a platform SuperAdmin acting inside another tenant.
//
// UNFORGEABLE BY CONSTRUCTION. Both values are authorityHeaders: stripped from
// every inbound request and re-minted only from validated claims. So an attacker
// can neither hide an impersonation (by forging home == effective) nor fake one
// (by forging a divergence to pollute the trail). A bearer-less request carries
// neither, so it resolves not-cross-org here, is refused by the org gate, and is
// audited as a 403 by the clause above. No path slips both.
func crossOrgHome(c *zip.Ctx) string {
	if !principal.Validated(c) {
		return ""
	}
	home := principal.Owner(c)
	eff := strings.TrimSpace(c.Org())
	if home == "" || eff == "" || home == eff {
		return ""
	}
	// The divergence must be a SuperAdmin's, not an ordinary multi-org member's.
	if !principal.Super(c) {
		return ""
	}
	return home
}

// authFromCtx records HOW the caller authenticated and the VALIDATED admin bit.
// IsAdmin comes from principal.Super (the sanitized X-User-IsAdmin, true only for a
// verified SuperAdmin), never a raw header. Method is inferred from the
// presence/shape of a credential: an Authorization/X-Authorization bearer or a
// session cookie ⇒ "jwt" (or "api-key" for an opaque sk- token); none ⇒
// "none".
func authFromCtx(c *zip.Ctx) audit.AuthContext {
	return audit.AuthContext{
		Method:  authMethodOf(c),
		IsAdmin: principal.Super(c),
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
		if IsAPIKey(tok) {
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

// EffectiveStatus reconciles the response status with a returned error. If the
// handler returned a *zip.HTTPError (e.g. ErrForbidden), its Status is
// authoritative — the framework renders it after this middleware unwinds, so the
// live response status does not yet reflect it. A non-HTTPError returned error
// with a still-2xx response means the framework will render a 500. Otherwise the
// response status stands.
func EffectiveStatus(respStatus int, err error) int {
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

// RouteFamily reduces a concrete path to its stable route family for the action
// verb, dropping trailing high-cardinality id segments so "DELETE /v1/admin/
// orgs/acme" and "DELETE /v1/admin/orgs/globex" share the action "DELETE
// /v1/admin/orgs". The full concrete path is preserved separately in Record.Path.
func RouteFamily(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	// Keep the leading "v1/<subsystem>/<noun>" and stop before an id-looking tail.
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		// A ":name" segment is the SAME segment an id fills at request time — a
		// route pattern is what an operation carries, and a concrete path is what
		// a request carries, so both have to reduce to one family or the trail
		// spells the same action two ways depending on the door it came through.
		// A concrete path never contains a colon, so this costs REST nothing.
		if looksLikeID(s) || strings.HasPrefix(s, ":") {
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
//   - a known API-key prefix (pk-/sk- — what IsAPIKey recognizes),
//     ALSO after percent-decoding, so sk%2DKEY can't slip the prefix check,
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
// The scrub rule — what a credential looks like in text, and how one is removed —
// lives in sdk/scrub. It is a LEAF so the fleet's MCP server and
// apps/admission can ask the same question without linking package cloud's ~400
// packages, which is what kept admission maintaining a hand-synced copy of the key
// prefixes.
//
// These three keep the names package cloud already used, so no caller moved.

func scrubToken(seg string) string  { return scrub.Token(seg) }
func scrubFreeText(s string) string { return scrub.FreeText(s) }

// ScrubText scrubs credential-shaped words out of free text, leaving every other
// byte alone — the same walk scrubFreeText performs, without its User-Agent length
// cap. Any surface relaying text it did not author (an upstream refusal, a tool's
// log tail) passes it through here first; cloud.ErrorHandler does it for every
// error a handler propagates.
func ScrubText(s string) string { return scrub.Text(s) }

// Route nouns/ids pass through unchanged (they are not IsAPIKey-shaped).
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

// agentOf is who acted for the validated principal through a delegated token,
// from the boundary's own attestation (principal.Mint), never a header.
func agentOf(c *zip.Ctx) string {
	p, _ := principal.Minted(c)
	return p.Agent
}
