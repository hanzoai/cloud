package account

// The console's half of the anti-forgery control: the address that HANDS a browser
// its token, and the three shapes in which this app's own surfaces ask for one.
//
// THE CONTROL ITSELF IS NOT HERE. It is cloud.Intended (intent.go) — one decision,
// asked at op-invoke for every operation the program serves, which is the only seam
// MCP, the call plane, the graph and the CLI all pass through. The token is
// internal/attest. This file holds what belongs to the ACCOUNT surface: a caller
// obtains their first token from their own account, so the issuing route is the
// account's, and the boot verdicts below are this app's because this app is the
// minter.
//
// WHY THE TOKEN WORKS. A browser write is authenticated from its httpOnly session
// COOKIE, which is AMBIENT — a page the caller never visited sends it too. So a
// change gets a POSITIVE control: a value the caller can obtain ONLY by reading a
// same-origin response (the Same-Origin Policy stops a cross-site page from reading
// GET /v1/account/csrf) and MUST echo in a CUSTOM header (a simple cross-site
// request cannot set X-CSRF-Token without a preflight the server never grants).
//
// THE KEY IS SHARED, AND THAT IS A DEPLOYMENT FACT. One address MINTS a token — the
// route below — and the processes that CHECK one are other processes entirely
// (plugin/<name>/main.go links one subsystem; the host runs each as a child). A MAC
// verifies against the key that wrote it, so those processes hold ONE key or no
// token ever verifies. That key is attest.KeyEnv, from KMS, on the pod — a child
// inherits the host's environment whole, so one value reaches every process.
//
// Absent it, a process mints a key of its own. That is right for the MINTER alone,
// which only ever checks tokens it wrote itself, and wrong for everyone else, whose
// every check then fails — a permanent 403 across the console that no test can see
// from inside one process. So the two sides ask different questions of the same key
// at BOOT, and both are answered here:
//
//	[Shared] — the checkers. A key of their own verifies nothing they will be sent,
//	           so they refuse, and the mount fails.
//	[MountAccount] — the minter. A key of its own works for one process over one
//	           lifetime, so it is allowed on a laptop and refused on a deployment
//	           ([cloud.Deployed] — this process was handed a master key, so it has a
//	           secret store and no excuse for a missing one).

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/attest"
	"github.com/zap-proto/zip"
)

// KeyEnv names the shared anti-forgery MAC key. It is attest's name, said here so
// an operator provisioning it and an error telling them say the same word.
const KeyEnv = attest.KeyEnv

// serving reports whether this process will answer requests. `<binary> describe <dir>`
// projects the router into an artifact and exits, so it holds no session, is sent no
// token and has nothing to control — and the artifact is a function of the code alone
// (cloud.SpecConfig), which a key read from the environment would break.
func serving() bool {
	_, projecting := cloud.DescribeRequested()
	return !projecting
}

// own is the MINTER's verdict on a key of its own, which is a different question with
// a different answer: this app issues the tokens it accepts, so one process over one
// lifetime is self-consistent and a laptop with no KMS works unchanged. A DEPLOYMENT
// is not one process over one lifetime — it was handed a master key, so a secret store
// stands behind it, and a key it invented would be one value per replica and a new one
// per restart, refusing every console session that crossed either.
func own(deployed bool) error {
	lone := attest.Shared()
	if lone == nil || !deployed || !serving() {
		return nil
	}
	return fmt.Errorf("anti-forgery: %w; this process holds a master key, so it has a secret store — set %s "+
		"from KMS (32 bytes, hex or base64) on every process that mints or checks a console token", lone, KeyEnv)
}

// CSRF is the control as a PREDICATE, in the shape a typed op holds: a context. It
// is how an operation on a surface cloud.Intent does not yet govern asks the same
// question, and every one of them — apps/billing, apps/company, apps/wallet,
// apps/admin, apps/todo, apps/referral, apps/s3, apps/websearch — asks it here
// rather than carrying a copy.
//
// IT IS THE ROLLOUT'S OTHER HALF AND IT ENDS. Every op that calls this is one
// cloud.Intent will cover the moment its surface joins that list, at which point
// this call is a second ask of a question already answered and comes out. Until
// then the two agree by construction: both are cloud.Intended.
//
// FAIL CLOSED off the HTTP path. There is no request there to judge and no ambient
// credential to abuse, and it refuses anyway, so "this write is controlled" is a
// property of the control rather than of whichever check happens to run after it.
// cloud.Intent takes the other reading for the same fact — an in-process invoke is
// the CLI, which is not forgeable — and the difference is deliberate: the surfaces
// that call this are the ones that MOVE MONEY, where the money rule refuses an
// unattested caller too (toll.go's ErrNoLedger), so refusing here agrees with it.
func CSRF(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return zip.ErrForbidden(Unattested)
	}
	return cloud.Intended(c)
}

// Unattested is what the off-the-HTTP-path refusal SAYS. Exported because a test
// has to tell it apart from the other refusals a write meets: every one of them
// is a 403 an unattested caller would get anyway, so a suite that only asked
// whether something refused would pass with this control removed. One string, so
// the words a test asserts are the words the control speaks.
const Unattested = "a change is made by an attested caller"

// RequireCSRF is the same control as a STANDALONE ROUTE HANDLER, for a RAW route —
// apps/meet's recording write and apps/todo's repository lifecycle routes.
//
// IT IS NOT WHAT CONTROLS A TYPED OP, and unlike [CSRF] it does not end with the
// rollout: a raw route has no registry entry, so cloud.Intent never sees it and
// never will. A typed op is reachable five ways and answers to the rule; a raw
// handler is reachable one way and answers to this.
func RequireCSRF() zip.Handler {
	return func(c *zip.Ctx) error {
		if err := cloud.Intended(c); err != nil {
			return err
		}
		return c.Next()
	}
}

// requireCSRF is the same control as MIDDLEWARE, which is the shape a route chain
// takes: it wraps the leaf handler at registration rather than standing in front of
// it. Both are one line over [cloud.Intended] — one decision, three shapes, never
// three decisions.
func requireCSRF() zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			if err := cloud.Intended(c); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// RequireCSRFOnSpend is [RequireCSRF] narrowed to the requests that COST the
// caller money, for a surface whose READ is the paid unit of work.
//
// The ordinary rule is that a read needs no token: a GET changes nothing, and
// requiring one to list a board would break every server-side reader. That rule
// rests on a read being free, and on these surfaces it is not — the answer is
// synthesized through a model, or embedded, or bought from a vendor, and the
// caller's balance pays for it. So a page the caller never visited can send their
// browser to one of these addresses, the cookie they already hold authenticates
// it, and the debit lands on them. Nothing leaks — the answer is unreadable
// cross-origin — what moves is money.
//
// WHICH REQUESTS SPEND IS NOT RESTATED HERE. It asks [cloud.Consumes], which is
// where the money rule lives and where the paid reads are named beside it, so this
// control and the balance gate can never disagree about what a read is, and a
// surface added to that list is covered without anyone remembering this function.
// cloud.Intent asks the same function, so a paid read is covered on every seam the
// moment its surface joins the governed list, and this comes out with it.
func RequireCSRFOnSpend() zip.Handler {
	gate := RequireCSRF()
	return func(c *zip.Ctx) error {
		if !cloud.Consumes(c.Method(), c.Path()) {
			return c.Next()
		}
		return gate(c)
	}
}

// csrfResp is the anti-forgery token a browser echoes on every change.
type csrfResp struct {
	// Token is the value to send back in the X-CSRF-Token header. It is bound to the
	// caller's identity, so it authorizes changes as them and as nobody else.
	Token string `json:"csrfToken"`
	// ExpiresIn is the token's lifetime in seconds. Fetch a new one when it lapses;
	// a change with an expired token is refused.
	ExpiresIn int64 `json:"expiresIn"`
}

// IssueCSRFToken mints the anti-forgery token a browser echoes as X-CSRF-Token on
// every change it asks for. The token is bound to the caller's validated identity
// and expires, so one minted for one identity cannot authorize a change as another.
//
// It is answered no-store, so it is never cached by a shared proxy. This is the
// same-origin endpoint the embedded console reads — the Same-Origin Policy is what
// stops a cross-site page from reading the response and forging a change.
func (o ops) issueCSRFToken(ctx context.Context, _ *noInput) (*csrfResp, error) {
	// reads: this is the endpoint that ISSUES the token, so requiring one here
	// would leave a browser no way to obtain its first. unscoped: a zero-org
	// (first-run) user still needs one to onboard.
	cr, c, err := o.requestCaller(ctx, reads, unscoped, "obtain a CSRF token")
	if err != nil {
		return nil, err
	}
	token, ttl := attest.Process().Mint(cr.name, cr.owner)
	c.Fiber().Set("Cache-Control", "no-store")
	return &csrfResp{Token: token, ExpiresIn: ttl}, nil
}
