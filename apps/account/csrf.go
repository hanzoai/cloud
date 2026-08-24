package account

// CSRF protection for the console money-WRITE surface (mint/revoke key, topup,
// onboard, and the billing/commerce write verbs).
//
// THREAT. The forward-perfect money path is the embed same-origin session bridge
// (middleware_identity.sessionAccessToken): a browser write is authenticated from its
// httpOnly session COOKIE, which is AMBIENT — a cross-site page's request to our
// origin carries it too. The bridge's Sec-Fetch-Site check is a NEGATIVE heuristic
// that passes VACUOUSLY when Origin/Referer/Sec-Fetch-Site are all absent (RED). So a
// state-changing write gets a POSITIVE control: a token the caller can obtain ONLY by
// reading a same-origin response (the Same-Origin Policy blocks a cross-site page from
// reading GET /v1/account/csrf) and MUST echo in a CUSTOM header (a cross-site simple/
// form request cannot set X-CSRF-Token without a CORS preflight the server never
// grants).
//
// SCOPE — only the AMBIENT path. A Bearer/Basic-authenticated request is immune to
// CSRF (a cross-site attacker cannot set the Authorization header), and the gateway
// path forwards minted identity HEADERS with no browser cookie. requireCSRF therefore
// enforces ONLY when the request carries NO explicit Authorization/X-Authorization AND
// a Cookie is present — i.e. exactly the ambient-cookie/embed browser write. Every
// other caller (API/machine Bearer, gateway-fronted, the header-injected tests) is
// unaffected. Fail-secure: an ambient write with a missing/invalid token is refused.
//
// TOKEN — base64url( ts_be64(8) || mac(16) ), where
//   mac = KeyedBLAKE3(csrfKey, domain \x00 uid \x00 org \x00 ts)[:16]  (luxfi/crypto).
// It is BOUND to the validated principal (X-User-Id + X-Org-Id) so a token minted for
// one identity cannot authorize a write as another, and it EXPIRES after csrfTTL. The
// key is server-only (KMS-sourced env CONSOLE_CSRF_KEY); no key ⇒ a per-process random
// key (tokens then reset on restart — the SPA re-fetches on a 403).

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/crypto/blake3"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	csrfDomain    = "hanzo-console-csrf-v1"
	csrfMACLen    = 16             // 128-bit truncated BLAKE3 MAC — ample for a bound, expiring token
	csrfTTL       = 12 * time.Hour // token lifetime; SPA re-fetches on expiry/403
	csrfClockSkew = 120            // seconds of future tolerance
	csrfTokenLen  = 8 + csrfMACLen // ts || mac
)

// csrfKeyOnce guards the process-wide CSRF MAC key. account@48 issues the token and
// the money WRITES that verify the token are registered elsewhere — co-resident on
// commerce (RequireCSRF below) — so the key must be ONE value for the process, not one
// per Mount. Without that, the ephemeral (no CONSOLE_CSRF_KEY) case gives each
// registration its own random key and no minted token ever verifies. Deterministic from
// CONSOLE_CSRF_KEY (KMS) in prod.
var (
	csrfKeyOnce sync.Once
	csrfKeyVal  []byte
)

// sharedCSRFKey returns the process-wide keyed-BLAKE3 MAC key, loaded ONCE.
func sharedCSRFKey(log luxlog.Logger) []byte {
	csrfKeyOnce.Do(func() { csrfKeyVal = loadCSRFKey(log) })
	return csrfKeyVal
}

// loadCSRFKey returns the 32-byte keyed-BLAKE3 MAC key. Prefers the server-only env
// CONSOLE_CSRF_KEY (KMS-sourced; hex or base64-std, must decode to exactly 32 bytes);
// otherwise a per-process random key with a WARN (single-replica tolerable — tokens
// reset on restart, the SPA transparently re-fetches on a 403).
func loadCSRFKey(log luxlog.Logger) []byte {
	if raw := strings.TrimSpace(os.Getenv("CONSOLE_CSRF_KEY")); raw != "" {
		if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
			return b
		}
		if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
			return b
		}
		if log != nil {
			log.Warn("CONSOLE_CSRF_KEY set but not a 32-byte hex/base64 value; using an ephemeral per-process key")
		}
	} else if log != nil {
		log.Warn("CONSOLE_CSRF_KEY unset; using an ephemeral per-process CSRF key (set it from KMS for multi-replica/restart-stable tokens)")
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		// crypto/rand failure is catastrophic; a zero key would be forgeable, so panic.
		panic("console: cannot generate CSRF key: " + err.Error())
	}
	return k
}

// csrfMAC computes the bound, truncated keyed-BLAKE3 MAC for (uid, org, ts).
func csrfMAC(s *cloud.Service[state], uid, org string, ts int64) []byte {
	var msg []byte
	msg = append(msg, csrfDomain...)
	msg = append(msg, 0)
	msg = append(msg, uid...)
	msg = append(msg, 0)
	msg = append(msg, org...)
	msg = append(msg, 0)
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], uint64(ts))
	msg = append(msg, t[:]...)
	sum, err := blake3.KeyedHash(s.State.csrfKey, msg)
	if err != nil {
		// Only errors on a bad key length; loadCSRFKey guarantees 32 bytes.
		panic("console: CSRF MAC: " + err.Error())
	}
	return sum[:csrfMACLen]
}

// issueCSRF mints a token bound to (uid, org) valid for csrfTTL. Returns the token and
// its lifetime in seconds.
func issueCSRF(s *cloud.Service[state], uid, org string) (string, int64) {
	ts := time.Now().Unix()
	var out [csrfTokenLen]byte
	binary.BigEndian.PutUint64(out[:8], uint64(ts))
	copy(out[8:], csrfMAC(s, uid, org, ts))
	return base64.RawURLEncoding.EncodeToString(out[:]), int64(csrfTTL / time.Second)
}

// verifyCSRF checks a token against the CURRENT request's validated (uid, org) and its
// expiry, in constant time.
func verifyCSRF(s *cloud.Service[state], token, uid, org string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil || len(raw) != csrfTokenLen {
		return false
	}
	ts := int64(binary.BigEndian.Uint64(raw[:8]))
	now := time.Now().Unix()
	if ts > now+csrfClockSkew || now-ts > int64(csrfTTL/time.Second) {
		return false
	}
	return subtle.ConstantTimeCompare(raw[8:], csrfMAC(s, uid, org, ts)) == 1
}

// ambientCookieAuth reports whether the request is authenticated by an AMBIENT
// credential (a browser cookie) rather than an explicit one. Only such requests are
// CSRF-able. Explicit Authorization/X-Authorization (Bearer/Basic) ⇒ not ambient; no
// Cookie at all (gateway header-injection, the tests) ⇒ not ambient.
func ambientCookieAuth(c *zip.Ctx) bool {
	// cloud.Presented is the identity boundary's OWN header reader, asked whole —
	// not its parse re-applied here per header. The boundary's rule is a CROSS-header
	// precedence that reads Basic from Authorization only, so judging the two headers
	// by one per-header rule made `X-Authorization: Basic …` explicit to this gate and
	// invisible to the boundary, which fell through to the cookie and authenticated
	// the write this gate had just excused. Asking the one function makes the two
	// agree by construction rather than by two implementations happening to match.
	if cloud.Presented(c) != "" {
		return false
	}
	return len(c.Fiber().Request().Header.Peek("Cookie")) > 0
}

// requireCSRF gates a state-changing handler, enforcing a valid X-CSRF-Token on the
// ambient-cookie path only (see package note). A validated principal is required for
// the ambient path to mean anything; the gated handler still does its own
// resolveCaller, so this only ADDS the anti-CSRF gate.
//
// It is a zip.Middleware, which reaches a ROUTE. `With` composes it around the
// leaf handler at registration time and around nothing else: a typed op's invoke
// and direct seams are built before the wrap, so MCP, the call plane, the graph
// and the CLI never meet it. The typed writes therefore ask checkCSRF in their own
// preamble (account.go's requestCaller); this stays for the raw handlers the
// untyped routes use — one decision, two ways to ask it.
func requireCSRF(s *cloud.Service[state]) zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			if err := checkCSRF(s, c); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// checkCSRF is THE decision, once: nil when this request may change something, a
// 403 when it may not. The middleware above and the exported predicate below are
// two ways to ASK it, never two copies of it.
func checkCSRF(s *cloud.Service[state], c *zip.Ctx) error {
	if !ambientCookieAuth(c) {
		return nil // Bearer/Basic/gateway/API — not CSRF-able
	}
	tok := strings.TrimSpace(c.Header("X-CSRF-Token"))
	if tok == "" {
		return zip.ErrForbidden("missing CSRF token (GET /v1/account/csrf and echo it in X-CSRF-Token)")
	}
	if !verifyCSRF(s, tok, strings.TrimSpace(c.User()), strings.TrimSpace(c.Org())) {
		return zip.ErrForbidden("invalid or expired CSRF token")
	}
	return nil
}

// CSRF is the anti-CSRF control as a PREDICATE, in the shape a typed op holds: a
// context. It is THE way an operation anywhere in this estate asks it, and the
// only exported one — apps/billing, apps/todo and apps/referral all call this and
// none of them carries a copy.
//
// IN THE OPERATION, because a route is one of the seams that reach it and not the
// only one. zip records the route's handler and the op as two fields of one entry
// and wraps only the handler, while MCP, the call plane, the graph and the CLI
// call the op directly — so no Use, Group or With reaches a tools/call, while the
// depth-0 identity middleware still authenticates whoever is calling. The
// preamble is the one place every seam passes through.
//
// zip's own op-level rule, App.Authorize, answers a different question: it decides
// on the decoded INPUT and this decides on the REQUEST's credentials, which are
// not part of any op's In and must never be — a caller that could declare itself
// exempt in a body field would.
//
// A caller who presented a credential (Bearer, Basic, gateway, API key) passes
// untouched: they cannot be CSRF'd, so this costs an API client nothing. Only the
// ambient-cookie path is asked for the echoed token.
//
// FAIL CLOSED off the HTTP path. There is no request there to judge and no
// ambient credential to abuse, and it refuses anyway, so "this write is
// controlled" is a property of the control rather than of whichever check happens
// to run after it.
func CSRF(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return zip.ErrForbidden(Unattested)
	}
	return checkCSRF(&cloud.Service[state]{State: state{csrfKey: sharedCSRFKey(nil)}}, c)
}

// Unattested is what the off-the-HTTP-path refusal SAYS. Exported because a test
// has to tell it apart from the other refusals a write meets: every one of them
// is a 403 an unattested caller would get anyway, so a suite that only asked
// whether something refused would pass with this control removed. One string, so
// the words a test asserts are the words the control speaks.
const Unattested = "a change is made by an attested caller"

// RequireCSRF is the same control as a STANDALONE ROUTE HANDLER, for a raw route
// registered outside this package that has no op preamble to put it in —
// apps/meet's recording write and apps/todo's repository lifecycle routes.
//
// IT IS NOT WHAT CONTROLS A TYPED OP. A route is one of the seams that reach an
// operation and not the only one, so an op asks [CSRF] itself; this covers the
// handlers that have no other way to ask. One decision, two shapes, never two
// decisions.
//
// It binds to the SAME process-wide key (sharedCSRFKey) the issuer uses, so a
// token minted at GET /v1/account/csrf verifies here byte-identically. It applies
// ONLY on the ambient-cookie path — a Bearer/gateway/API caller cannot be CSRF'd —
// and on success continues into the rest of the chain. The minimal Service carries
// only the shared key; requireCSRF and verifyCSRF read nothing else off it.
func RequireCSRF() zip.Handler {
	s := &cloud.Service[state]{State: state{csrfKey: sharedCSRFKey(nil)}}
	return requireCSRF(s)(func(c *zip.Ctx) error { return c.Next() })
}

// csrfResp is the anti-CSRF token a browser echoes on every money write.
type csrfResp struct {
	// Token is the value to send back in the X-CSRF-Token header. It is bound to the
	// caller's identity, so it authorizes writes as them and as nobody else.
	Token string `json:"csrfToken"`
	// ExpiresIn is the token's lifetime in seconds. Fetch a new one when it lapses;
	// a write with an expired token is refused.
	ExpiresIn int64 `json:"expiresIn"`
}

// IssueCSRFToken mints the anti-CSRF token a browser echoes as X-CSRF-Token on
// every money write (mint/revoke a key, top up, onboard, and the billing/commerce
// write verbs). The token is bound to the caller's validated identity and expires,
// so one minted for one identity cannot authorize a write as another.
//
// It is answered no-store, so it is never cached by a shared proxy. This is the
// same-origin endpoint the embedded console reads — the Same-Origin Policy is what
// stops a cross-site page from reading the response and forging a write.
func (o ops) issueCSRFToken(ctx context.Context, _ *noInput) (*csrfResp, error) {
	// reads: this is the endpoint that ISSUES the token, so requiring one here
	// would leave a browser no way to obtain its first. unscoped: a zero-org
	// (first-run) user still needs one to onboard.
	cr, c, err := o.requestCaller(ctx, reads, unscoped, "obtain a CSRF token")
	if err != nil {
		return nil, err
	}
	token, ttl := issueCSRF(o.s, cr.name, cr.owner)
	c.Fiber().Set("Cache-Control", "no-store")
	return &csrfResp{Token: token, ExpiresIn: ttl}, nil
}
