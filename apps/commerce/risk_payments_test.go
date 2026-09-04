package commerce

// risk_payments_test.go — THE SECOND ENDPOINT ONTO THE MINT, and the one property that
// decides whether screening the first one was a control at all.
//
// commerce holds ONE card money move (billing.TakePayment) and this binary opens TWO
// addresses onto it: the browser's POST /v1/billing/topup/token and the agent's typed
// POST /v1/commerce/payments. Both end in the same authorized deposit. So a screen on the first
// address was never a bound on the mint — it was a bound on one entrance, with a second
// entrance beside it that runs the same core, and the second one is the one published as
// an MCP tool for an agent to call.
//
// Four properties, and each one is a way the bypass could come back:
//
//	THE TYPED ENDPOINT IS SCREENED, against its REAL registration — not a stand-in route
//	that only this file knows how to build. Remove the screen from exposePayments and
//	the request walks through to the handler.
//
//	SHADOW IS PRESERVED THERE TOO. A control that is not deployed must not be able to
//	take a second product surface down, and one that is present and mute must still
//	refuse: the same two facts, at the new address.
//
//	A BURST SPLIT ACROSS BOTH ENDPOINTS IS ONE ACCRUAL. This is the whole point of doing
//	it with the same gate. If the two endpoints resolved their payer differently, or keyed
//	their settlements differently, then splitting a burst across the two addresses
//	would halve every velocity bound — a bypass with the screen still switched on.
//
//	ONE PAYMENT THROUGH TWO ENDPOINTS IS ONE OBSERVATION. The mirror image: keys that
//	converge for the same money, so nothing is double-counted and no customer is
//	frozen for paying once.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// creditEndpoints is every address in this package that reaches commerce's card money
// move, and therefore every address that MUST carry the screen. It is a closed list
// because the structural check below reads it: an endpoint added to the binary and not
// to this list is the bug this whole file is about, so the list is also the place the
// next one gets caught.
//
// THEY ARE ALL PLANE ADDRESSES, and that is the fold rather than a weakening.
// /v1/billing is billing's address now, so the browser's top-up, the saved-card
// top-up and the card subscription are reached BY NAME from the money endpoint and
// answer here — which is the point at which they touch the card. A screen on the
// endpoint and not on the op would be a bound on one entrance with the op still
// callable beside it, which is the exact state this file was written about.
//
// There were FOUR. /v1/commerce/payments was a second public address onto the same
// card money move, opened when the billing route was raw and so reached no agent;
// once that route became a typed op the second address was redundant rather than
// necessary, and it was retired. The screen did not move — it never rode that
// endpoint's router — and this list shrank with the surface rather than around it.
var creditEndpoints = []string{
	"/billing/topup/card",
	"/billing/topup",
	"/billing/subscribe",
}

// screenType is the screen's own type name, which is what makes an identifier in the
// source recognisable AS the screen. Held to the real one by [screenIsAValue].
const screenType = "screen"

// TestCreditEndpoints_EveryEndpointOntoTheMintIsComposedWithTheScreen — the STRUCTURAL half,
// and the one that guards the endpoints this file's behavioural tests cannot reach.
//
// The behavioural tests drive registrations: exposePayments is called directly, so the
// typed endpoint's wiring is real. Mount's own registration of the browser endpoint is
// not — nothing in the package calls Mount, by convention (methods_route_test.go says
// so in as many words), so a future edit that drops `screen` from that one Post would
// leave every test in this package green while the original bypass reopened on the
// other side.
//
// So the rule is checked by READING THE SOURCE rather than by remembering it, exactly as
// apps/risk checks that an observation has one constructor.
//
// # It asserts the screen VALUE reaches the HANDLER, and that is the whole upgrade
//
// The check this replaces asserted that one of two SPELLINGS — a `.With(…)` or a
// `screenChain(…)` — appeared somewhere in the registration. Both were satisfied by
// `zip.Post(app.With(screen), "/payments", o.take)`, and that registration was the
// bug: router middleware wraps the fiber handler the REST route is served through, and
// a typed op is dispatched to its HANDLER by four projections. `takePayment` is in
// tools/list, so an agent's tools/call ran commerce's card money move with the screen
// standing beside it. A check that reads route names and middleware spellings cannot
// see that; one that reads WHERE THE VALUE WENT can.
//
// So two properties, and each is a way the bypass comes back:
//
//	THE HANDLER CARRIES THE SCREEN. The expression a registration hands zip as its
//	handler — the last argument of a raw route, args[2] of a typed op — must be built
//	from an identifier that holds the screen. That is the one composition point every
//	projection of an op runs through.
//
//	NOTHING COMPOSES IT AS ROUTER MIDDLEWARE. A `.With(screen)` anywhere is the old
//	bug by construction: it screens REST and publishes an unscreened tool. It is
//	checked over EVERY registration rather than only the credit endpoints, because the
//	next mint op is not on the list yet.
//
// WHAT HOLDS THE SCREEN is not a name this test knows: it is every identifier the
// package binds to [riskGate]'s answer, plus every parameter DECLARED to be one
// ([screenValues]). An endpoint reached through a differently-named local, or through a
// second function that takes the screen, is therefore read correctly — and an endpoint
// that invents its own screen is not, which is the point.
//
// AND IT COMPOSES THE ADDRESS, because zip does: a route's path is its router's prefix
// joined with its leaf, so an endpoint declared on a group is spelled by no single literal
// in the source. Matching literals alone would find no registration at all — which is
// why the found-set is asserted below. Reading only what the framework reads is the
// difference between a check and a habit.
//
// Mutation proof: register the typed op as `zip.Post(app, "/v1/commerce/payments", o.charge, …)`
// — the money core with no wrap — and this names the file, the line and the endpoint.
// Restore the old `zip.Post(app.With(screen).Group("/v1"), "/payments", o.charge)` and
// it fails twice: once for the unscreened handler, once for the router composition.
func TestCreditEndpoints_EveryEndpointOntoTheMintIsComposedWithTheScreen(t *testing.T) {
	fset := token.NewFileSet()
	pkgs := shipped(t, fset)

	// held records which endpoints were actually FOUND, so the check cannot pass by
	// matching nothing — a renamed or recomposed path with the assertion still green is
	// the same blindness in a new costume, and it is the failure this check already had
	// once.
	held := map[string]bool{}
	for _, pkg := range pkgs {
		values := screenValues(pkg.Files)
		if len(values) == 0 {
			t.Fatal("this check found no identifier holding the screen, so it is reading nothing " +
				"and would stay green however the credit endpoints are wired — the screen's type or " +
				"its resolver was renamed and screenType/riskGate must move with it")
		}
		groups := groupRouters(pkg.Files)
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := verbOf(call)
				if !ok {
					return true
				}
				// THE SCREEN IS NEVER ROUTER MIDDLEWARE. Checked over every registration in
				// the package, not only the credit endpoints: this spelling guards one
				// projection of four, so an op wired that way is unscreened on the plane an
				// agent calls whether or not its address is on the list yet.
				if sel.Sel.Name == "With" {
					for _, arg := range call.Args {
						if carries(arg, values) {
							t.Errorf("%s composes the screen as ROUTER middleware — that wraps only the "+
								"fiber handler the REST route is served through, so a typed op registered "+
								"this way publishes an MCP tool and a by-name op that reach the money core "+
								"unscreened. Wrap the HANDLER instead (screen.route / screen.op).",
								fset.Position(call.Pos()))
						}
					}
					return true
				}
				if sel.Sel.Name != "Post" {
					return true
				}
				endpoint := endpointOf(call, sel, groups)
				if endpoint == "" {
					return true
				}
				held[endpoint] = true
				h := handlerOf(call, sel)
				if h != nil && carries(h, values) {
					return true
				}
				t.Errorf("%s registers the credit endpoint %s with a handler the screen VALUE never "+
					"reached — this address ends in commerce's card money move, and a screen that "+
					"is not inside the handler is absent from every projection of it except the "+
					"one HTTP route", fset.Position(call.Pos()), endpoint)
				return true
			})
		}
	}

	for _, endpoint := range creditEndpoints {
		if !held[endpoint] {
			t.Errorf("no registration of the credit endpoint %s was found — either it moved (and this "+
				"list must move with it) or the check is reading nothing and would stay green "+
				"however the endpoint is wired", endpoint)
		}
	}
}

// shipped is the package as it will be COMPILED — every non-test file, parsed. The
// structural checks read it through one function because they must read the SAME source:
// a check that parsed a subset would be green over exactly the file the next mint is
// added to. It is the whole package rather than mount.go for that reason.
func shipped(t *testing.T, fset *token.FileSet) map[string]*ast.Package {
	t.Helper()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}
	return pkgs
}

// mints is EVERY function in this package that DEPOSITS into the spendable ledger, and
// the reason each one may. It is a closed list because the check below reads it.
//
// The list exists because the pre-charge refusal is a property of the two endpoints the
// screen is composed onto, and NOT of the ledger. A mint added beside these three is a
// mint with no screen in front of it: [screen.settle]'s own [payment.diverged] guard
// backstops the address, but nothing stops a new caller charging a card first and
// discovering afterwards that the credit has nowhere to go. That is the defect this
// batch fixed, and a new entry here is where the next one gets caught.
var mints = map[string]string{
	"settle": "the CARD settlement — the only mint behind a charge, screened at both endpoints, " +
		"and refused before the card moves when the charge and the credit name two orgs",
	"planeCredit": "the internal plane's credit op — the org is the CALLER's and can never be an " +
		"argument, the ref is required, and no card is charged, so there is nothing to take first",
	"Credit": "commerce's creditledger adapter — the admin grant, the endpoint a platform operator " +
		"funds another organisation through and the one the pre-charge refusal names",
}

// TestCreditEndpoints_EveryMintIntoTheSpendableLedgerIsAccountedFor — the same structural
// argument as the endpoint check, one level down.
//
// The endpoint check asks whether every ADDRESS onto the card core is screened. This asks
// what can put money in the ledger AT ALL, because the two questions have different
// answers: a mint reached from a webhook, a job or a second settlement path is not an
// endpoint and would not appear on [creditEndpoints] at all.
//
// It is a LIST WITH REASONS rather than a count, so the failure tells the next author
// what the check is for: either the new mint charges nothing first (say why, add it) or
// it charges a card and needs the screen's own refusal ahead of it.
//
// Mutation proof: delete any entry from [mints] and the check names the function and the
// line; add a fourth `fin.Deposit` anywhere in the package and it fails until the reason
// is written down.
func TestCreditEndpoints_EveryMintIntoTheSpendableLedgerIsAccountedFor(t *testing.T) {
	fset := token.NewFileSet()
	found := map[string]string{}
	for _, pkg := range shipped(t, fset) {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Deposit" {
						return true
					}
					if _, seen := found[fn.Name.Name]; !seen {
						found[fn.Name.Name] = fset.Position(call.Pos()).String()
					}
					return true
				})
			}
		}
	}

	for name, where := range found {
		if _, ok := mints[name]; !ok {
			t.Errorf("%s: %s deposits into the spendable ledger and is not accounted for in [mints] — "+
				"if it stands behind a card charge it needs the screen's refusal ahead of it "+
				"(screen.decide), because the ledger boundary can only refuse AFTER the money moved",
				where, name)
		}
	}
	for name := range mints {
		if _, ok := found[name]; !ok {
			t.Errorf("[mints] names %s and nothing in the package deposits there — either it moved "+
				"(and this list must move with it) or the check is reading nothing and would stay "+
				"green however the ledger is minted", name)
		}
	}
}

// screenValues is every identifier in the package that HOLDS the screen — the answer
// to "what does the screen look like in this source", asked of the source instead of
// remembered.
//
// Two ways an identifier comes to hold one, and they are the only two the package has:
// it was bound to [riskGate]'s answer (the composition root's `screen := riskGate(lg)`),
// or it was DECLARED to be one as a parameter (`func exposePayments(app *zip.App, s
// screen)`). Following the parameter is what lets an endpoint be registered in a
// different function from the one that resolved the screen, which is exactly how the
// typed endpoint is wired — and it is why this reads a value's travel rather than a
// fixed name.
func screenValues(files map[string]*ast.File) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				if len(v.Lhs) != 1 || len(v.Rhs) != 1 || !isRiskGate(v.Rhs[0]) {
					return true
				}
				if id, ok := v.Lhs[0].(*ast.Ident); ok {
					out[id.Name] = true
				}
			case *ast.FuncDecl:
				if v.Type.Params == nil {
					return true
				}
				for _, p := range v.Type.Params.List {
					id, ok := p.Type.(*ast.Ident)
					if !ok || id.Name != screenType {
						continue
					}
					for _, name := range p.Names {
						out[name.Name] = true
					}
				}
			}
			return true
		})
	}
	return out
}

// isRiskGate reports whether an expression is the call that resolves the screen.
func isRiskGate(e ast.Expr) bool {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "riskGate"
}

// carries reports whether an expression is BUILT FROM one of the screen's identifiers
// — as a receiver (`screen.route(h)`), as an argument (`o.take(screen)`), or nested in
// either. It is the whole of "the value reached here".
func carries(n ast.Node, values map[string]bool) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && values[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// handlerOf is the expression a registration hands zip as its HANDLER — the one value
// every projection of the route dispatches to.
//
// The two registrars put it in different places and both are read, because a check
// that knew only one of them would be blind to exactly the other endpoint: a typed op is
// zip.Post(target, path, handler, opts…) so the handler is args[2] and the options
// follow it, while a raw route is <router>.Post(path, handlers…) whose handler is the
// LAST of a chain.
func handlerOf(call *ast.CallExpr, sel *ast.SelectorExpr) ast.Expr {
	if id, ok := sel.X.(*ast.Ident); ok && id.Name == "zip" {
		if len(call.Args) < 3 {
			return nil
		}
		return call.Args[2]
	}
	if len(call.Args) < 2 {
		return nil
	}
	return call.Args[len(call.Args)-1]
}

// endpointOf returns the credit endpoint a registration addresses, or "" for a route that is
// not one. The address is the ROUTER'S PREFIX joined with the leaf, which is how zip
// composes it; a path assembled from anything but literals is one this check cannot vouch
// for, and it reports nothing rather than pretending otherwise (the found-set assertion is
// what turns such a gap into a failure instead of a silence).
func endpointOf(call *ast.CallExpr, sel *ast.SelectorExpr, groups map[string]router) string {
	leaf := leafOf(call.Args)
	if leaf == "" {
		return ""
	}
	prefix := ""
	for _, r := range routersOf(call, sel) {
		if p := resolve(r, groups).prefix; p != "" {
			prefix = p
			break
		}
	}
	got := prefix + leaf
	for _, endpoint := range creditEndpoints {
		if got == endpoint {
			return endpoint
		}
	}
	return ""
}

// verbOf reaches a registration's verb through the generic spelling as well as
// the plain one.
//
// zip.Post[In, Out](router, …) parses as an IndexListExpr wrapping the selector
// — an IndexExpr when only one type is written — and reading only the plain
// SelectorExpr made this check BLIND to exactly the form the typed surface is
// registered in. A guard that cannot see the registrations it audits is worse
// than none, because it reads as green; that is the same failure the regexp
// version of the sibling guard shipped with, one spelling over.
func verbOf(call *ast.CallExpr) (*ast.SelectorExpr, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn, true
	case *ast.IndexExpr:
		sel, ok := fn.X.(*ast.SelectorExpr)
		return sel, ok
	case *ast.IndexListExpr:
		sel, ok := fn.X.(*ast.SelectorExpr)
		return sel, ok
	}
	return nil, false
}

// router is a registration target as this check can read it: where it sits. Whether
// the screen is on it is no longer a property worth recording — a screen on a ROUTER is
// the bug, and it is reported where it is found rather than credited here.
type router struct{ prefix string }

// routersOf is where a registration's target can be. zip.Post takes it as the first
// ARGUMENT; a method call carries it as the RECEIVER. Both are consulted, so neither
// registrar is the one nobody reads.
func routersOf(call *ast.CallExpr, sel *ast.SelectorExpr) []ast.Expr {
	return []ast.Expr{sel.X, firstNonLiteral(call.Args)}
}

// resolve reads a router expression: a named group as this package recorded it, or an
// inline `<router>.Group("…")`.
func resolve(r ast.Expr, groups map[string]router) router {
	if r == nil {
		return router{}
	}
	if id, ok := r.(*ast.Ident); ok {
		return groups[id.Name]
	}
	out, _ := groupCall(r)
	return out
}

// leafOf is the path a registration names — the first string literal among its
// arguments, which is args[0] for a method call and args[1] for zip.Post.
func leafOf(args []ast.Expr) string {
	for _, a := range args {
		if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return strings.Trim(lit.Value, `"`)
		}
	}
	return ""
}

// firstNonLiteral is the router among a call's arguments: the first one that is not the
// path. nil when there is none (a method call, whose router is its receiver).
func firstNonLiteral(args []ast.Expr) ast.Expr {
	for _, a := range args {
		if lit, ok := a.(*ast.BasicLit); !ok || lit.Kind != token.STRING {
			return a
		}
	}
	return nil
}

// groupRouters records every group ASSIGNED a name in this package, and its prefix. A
// named group is the form zipdoc also requires of a router a typed op is declared on,
// so it is the form this package uses and therefore the form this check has to follow —
// and the reason an endpoint's ADDRESS is composed rather than matched as a literal.
func groupRouters(files map[string]*ast.File) map[string]router {
	out := map[string]router{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			id, ok := as.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if r, ok := groupCall(as.Rhs[0]); ok {
				out[id.Name] = r
			}
			return true
		})
	}
	return out
}

// groupCall reads `<router>.Group("<literal>")` into its prefix.
func groupCall(e ast.Expr) (router, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return router{}, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Group" {
		return router{}, false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return router{}, false
	}
	return router{prefix: strings.Trim(lit.Value, `"`)}, true
}

// topupEndpoint is the ONE endpoint onto the mint. A second address onto the same money
// move used to sit at /v1/commerce/payments; it was retired, and the tests that
// drove it went with it. What did NOT go is the structural guard above: it walks the
// package and refuses any NEW endpoint onto the mint that is not composed with the
// screen, which is the check that made one endpoint the whole story rather than the
// one somebody happened to remember.
const topupEndpoint = "/v1/billing/topup/token"

// isolate gives one test its own risk plane: a socket directory with no listener in it
// (so "not deployed" is the state of the world unless the test installs a scorer) and a
// scorer client that is put back afterwards.
//
// It is [shortRuntimeDir] and not t.TempDir, and the difference is load-bearing HERE
// more than anywhere: a unix socket address is capped near a hundred bytes, t.TempDir
// embeds the calling test's name, and the names in this file run to sixty-four
// characters. Over the cap every dial answers `connect: invalid argument` — which is
// indistinguishable from the undeployed peer these tests assert about, so the suite
// would go red on any Mac for a reason that looks exactly like its own subject. That
// already happened once to the two tests beside these; this is the same fix, reached for
// rather than rediscovered.
func isolate(t *testing.T) {
	t.Helper()
	shortRuntimeDir(t)
	client.Unbind()
	t.Cleanup(client.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
}

func payApp(t *testing.T) *zip.App {
	t.Helper()
	isolate(t)
	funded(t)
	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	app.Use(zip.H(cloud.Bridge()))
	return app
}

func browserEndpoint(app *zip.App, s screen, ref func() string) {
	s = stating(s, settlement{cents: gateCents, currency: "usd"})
	app.Post("/v1/billing/topup/token", s.route(func(c *zip.Ctx) error {
		c.Fiber().Response().Header.Set("Content-Type", "application/json")
		return c.Bytes(http.StatusOK,
			[]byte(`{"transactionId":"txn_b","status":"ok","processorRef":"`+ref()+`"}`))
	}))
}
