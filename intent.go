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
	"net/url"
	"strings"

	"github.com/hanzoai/cloud/apps/metering"
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
	// THE BROWSER ALREADY ANSWERS THIS, and its answer cannot be forged from a page.
	//
	// Sec-Fetch-Site is set by the browser and is a forbidden header, so script
	// cannot write it. It distinguishes exactly the case that matters here and that
	// SameSite cannot: a sibling subdomain sends `same-site`, not `same-origin`, so
	// a page on any other *.hanzo.ai host is refused while the console's own request
	// passes. `none` is a user-initiated navigation — typed, bookmarked — which is by
	// definition asked for.
	//
	// This replaces a 32-byte key that every process had to hold the same copy of.
	// That key bought nothing this does not: it proved the caller had first read
	// GET /v1/account/csrf from THIS origin, which is the same fact Sec-Fetch-Site
	// states directly. What it cost was an agreement problem — one value, shared by
	// every child of the fleet, absent or blank in any of them. Nine surfaces
	// answered 503 to every caller for exactly that reason, and no probe could see
	// it, because a key is a deployment fact and a header is a request fact.
	switch c.Header("Sec-Fetch-Site") {
	case "same-origin", "none":
		return nil
	case "":
		// Pre-2023 Safari and other exotic clients. Origin is still sent on every
		// POST by every browser that has ever implemented fetch, so an exact-host
		// match stands in. Absent BOTH, refuse: a cookie-bearing state change that
		// will not say where it came from is the shape being defended against.
		if o := strings.TrimSpace(c.Header("Origin")); o != "" {
			if u, err := url.Parse(o); err == nil && strings.EqualFold(u.Host, c.Fiber().Hostname()) {
				return nil
			}
			return zip.ErrForbidden(Unasked + " (CSRF): this change came from " + o)
		}
		return zip.ErrForbidden(Unasked + " (CSRF): a cookie-authenticated change that states no origin")
	default:
		// same-site (a sibling subdomain) and cross-site.
		return zip.ErrForbidden(Unasked + " (CSRF): the browser says this came from another site")
	}
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

// NO COMPOSE-TIME KEY CHECK. keyed() stood here and refused to compose a deployed
// program that served a governed change without the shared anti-forgery key.
//
// It went when the key did. [Intended] reads Sec-Fetch-Site now — a fact the
// browser states on the request — so there is no value every process must hold the
// same copy of, and therefore nothing to verify at composition. What that check
// really enforced was an agreement between processes, and the way to stop needing
// agreement is to stop having a shared secret, not to check for one earlier.
