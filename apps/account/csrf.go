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
// one identity cannot authorize a write as another, and it EXPIRES after csrfTTL.
//
// THE KEY IS SHARED, AND THAT IS A DEPLOYMENT FACT. One address MINTS a token —
// GET /v1/account/csrf, served by this app — and the operations that VERIFY one are
// registered in OTHER apps, each of which is its own process (plugin/<name>/main.go
// links one subsystem; the host runs each as a child). A MAC verifies against the key
// that wrote it, so those processes hold ONE key or no token ever verifies. That key
// is CONSOLE_CSRF_KEY, from KMS, on the pod — a child inherits the host's environment
// whole, so one value reaches every process.
//
// Absent, [loadCSRFKey] mints a per-process random key. That is right for the MINTER
// alone, which only ever verifies tokens it wrote itself, and wrong for everyone
// else, whose every verify then fails — a permanent 403 across the money path that no
// test can see from inside one process. So the two sides ask different questions of
// the same key at BOOT, and both are answered here:
//
//	[Shared] — the verifiers. An ephemeral key verifies nothing they will be sent,
//	           so it refuses, and the mount fails.
//	[MountAccount] — the minter. An ephemeral key works for one process over one
//	           lifetime, so it is allowed on a laptop and refused on a deployment
//	           ([cloud.Deployed] — this process was handed a master key, so it has a
//	           secret store and no excuse for a missing one).

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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

// KeyEnv names the shared anti-forgery MAC key. KMS holds the value; the pod carries
// it; a plugin child inherits it. It is the ONE name, so an operator provisioning it
// and an error telling them to say the same word.
const KeyEnv = "CONSOLE_CSRF_KEY"

// shared decodes the key every process is meant to hold, or says why there is none.
// A PURE READ of the environment, so both verdicts below can be asked whenever, and
// asking one cannot fix the answer for the other.
func shared() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(KeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s is unset, so this process holds an anti-forgery key nobody else does", KeyEnv)
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("%s is set but does not decode to 32 bytes of hex or base64", KeyEnv)
}

// The process-wide MAC key, resolved once: the token is minted and verified in one
// process by several registrations, and a key per Mount would leave a token minted by
// one unverifiable by the next.
var (
	csrfKeyOnce sync.Once
	csrfKeyVal  []byte
)

// sharedCSRFKey returns the process-wide keyed-BLAKE3 MAC key — [shared] when there
// is one, else a random key this process alone holds. Which of the two is a boot
// question, answered by [Shared] and [own] before a request ever arrives.
func sharedCSRFKey(log luxlog.Logger) []byte {
	csrfKeyOnce.Do(func() {
		k, lone := shared()
		if lone == nil {
			csrfKeyVal = k
			return
		}
		if log != nil {
			log.Warn("anti-forgery key is this process's alone", "why", lone)
		}
		csrfKeyVal = make([]byte, 32)
		if _, err := rand.Read(csrfKeyVal); err != nil {
			// crypto/rand failure is catastrophic; a zero key would be forgeable.
			panic("console: cannot generate CSRF key: " + err.Error())
		}
	})
	return csrfKeyVal
}

// serving reports whether this process will answer requests. `<binary> describe <dir>`
// projects the router into an artifact and exits, so it holds no session, is sent no
// token and has nothing to control — and the artifact is a function of the code alone
// (cloud.SpecConfig), which a key read from the environment would break.
func serving() bool {
	_, projecting := cloud.DescribeRequested()
	return !projecting
}

// Shared is the VERIFIER's verdict, taken at BOOT. An app whose operations ask [CSRF]
// calls it once from its Mount.
//
// It is the whole difference between a control that works and one that refuses
// everything: this process verifies MACs written by the process that serves
// GET /v1/account/csrf, and a key it invented itself matches none of them. Returning
// the error fails the mount, which in a plugin child is exit 1 and in the host is the
// app absent behind a 503 — a missing key is then loud at boot, in one line, instead
// of a 403 on every console write for as long as the pod runs.
//
// The minter does not call this: its own key verifies its own tokens, so a laptop with
// no KMS still issues and accepts them. See [own].
func Shared() error {
	if _, lone := shared(); lone != nil && serving() {
		return fmt.Errorf("anti-forgery: %w; it verifies the token GET /v1/account/csrf mints in another process, "+
			"so both must hold the same 32-byte key — set %s from KMS", lone, KeyEnv)
	}
	return nil
}

// own is the MINTER's verdict on a key of its own, which is a different question with
// a different answer: this app issues the tokens it accepts, so one process over one
// lifetime is self-consistent and a laptop with no KMS works unchanged. A DEPLOYMENT
// is not one process over one lifetime — it was handed a master key, so a secret store
// stands behind it, and a key it invented would be one value per replica and a new one
// per restart, refusing every console session that crossed either.
func own(deployed bool) error {
	_, lone := shared()
	if lone == nil || !deployed || !serving() {
		return nil
	}
	return fmt.Errorf("anti-forgery: %w; this process holds a master key, so it has a secret store — set %s "+
		"from KMS (32 bytes, hex or base64) on every process that mints or verifies a console token", lone, KeyEnv)
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
