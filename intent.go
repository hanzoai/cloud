package cloud

// intent.go — whether a change was ASKED FOR by the caller, or merely carried by
// their browser.
//
// THE HOLE THIS CLOSES. A browser authenticates this API from a session COOKIE
// (middleware_identity's cookieTokenNames), and a cookie is AMBIENT: it rides any
// request the browser is made to send, including one composed by a page the caller
// never chose to visit. SameSite=Lax withholds it from a cross-SITE POST, and that
// is worth something, but SameSite is scoped to the registrable domain — so a page
// on any *.hanzo.ai host sends it, and a request that arrives with it looks exactly
// like the console's own.
//
// The estate already answers this, and answers it in the wrong PLACE. account.CSRF
// is asked inside 46 operations, one preamble at a time, and account.RequireCSRF is
// installed on four groups. Both are correct and neither is reachable by an author
// who does not know to write it: the fleet registers 566 typed operations that
// change something, so the control covers under a tenth of what it is for, and the
// gap grows by whatever anybody adds next.
//
// SO IT IS ASKED ONCE, ABOUT THE OPERATION. zip hands every projection of a typed
// handler — the REST route, tools/call at /mcp, the by-name call plane, the graph,
// the CLI — through ONE dispatcher, and offers ONE decision inside it:
// App.Authorize. That is where the money question already lives (toll.go), and it
// is where this one belongs for the same reason: a route middleware reads the path
// the TRANSPORT carried, and over MCP that path is /mcp for every operation there
// is. account.RequireCSRF says so itself — it "reaches a ROUTE", so MCP, the call
// plane, the graph and the CLI never meet it. This does, because it is not a route.
//
// WHAT IT ASKS, AND WHAT IT DOES NOT. Only the ambient path. A caller who PRESENTED
// a credential (Bearer, Basic, an API key, the gateway's minted headers) cannot be
// forged into — a page cannot set those headers on a request to us — so they are
// asked for nothing and pay nothing. Only a CHANGE: it reads [Consumes], the same
// rule the money gate reads, so a read stays free of it and the paid reads that
// spend on GET are covered without this file naming them. And only where the
// request EXISTS: an in-process invoke (the CLI) has no browser and no cookie, so
// there is nothing to forge and it passes. Refusing there would be refusing the
// terminal.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/internal/attest"
	"github.com/zap-proto/zip"
)

// Rule is the decision EVERY operation this program serves answers to, wherever it
// was reached from. The composition root installs it (App) and nothing else may:
// one program, one rule, so a surface cannot acquire an exemption by being mounted
// somewhere else.
//
// INTENT BEFORE TOLL, and the order is load-bearing rather than alphabetical. Toll
// debits at ADMISSION — it takes the money before the handler runs — so a forged
// change judged by Toll first would have already emptied the victim's balance by
// the time anybody asked whether they meant to spend it. The cheap question that
// can refuse comes first.
func Rule(m *metering.Client, commerce CommerceClient) zip.Authorizer {
	intent, toll := Intent(), Toll(m, commerce)
	return func(ctx context.Context, op zip.Op, in any) error {
		if err := intent(ctx, op, in); err != nil {
			return err
		}
		return toll(ctx, op, in)
	}
}

// Intent returns the op-invoke authorizer that refuses a change nobody asked for.
func Intent() zip.Authorizer {
	return func(ctx context.Context, op zip.Op, _ any) error {
		if !governs(op.Path) || !Consumes(op.Method, op.Path) {
			return nil
		}
		c, live := Request(ctx)
		if !live {
			// No request, so no browser, no cookie and nothing a page could have
			// sent. An in-process invoke is the CLI, and the CLI is not forgeable.
			return nil
		}
		return Intended(c)
	}
}

// Intended is THE decision, once: nil when this request may change something, a 403
// when it may not. [Intent] and apps/account's preamble and route handler are three
// ways to ASK it, never three copies of it.
//
// It reads the request's CREDENTIALS, which is why it takes a request rather than
// an operation's input: a caller that could declare itself exempt in a body field
// would.
func Intended(c *zip.Ctx) error {
	// Presented is the identity boundary's OWN header reader, asked whole — not its
	// parse re-applied here per header. The boundary's rule is a CROSS-header
	// precedence, so judging the two headers by one per-header rule would make
	// `X-Authorization: Basic …` explicit to this control and invisible to the
	// boundary, which falls through to the cookie and authenticates the change this
	// control had just excused.
	if Presented(c) != "" {
		return nil // Bearer/Basic/API key — not forgeable
	}
	if len(c.Fiber().Request().Header.Peek("Cookie")) == 0 {
		return nil // gateway header-injection, an in-cluster hop — no ambient credential
	}
	tok := strings.TrimSpace(c.Header(attest.Header))
	if tok == "" {
		return zip.ErrForbidden(Unasked + ": GET /v1/account/csrf and echo it in " + attest.Header)
	}
	if !attest.Process().Valid(tok, strings.TrimSpace(c.User()), strings.TrimSpace(c.Org())) {
		return zip.ErrForbidden(Unasked + ", and that token does not say so")
	}
	return nil
}

// Unasked is the stem of every refusal this control speaks, and it is exported
// because a test has to tell it apart from the other 403s a change meets — an
// unattested caller is refused by half a dozen other rules, so a suite that only
// asked whether something refused would pass with this control deleted. One string,
// so the words a test asserts are the words the caller reads.
const Unasked = "a change is made on purpose"

// governed are the surfaces whose changes answer to [Intent]. Each entry is a ROOT
// and covers everything beneath it, compared through scope.go's `under` so the
// match is the one the ROUTER makes rather than a raw prefix test.
//
// IT IS A ROLLOUT DIAL AND IT ONLY EVER GROWS. The control refuses every change a
// same-origin client sends without the token, so a surface joins this list only
// once the clients that write to it stamp one — which for the console is a single
// predicate (its authedFetch already re-mints and retries on a 403), and for the
// raw fetch call sites beside it is a per-site change. Widening ahead of that
// breaks writes rather than protecting them, so the widening is the increment.
//
// When it names every surface the entry is "/" and this list, this variable and
// governs below are deleted: a dial with one position is not a dial.
var governed = []string{
	"/v1/crm/",
}

func governs(path string) bool {
	for _, root := range governed {
		if under(path, root) {
			return true
		}
	}
	return false
}

// keyed refuses to compose a DEPLOYED program that serves a governed change
// without the key every process shares. MountAll asks it once, after the ops exist
// and before anything listens.
//
// A CONTROL WITHOUT THE KEY IS A CONTROL THAT REFUSES EVERYTHING. The token is a
// MAC, so a process checks it against the key that WROTE it, and the writer is the
// process serving GET /v1/account/csrf — another process entirely. A process that
// mints a key of its own therefore refuses every change a browser makes, for as
// long as the pod runs, with nothing in any log saying why. That failure is
// invisible from inside one process, so it cannot be found by a test; it can only
// be refused at boot, which is what this does.
//
// DEPLOYED ONLY. A laptop runs the whole fleet in ONE process, so the key it
// invents is the key it checks against and everything works — asking an engineer
// for a KMS value to run a CRM would be a control that costs more than it defends.
// A deployment is not one process: it was handed a master key, so a secret store
// stands behind it, and a key it invented would be one value per replica and a new
// one per restart.
//
// It takes the fact rather than reading it, so the refusal can be measured: a test
// binary is not a deployment, and a rule that asked Deployed itself would be
// untestable in the one direction that matters.
func keyed(app *zip.App, deployed bool) error {
	if !deployed {
		return nil
	}
	if _, projecting := DescribeRequested(); projecting {
		return nil // no session, no token, nothing to check
	}
	lone := attest.Shared()
	if lone == nil {
		return nil
	}
	for _, op := range app.Registry() {
		if governs(op.Path) && Consumes(op.Method, op.Path) {
			return fmt.Errorf("anti-forgery: %w; this process serves %s %s, whose changes answer to the "+
				"control, and it checks a token another process mints — set %s from KMS (32 bytes, hex or "+
				"base64) on every process, or take that surface out of the governed list",
				lone, op.Method, op.Path, attest.KeyEnv)
		}
	}
	return nil
}
