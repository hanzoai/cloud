package commerce

// risk_payments_test.go — THE SECOND DOOR ONTO THE MINT, and the one property that
// decides whether screening the first one was a control at all.
//
// commerce holds ONE card money move (billing.TakePayment) and this binary opens TWO
// addresses onto it: the browser's POST /v1/billing/topup/token and the agent's typed
// POST /v1/payments. Both end in the same authorized deposit. So a screen on the first
// address was never a bound on the mint — it was a bound on one entrance, with a second
// entrance beside it that runs the same core, and the second one is the one published as
// an MCP tool for an agent to call.
//
// Four properties, and each one is a way the bypass could come back:
//
//	THE TYPED DOOR IS SCREENED, against its REAL registration — not a stand-in route
//	that only this file knows how to build. Remove the screen from exposePayments and
//	the request walks through to the handler.
//
//	SHADOW IS PRESERVED THERE TOO. A control that is not deployed must not be able to
//	take a second product surface down, and one that is present and mute must still
//	refuse: the same two facts, at the new address.
//
//	A BURST SPLIT ACROSS BOTH DOORS IS ONE ACCRUAL. This is the whole point of doing
//	it with the same gate. If the two doors resolved their payer differently, or keyed
//	their settlements differently, then splitting a burst across the two addresses
//	would halve every velocity bound — a bypass with the screen still switched on.
//
//	ONE PAYMENT THROUGH TWO DOORS IS ONE OBSERVATION. The mirror image: keys that
//	converge for the same money, so nothing is double-counted and no customer is
//	frozen for paying once.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// creditDoors is every address in this package that reaches commerce's card money
// move, and therefore every address that MUST carry the screen. It is a closed list
// because the structural check below reads it: a door added to the binary and not to
// this list is the bug this whole file is about, so the list is also the place the
// next one gets caught.
var creditDoors = []string{"/v1/billing/topup/token", "/v1/payments"}

// screenIsAValue anchors the structural check below to the REAL screen. The check
// works over identifiers, so a rename of the type or of its resolver would silently
// empty the set it reads and leave it green over nothing; this line stops compiling
// first, which is the only kind of anchor an AST test can have.
var screenIsAValue screen = riskGate(luxlog.New("guard"))

// screenType is the screen's own type name, which is what makes an identifier in the
// source recognisable AS the screen. Held to the real one by [screenIsAValue].
const screenType = "screen"

// TestCreditDoors_EveryDoorOntoTheMintIsComposedWithTheScreen — the STRUCTURAL half,
// and the one that guards the doors this file's behavioural tests cannot reach.
//
// The behavioural tests drive registrations: exposePayments is called directly, so the
// typed door's wiring is real. Mount's own registration of the browser door is not —
// nothing in the package calls Mount, by convention (methods_route_test.go says so in
// as many words), so a future edit that drops `screen` from that one Post would leave
// every test in this package green while the original bypass reopened on the other side.
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
//	checked over EVERY registration rather than only the credit doors, because the
//	next mint op is not on the list yet.
//
// WHAT HOLDS THE SCREEN is not a name this test knows: it is every identifier the
// package binds to [riskGate]'s answer, plus every parameter DECLARED to be one
// ([screenValues]). A door reached through a differently-named local, or through a
// second function that takes the screen, is therefore read correctly — and a door that
// invents its own screen is not, which is the point.
//
// AND IT COMPOSES THE ADDRESS, because zip does: a route's path is its router's prefix
// joined with its leaf, so a door declared on a group is spelled by no single literal
// in the source. Matching literals alone would find no registration at all — which is
// why the found-set is asserted below. Reading only what the framework reads is the
// difference between a check and a habit.
//
// Mutation proof: register the typed op as `zip.Post(app, "/v1/payments", o.charge, …)`
// — the money core with no wrap — and this names the file, the line and the door.
// Restore the old `zip.Post(app.With(screen).Group("/v1"), "/payments", o.charge)` and
// it fails twice: once for the unscreened handler, once for the router composition.
func TestCreditDoors_EveryDoorOntoTheMintIsComposedWithTheScreen(t *testing.T) {
	fset := token.NewFileSet()
	pkgs := shipped(t, fset)

	// held records which doors were actually FOUND, so the check cannot pass by
	// matching nothing — a renamed or recomposed path with the assertion still green is
	// the same blindness in a new costume, and it is the failure this check already had
	// once.
	held := map[string]bool{}
	for _, pkg := range pkgs {
		values := screenValues(pkg.Files)
		if len(values) == 0 {
			t.Fatal("this check found no identifier holding the screen, so it is reading nothing " +
				"and would stay green however the credit doors are wired — the screen's type or " +
				"its resolver was renamed and screenType/riskGate must move with it")
		}
		groups := groupRouters(pkg.Files)
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// THE SCREEN IS NEVER ROUTER MIDDLEWARE. Checked over every registration in
				// the package, not only the credit doors: this spelling guards one
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
				door := doorOf(call, sel, groups)
				if door == "" {
					return true
				}
				held[door] = true
				h := handlerOf(call, sel)
				if h != nil && carries(h, values) {
					return true
				}
				t.Errorf("%s registers the credit door %s with a handler the screen VALUE never "+
					"reached — this address ends in commerce's card money move, and a screen that "+
					"is not inside the handler is absent from every projection of it except the "+
					"one HTTP route", fset.Position(call.Pos()), door)
				return true
			})
		}
	}

	for _, door := range creditDoors {
		if !held[door] {
			t.Errorf("no registration of the credit door %s was found — either it moved (and this "+
				"list must move with it) or the check is reading nothing and would stay green "+
				"however the door is wired", door)
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
// The list exists because the pre-charge refusal is a property of the two doors the
// screen is composed onto, and NOT of the ledger. A mint added beside these three is a
// mint with no screen in front of it: [screen.settle]'s own [payment.diverged] guard
// backstops the address, but nothing stops a new caller charging a card first and
// discovering afterwards that the credit has nowhere to go. That is the defect this
// batch fixed, and a new entry here is where the next one gets caught.
var mints = map[string]string{
	"settle": "the CARD settlement — the only mint behind a charge, screened at both doors, " +
		"and refused before the card moves when the charge and the credit name two orgs",
	"planeCredit": "the internal plane's credit op — the org is the CALLER's and can never be an " +
		"argument, the ref is required, and no card is charged, so there is nothing to take first",
	"Credit": "commerce's creditledger adapter — the admin grant, the door a platform operator " +
		"funds another organisation through and the one the pre-charge refusal names",
}

// TestCreditDoors_EveryMintIntoTheSpendableLedgerIsAccountedFor — the same structural
// argument as the door check, one level down.
//
// The door check asks whether every ADDRESS onto the card core is screened. This asks
// what can put money in the ledger AT ALL, because the two questions have different
// answers: a mint reached from a webhook, a job or a second settlement path is not a
// door and would not appear on [creditDoors] at all.
//
// It is a LIST WITH REASONS rather than a count, so the failure tells the next author
// what the check is for: either the new mint charges nothing first (say why, add it) or
// it charges a card and needs the screen's own refusal ahead of it.
//
// Mutation proof: delete any entry from [mints] and the check names the function and the
// line; add a fourth `fin.Deposit` anywhere in the package and it fails until the reason
// is written down.
func TestCreditDoors_EveryMintIntoTheSpendableLedgerIsAccountedFor(t *testing.T) {
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
// screen)`). Following the parameter is what lets a door be registered in a different
// function from the one that resolved the screen, which is exactly how the typed door
// is wired — and it is why this reads a value's travel rather than a fixed name.
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
// that knew only one of them would be blind to exactly the other door: a typed op is
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

// doorOf returns the credit door a registration addresses, or "" for a route that is not
// one. The address is the ROUTER'S PREFIX joined with the leaf, which is how zip composes
// it; a path assembled from anything but literals is one this check cannot vouch for, and
// it reports nothing rather than pretending otherwise (the found-set assertion is what
// turns such a gap into a failure instead of a silence).
func doorOf(call *ast.CallExpr, sel *ast.SelectorExpr, groups map[string]router) string {
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
	for _, door := range creditDoors {
		if got == door {
			return door
		}
	}
	return ""
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
// and the reason a door's ADDRESS is composed rather than matched as a literal.
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

// paymentsDoor is the typed door's address.
const paymentsDoor = "/v1/payments"

// isolate gives one test its own risk plane: a socket directory with no listener in it
// (so "not deployed" is the state of the world unless the test installs a scorer) and a
// scorer seam that is put back afterwards.
//
// It is [shortRuntimeDir] and not t.TempDir, and the difference is load-bearing HERE
// more than anywhere: a unix socket address is capped near a hundred bytes, t.TempDir
// embeds the calling test's name, and the names in this file run to sixty-four
// characters. Over the cap every dial answers `connect: invalid argument` — which is
// indistinguishable from the undeployed peer these tests assert about, so the suite
// would go red on any Mac for a reason that looks exactly like its own subject. That
// already happened once to the two tests next door; this is the same fix, reached for
// rather than rediscovered.
func isolate(t *testing.T) {
	t.Helper()
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
}

// refs mints a distinct gateway payment id per settlement, which is what a real
// processor does and what the accrual depends on: distinct money must carry distinct
// keys or the record deduplicates real payments away.
func refs(prefix string) func() string {
	var n int64
	return func() string { return prefix + strconv.FormatInt(atomic.AddInt64(&n, 1), 10) }
}

// settledPayment answers the way the typed op answers a charge that cleared: the REAL
// PaymentOut, so the field names the settlement key is read out of are the shipped ones
// and not a fixture's idea of them.
//
// ref returning "" is the processor that settled while stating no reference — the case
// where the key falls back to the ledger receipt, which this door spells `id`.
func settledPayment(ref func() string) func(context.Context, *PaymentIn) (*PaymentOut, error) {
	receipts := refs("txn_typed_")
	return func(_ context.Context, in *PaymentIn) (*PaymentOut, error) {
		return &PaymentOut{
			ID:           receipts(),
			BalanceCents: in.AmountCents,
			Status:       "ok",
			ProcessorRef: ref(),
		}, nil
	}
}

// payApp is a test app wired the way the binary is: the app-wide Bridge FIRST, because
// it is what parks the request a typed op reads its payer off ([cloud.Request]), and it
// must precede every route it serves — fiber runs middleware in registration order.
// It also publishes the ONE spendable ledger, because a settled payment now deposits
// into it before anything else happens (settle.go): a door fixture with no ledger
// behind it is a door that answers 500 to every charge that clears.
func payApp(t *testing.T) *zip.App {
	t.Helper()
	isolate(t)
	funded(t)
	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	app.Use(zip.H(cloud.Bridge()))
	return app
}

// typedDoor is the typed credit door, composed the way exposePayments composes it and
// answering at the 201 the op DECLARES: the screen WRAPS THE HANDLER, so the registered
// op is the screened one and every projection of it runs the screen. It stands in for
// the money core and for nothing else — the registration form, the response type and
// the status are the shipped ones.
//
// The screen is handed the settlement its fake core stands for, because the door
// finishes a cleared charge by depositing what SETTLED (settle.go) and the amount of
// that is a fact about a commerce row this fixture does not write.
func typedDoor(t *testing.T, app *zip.App, s screen, ref func() string) {
	t.Helper()
	s = stating(s, settlement{cents: gateCents, currency: "usd"})
	zip.Post(app, paymentsDoor, s.op(settledPayment(ref)),
		zip.WithOperationID("takePaymentProbe"),
		zip.WithStatus(http.StatusCreated))
}

// browserDoor is the RAW credit door, composed the way mount.go composes it: the screen
// wrapping the handler that answers like commerce's top-up.
// It states the settlement for [typedDoor]'s reason: the deposit is sized by what
// cleared, not by what the body asked for.
func browserDoor(app *zip.App, s screen, ref func() string) {
	s = stating(s, settlement{cents: gateCents, currency: "usd"})
	app.Post("/v1/billing/topup/token", s.route(func(c *zip.Ctx) error {
		c.Fiber().Response().Header.Set("Content-Type", "application/json")
		return c.Bytes(http.StatusOK,
			[]byte(`{"transactionId":"txn_b","status":"ok","processorRef":"`+ref()+`"}`))
	}))
}

// pay posts the credit door's own body to a door, as a validated customer. The body is
// [gateBody] — the SAME bytes the browser door is driven with — because PaymentIn and
// commerce's top-up request declare the amount and the currency under the same names,
// which is exactly why one signal reader serves both.
func pay(t *testing.T, app *zip.App, door string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, door, strings.NewReader(gateBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Org-Id", gateOrg)
	r.Header.Set("X-User-Id", gateUser)
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("post %s: %v", door, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b := make([]byte, 4096)
	n, _ := resp.Body.Read(b)
	return resp.StatusCode, string(b[:n])
}

// realPaymentsApp registers the SHIPPED payment surface — exposePayments itself — with
// the shipped screen. Without a screen refusal the request reaches paymentOps.charge,
// which refuses with its OWN 503 because commerce is not co-resident in a test binary;
// that difference in STATUS is what tells "the screen refused" apart from "the screen
// allowed and the money core ran".
func realPaymentsApp(t *testing.T, s screen) *zip.App {
	t.Helper()
	app := payApp(t)
	exposePayments(app, s)
	return app
}

// unscreenedPaymentsApp IS THE MUTATION, kept as a fixture: the money core registered
// as the op's handler with no wrap — `zip.Post(app, "/v1/payments", o.charge, …)` —
// which is what exposePayments would be if the screen were dropped from it, and what
// the composition looked like from the MCP plane's point of view while the screen was
// router middleware.
//
// It is the CONTROL FOR EVERY SCREEN ASSERTION in this file: a refusal only proves the
// screen if the same request walks through without it.
func unscreenedPaymentsApp(t *testing.T) *zip.App {
	t.Helper()
	app := payApp(t)
	zip.Post(app, paymentsDoor, paymentOps{}.charge,
		zip.WithOperationID("takePayment"),
		zip.WithStatus(http.StatusCreated))
	return app
}

// TestPayments_TheTypedDoorIsScreened — THE BYPASS, closed, against the real thing.
//
// A verdict that freezes a payment must refuse it at THIS address as surely as at the
// browser's. The refusal is read off the STATUS and off whose sentence it is: 403 in the
// screen's own words means the screen answered and paymentOps.take never ran, where the
// handler's own refusal for a test binary is a 503 naming co-residence.
//
// Mutation proof: drop `screen` from the zip.Post in [exposePayments] (or hand it the
// identity middleware) and the blocked case answers 503 — the frozen payment reached the
// money core, which is the state this test exists to make impossible to reintroduce.
func TestPayments_TheTypedDoorIsScreened(t *testing.T) {
	lg := luxlog.New("paytest")

	// A scorer that freezes everything: the verdict is not the subject of this test, the
	// door's obedience to it is.
	frozen := func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionBlock, Score: 0.99, Cause: "above the cut"}, nil
	}

	t.Run("screened — the frozen payment never reaches the money core", func(t *testing.T) {
		app := realPaymentsApp(t, riskGate(lg))
		cloud.SetRiskScorer(frozen)

		code, body := pay(t, app, paymentsDoor)
		if code != http.StatusForbidden {
			t.Fatalf("%d %s, want 403 — a payment the screen froze settled through the second door, "+
				"so the whole risk gate is bypassable by calling the op an agent already has", code, body)
		}
		if !strings.Contains(body, "not authorised") {
			t.Errorf("the refusal is not the screen's: %s", body)
		}
		// AND THE HANDLER NEVER RAN. Its own refusal names co-residence; seeing that
		// sentence would mean the screen let a frozen payment through to the core and only
		// the test environment stopped it.
		if strings.Contains(body, "co-resident") {
			t.Errorf("the frozen payment reached paymentOps.take: %s", body)
		}
	})

	// THE CONTROL FOR THE CONTROL. Without a screen the same request walks through to the
	// money core — which is what makes the case above evidence of the gate rather than
	// evidence of the fixture.
	t.Run("unscreened — the same request reaches the money core", func(t *testing.T) {
		app := unscreenedPaymentsApp(t)
		cloud.SetRiskScorer(frozen)

		code, body := pay(t, app, paymentsDoor)
		if code == http.StatusForbidden {
			t.Fatalf("%d %s — the ungated door refused, so the 403 above proves nothing about the screen", code, body)
		}
		if !strings.Contains(body, "co-resident") {
			t.Fatalf("%d %s, want the handler's own refusal — this fixture must reach paymentOps.take "+
				"or it cannot show what the screen prevents", code, body)
		}
	})
}

// tool invokes one MCP tools/call against the app's own /mcp door — the SAME door
// api.hanzo.ai publishes and the SAME dispatch an agent's call takes: zip finds the op
// by name and runs op.invoke, which is the typed op's HANDLER and nothing around it.
//
// It answers what the model would see: the JSON-RPC result's text content, and whether
// the tool reported a failure. An MCP error is CONTENT with isError, not a transport
// error, so a refused payment arrives as a 200 carrying the refusal — which is exactly
// why a status-code assertion cannot see this plane and this helper has to read the
// envelope.
func tool(t *testing.T, app *zip.App, name, args string) (text string, isError bool) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Org-Id", gateOrg)
	r.Header.Set("X-User-Id", gateUser)
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("tools/call %s answered %s, which is not JSON-RPC: %v", name, raw, err)
	}
	if out.Error != nil {
		t.Fatalf("tools/call %s was refused by the MCP door itself (%s) — the tool has to be "+
			"REACHABLE for this file to say anything about whether it is screened", name, out.Error.Message)
	}
	for _, c := range out.Result.Content {
		text += c.Text
	}
	return text, out.Result.IsError
}

// TestPayments_TheAgentsToolIsScreened — THE BYPASS THIS BATCH CLOSED, at the plane it
// was open on.
//
// `takePayment` is in tools/list. The screen was ROUTER middleware, and zip composes
// router middleware around the fiber handler its REST route is served through — while
// a typed op is dispatched to its HANDLER by four projections. So a payment the model
// froze was refused at the URL and MINTED through the tool, and the settlement the
// model should have learned from was never taught: the whole control present on the
// browser's door and absent on the agent's, silently, with every HTTP test green.
//
// Two halves, and the second is the one that has no status code to hide behind — an
// MCP failure is content, not a transport error, so an unscreened tool answers 200
// either way and only what the model READS tells the two apart.
//
// Mutation proof, and it is the exact regression: register the op without the wrap
// (unscreenedPaymentsApp — `zip.Post(app, "/v1/payments", o.charge, …)`, which is what
// the handler looked like from this plane while the screen sat on the router) and the
// frozen payment reaches the money core. That control runs below, in this test, so a
// pass here is evidence about the screen rather than about the fixture.
func TestPayments_TheAgentsToolIsScreened(t *testing.T) {
	lg := luxlog.New("paytest")
	frozen := func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionBlock, Score: 0.99, Cause: "above the cut"}, nil
	}

	t.Run("screened — the frozen payment never reaches the money core", func(t *testing.T) {
		app := realPaymentsApp(t, riskGate(lg))
		cloud.SetRiskScorer(frozen)

		text, isError := tool(t, app, "takePayment", gateBody)
		if !isError {
			t.Fatalf("the tool answered %s with no error — a payment the screen froze settled "+
				"through the MCP plane, which is the whole risk gate bypassed by an agent "+
				"calling the tool instead of the URL", text)
		}
		if !strings.Contains(text, "not authorised") {
			t.Errorf("the refusal is not the screen's: %s", text)
		}
		// AND THE MONEY CORE NEVER RAN. Its own refusal names co-residence; seeing that
		// sentence would mean the screen let a frozen payment through and only the test
		// environment stopped it.
		if strings.Contains(text, "co-resident") {
			t.Errorf("the frozen payment reached the money core through the tool: %s", text)
		}
	})

	// THE CONTROL FOR THE CONTROL: the op registered with no wrap — the state the MCP
	// plane was in — reaches the core with the same frozen verdict installed.
	t.Run("unscreened — the same tool call reaches the money core", func(t *testing.T) {
		app := unscreenedPaymentsApp(t)
		cloud.SetRiskScorer(frozen)

		text, _ := tool(t, app, "takePayment", gateBody)
		if strings.Contains(text, "not authorised") {
			t.Fatalf("the ungated tool refused, so the refusal above proves nothing: %s", text)
		}
		if !strings.Contains(text, "co-resident") {
			t.Fatalf("want the money core's own refusal, got %s — this fixture must REACH "+
				"paymentOps.charge or it cannot show what the screen prevents", text)
		}
	})
}

// TestPayments_TheAgentsToolJudgesAndTeachesThePayer — the OTHER half, and the one the
// bypass made invisible: an agent's settled payment has to accrue on the same subject
// the browser's does, or the velocity bound sees half the money and a stolen card is
// laundered through the tool.
//
// It pins every fact the two planes must agree on, each against the SAME rule the HTTP
// test asserts rather than against the other plane's answer — matching each other would
// pass just as well if both were wrong:
//
//	THE PAYER RESOLVES HERE TOO. A tools/call is an ordinary HTTP request carrying the
//	gateway's identity headers, and the app-wide Bridge parks it for /mcp exactly as
//	for a REST route — so [payerOrg] and [principal.Subject] answer, and the org and
//	the wallet key are the browser door's.
//
//	THE VALUE IS STATED. Over MCP the request body is a JSON-RPC envelope and the
//	payment is inside `arguments`, so a screen that read the wire would state no
//	amount at all — the sharpest axis a credit door has, blind on exactly the plane an
//	agent uses. It is read off the DECODED input instead.
//
//	AND THE SETTLEMENT TEACHES. Over MCP the op's answer is wrapped in a tools/call
//	result and no HTTP response exists when the handler returns, so a screen that read
//	the response bytes would watch an agent's payment settle and teach nothing. It is
//	read off the RETURNED receipt instead.
//
// Mutation proof: register the op without the wrap and this fails on the await timeout
// — the tool payment settles and the model is never told.
func TestPayments_TheAgentsToolJudgesAndTeachesThePayer(t *testing.T) {
	seen := watchTeaching(t)
	app := payApp(t)

	var asked cloud.RiskQuery
	var forOrg string
	cloud.SetRiskScorer(func(_ context.Context, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		forOrg, asked = org, q
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	typedDoor(t, app, riskGate(luxlog.New("paytest")), func() string { return settledRef })

	text, isError := tool(t, app, "takePaymentProbe", gateBody)
	if isError {
		t.Fatalf("the tool payment did not settle: %s", text)
	}

	want := account.Payer(account.Credential{Owner: gateOrg, Name: gateUser}).Subject()

	// THE QUESTION. Same tenant, same stage, same kind, same subject, same privileged
	// bit, same value as the browser door asks with.
	if forOrg != gateOrg {
		t.Errorf("the model asked was %q's, want %q's — the payer does not resolve on the "+
			"MCP plane, so the screen is judging nobody there", forOrg, gateOrg)
	}
	if asked.Stage != cloud.StagePayment {
		t.Errorf("stage %q, want %q", asked.Stage, cloud.StagePayment)
	}
	if asked.Subject.Kind != plane.KindPayer || asked.Subject.ID != want {
		t.Errorf("judged %q/%q, want %q/%q — an agent's payments must accrue on the same key "+
			"a browser's do or the tool is a way to spend a bound twice",
			asked.Subject.Kind, asked.Subject.ID, plane.KindPayer, want)
	}
	if !asked.Privileged {
		t.Error("the query is not privileged — a scorer outage mints spendable balance through the tool")
	}
	if got := asked.Signals[plane.SignalNano]; got != "42000000000" {
		t.Errorf("nano %q, want %q — the amount was not observed on the MCP plane, so the value "+
			"axis is blind on exactly the door an agent calls", got, "42000000000")
	}

	// AND THE RECORD, which the bypass left structurally missing at this plane.
	got := await(t, seen)
	if got.org != gateOrg {
		t.Errorf("the observation was filed under %q, want %q", got.org, gateOrg)
	}
	if got.in.Kind != plane.KindPayer || got.in.Subject != want {
		t.Errorf("taught %q/%q, want %q/%q", got.in.Kind, got.in.Subject, plane.KindPayer, want)
	}
	if got.in.Settlement != settledRef {
		t.Errorf("settlement %q, want the processor's own reference %q — a tool payment that "+
			"teaches under a different key is a burst the accrual cannot see whole",
			got.in.Settlement, settledRef)
	}
	var nano string
	for _, s := range got.in.Signals {
		if s.Name == plane.SignalNano {
			nano = s.Value
		}
	}
	if nano != "42000000000" {
		t.Errorf("taught nano %q, want %q", nano, "42000000000")
	}
}

// TestPayments_TheReceiptReadIsNotScreened. A gate belongs on the act it can prevent.
// GET /v1/payments/:id reads a receipt out of the caller's own ledger namespace and
// mints nothing, so screening it would spend a scorer round trip — and, whenever a
// scorer is present and mute, refuse a customer their own receipt — to guard a mint that
// is not there.
//
// The SCORER is what is watched, not a stand-in middleware, because the scorer round
// trip is the cost the read must not pay: a screen that ran without asking anything
// would be a screen this test could not see.
//
// Mutation proof: wrap the read's handler with the screen too and this fails.
func TestPayments_TheReceiptReadIsNotScreened(t *testing.T) {
	var asked atomic.Bool
	app := realPaymentsApp(t, riskGate(luxlog.New("paytest")))
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		asked.Store(true)
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})

	r := httptest.NewRequest(http.MethodGet, paymentsDoor+"/txn_1", nil)
	r.Header.Set("X-Org-Id", gateOrg)
	r.Header.Set("X-User-Id", gateUser)
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if asked.Load() {
		t.Error("the receipt read was screened — a read that mints nothing must not be able to be " +
			"refused by a risk-plane outage")
	}
}

// TestPayments_ANotDeployedScorerDoesNotCloseTheTypedDoor — the NEGATIVE CONTROL,
// carried onto the second door.
//
// A fleet that does not run the risk app must still take money at both addresses. This
// is the half that keeps the screen from being an outage: widening a gate to a new
// surface widens the RECORD, and a control that is not installed must not decide whether
// a product works.
func TestPayments_ANotDeployedScorerDoesNotCloseTheTypedDoor(t *testing.T) {
	app := payApp(t)
	typedDoor(t, app, riskGate(luxlog.New("paytest")), refs("sq_pay_"))

	// No producer at all — the state of every process before the gate existed.
	cloud.SetRiskScorer(nil)
	if code, body := pay(t, app, paymentsDoor); code != http.StatusCreated {
		t.Fatalf("no scorer installed: %d %s, want 201 — an uninstalled control must not refuse a payment", code, body)
	}

	// The producer the fleet installs, against a fleet with no risk app: the socket has
	// no listener, so the answer is ABSENT and the door stays open.
	installRiskScorer(luxlog.New("paytest"))
	if code, body := pay(t, app, paymentsDoor); code != http.StatusCreated {
		t.Fatalf("scorer installed, peer not deployed: %d %s, want 201 — an unreachable model is "+
			"absent, not a denial", code, body)
	}
}

// TestPayments_APresentScorerThatCannotAnswerMakesTheTypedGrantWait is the other half of
// the fail policy at the new address: the judge is HERE and did not answer, so a
// privileged grant waits rather than minting balance unscored.
//
// Mutation proof: drop `Privileged: true` from the query in [riskGate] and this answers
// 201 — a scorer outage mints spendable balance through the agent's door.
func TestPayments_APresentScorerThatCannotAnswerMakesTheTypedGrantWait(t *testing.T) {
	app := payApp(t)
	typedDoor(t, app, riskGate(luxlog.New("paytest")), refs("sq_pay_"))

	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{}, cloud.ErrNoPeer // present, asked, no answer
	})
	code, body := pay(t, app, paymentsDoor)
	if code == http.StatusCreated {
		t.Fatalf("the payment SETTLED against a scorer that could not answer: %d %s — a stolen card "+
			"clears through the typed door during a risk-plane outage", code, body)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s, want 503", code, body)
	}
}

// TestPayments_TheTypedDoorJudgesAndTeachesThePayer.
//
// The subject and the key are the two things that have to match the other door, so they
// are asserted against the SAME rules the other door's test asserts — account.Payer for
// the subject, the gateway's own reference for the key — rather than against each other.
// Matching each other would pass just as well if both were wrong.
//
// It also pins the STATUS BAND. The typed op declares 201 and the browser door answers
// 200, so a settlement check that recognised one code would allow the agent's payment,
// watch it settle, and teach nothing.
func TestPayments_TheTypedDoorJudgesAndTeachesThePayer(t *testing.T) {
	seen := watchTeaching(t)
	app := payApp(t)

	var asked cloud.RiskQuery
	var forOrg string
	cloud.SetRiskScorer(func(_ context.Context, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		forOrg, asked = org, q
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	typedDoor(t, app, riskGate(luxlog.New("paytest")), func() string { return settledRef })

	if code, body := pay(t, app, paymentsDoor); code != http.StatusCreated {
		t.Fatalf("%d %s, want 201", code, body)
	}

	want := account.Payer(account.Credential{Owner: gateOrg, Name: gateUser}).Subject()

	// THE QUESTION, first. Same tenant, same stage, same kind, same subject, same
	// privileged bit as the browser door asks with.
	if forOrg != gateOrg {
		t.Errorf("the model asked was %q's, want %q's", forOrg, gateOrg)
	}
	if asked.Stage != cloud.StagePayment {
		t.Errorf("stage %q, want %q", asked.Stage, cloud.StagePayment)
	}
	if asked.Subject.Kind != plane.KindPayer || asked.Subject.ID != want {
		t.Errorf("judged %q/%q, want %q/%q — the two doors must judge one payer or a burst split "+
			"across them is two histories", asked.Subject.Kind, asked.Subject.ID, plane.KindPayer, want)
	}
	if !asked.Privileged {
		t.Error("the query is not privileged — cloud.Privileged() does not match this route either")
	}
	if got := asked.Signals[plane.SignalNano]; got != "42000000000" {
		t.Errorf("nano %q, want %q — the typed body's amount was not observed", got, "42000000000")
	}

	// AND THE RECORD, which is the half that was structurally missing at this address.
	got := await(t, seen)
	if got.org != gateOrg {
		t.Errorf("the observation was filed under %q, want %q", got.org, gateOrg)
	}
	if got.in.Stage != cloud.StagePayment {
		t.Errorf("stage %q, want %q", got.in.Stage, cloud.StagePayment)
	}
	if got.in.Kind != plane.KindPayer || got.in.Subject != want {
		t.Errorf("taught %q/%q, want %q/%q", got.in.Kind, got.in.Subject, plane.KindPayer, want)
	}
	if got.in.Settlement != settledRef {
		t.Errorf("settlement %q, want the processor's own reference %q", got.in.Settlement, settledRef)
	}
}

// TestPayments_TheTypedReceiptCarriesTheFallbackKey.
//
// The two doors spell the ledger receipt differently — commerce's core returns
// TransactionID, the browser door forwards it as `transactionId`, the typed op renames it
// to `id` — and it is the SAME ledger row either way. So a processor that settles while
// stating no reference must still be keyable at this door.
//
// Mutation proof: drop the `id` field from [settlementOf] and this fails on the timeout:
// the payment settled and taught nothing, so the accrual is blind to every settlement
// whose processor stated no reference — silently, at the door an agent calls.
func TestPayments_TheTypedReceiptCarriesTheFallbackKey(t *testing.T) {
	seen := watchTeaching(t)
	app := payApp(t)
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	// The processor stated no reference.
	typedDoor(t, app, riskGate(luxlog.New("paytest")), func() string { return "" })

	if code, body := pay(t, app, paymentsDoor); code != http.StatusCreated {
		t.Fatalf("%d %s, want 201", code, body)
	}
	got := await(t, seen)
	if !strings.HasPrefix(got.in.Settlement, "txn_typed_") {
		t.Errorf("settlement %q — the typed door's ledger receipt is not the key, so a settlement "+
			"with no processor reference teaches nothing here", got.in.Settlement)
	}
}

// TestPayments_ABurstSplitAcrossBothDoorsIsOneAccrual — THE CROSS-DOOR PROOF, and the
// reason the second door had to hold the SAME gate rather than a gate of its own.
//
// Velocity is a bound only if it sees the whole burst. An attacker with a stolen card
// does not care which URL takes it: six payments, three at each address, is the shape
// that defeats a per-door accrual. The two doors converge iff three things hold, and
// each is asserted separately here because each can break on its own:
//
//	ONE SUBJECT — both doors resolve the payer through [payerOrg] and
//	principal.Subject, so all six observations name one key. Two rules here and the
//	accrual splits in half with the screen still switched on.
//
//	SIX KEYS — distinct money carries distinct settlement ids, so nothing
//	deduplicates a real payment away. One key for all six and the plane learns once.
//
//	THE WHOLE VALUE — the nano the six carry sums to the burst, so the accrual the
//	pace rule reads is the money that actually arrived.
//
// What turns that sequence into a REFUSAL is apps/risk, where the accrual lives, and it
// is proven there over exactly this shape:
// [risk.TestObserve_ABurstSplitAcrossTwoDoorsAccruesOnOneSubject].
func TestPayments_ABurstSplitAcrossBothDoorsIsOneAccrual(t *testing.T) {
	const each = 3
	seen := watchTeaching(t)

	app := payApp(t)
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})

	screen := riskGate(luxlog.New("paytest"))
	// ONE screen value wrapping BOTH handlers, exactly as mount.go composes it.
	browserDoor(app, screen, refs("sq_pay_browser_"))
	typedDoor(t, app, screen, refs("sq_pay_typed_"))

	for i := range each {
		if code, body := pay(t, app, "/v1/billing/topup/token"); code != http.StatusOK {
			t.Fatalf("browser payment %d: %d %s", i, code, body)
		}
		if code, body := pay(t, app, paymentsDoor); code != http.StatusCreated {
			t.Fatalf("typed payment %d: %d %s", i, code, body)
		}
	}

	want := account.Payer(account.Credential{Owner: gateOrg, Name: gateUser}).Subject()
	keys := map[string]bool{}
	var total int64
	for i := range 2 * each {
		got := await(t, seen)

		// ONE SUBJECT. This is the property the whole cross-door argument rests on: half
		// a burst filed under a second key is half a burst nothing bounds.
		if got.in.Kind != plane.KindPayer || got.in.Subject != want {
			t.Fatalf("observation %d named %q/%q, want %q/%q — the doors accrue on two keys, so "+
				"splitting a burst across them halves every velocity bound",
				i, got.in.Kind, got.in.Subject, plane.KindPayer, want)
		}
		if got.org != gateOrg {
			t.Errorf("observation %d filed under %q, want %q", i, got.org, gateOrg)
		}
		keys[got.in.Settlement] = true
		for _, s := range got.in.Signals {
			if s.Name != plane.SignalNano {
				continue
			}
			n, err := strconv.ParseInt(s.Value, 10, 64)
			if err != nil {
				t.Fatalf("observation %d stated nano %q, which is not a number", i, s.Value)
			}
			total += n
		}
	}

	// SIX KEYS for six payments. Distinct money must not deduplicate.
	if len(keys) != 2*each {
		t.Errorf("%d distinct settlements for %d payments (%v) — real payments are deduplicating "+
			"away and the accrual reads less money than arrived", len(keys), 2*each, keys)
	}
	// AND THE WHOLE VALUE ARRIVED. $42.00 six times, in nano-USD.
	if want := int64(2*each) * 42_000_000_000; total != want {
		t.Errorf("the accrual was taught %d nano, want %d — the burst is being under-counted", total, want)
	}
}

// TestPayments_OnePaymentThroughTwoDoorsIsOneObservation — the mirror image of the burst.
//
// The gateway's payment id is the one identifier that is the same across every path that
// can credit ONE payment, and both doors return it out of the same core. So the same
// money arriving at both addresses — a retry that changed URL, a client that fell back —
// states ONE settlement key, and the plane's dedupe on (tenant, id) makes the second an
// inert replay rather than a second count. Velocity that double-counted would freeze a
// customer for paying once.
//
// Mutation proof: key on the ledger receipt instead of the processor reference (swap the
// order in [settlementOf]) and this fails with two keys — the two doors write their own
// receipts even for one payment.
func TestPayments_OnePaymentThroughTwoDoorsIsOneObservation(t *testing.T) {
	seen := watchTeaching(t)
	app := payApp(t)
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})

	screen := riskGate(luxlog.New("paytest"))
	// ONE gateway payment id, reached through both addresses. Each door still writes its
	// OWN ledger receipt, which is exactly the trap: keyed on the receipt this is two
	// observations of one payment.
	browserDoor(app, screen, func() string { return settledRef })
	typedDoor(t, app, screen, func() string { return settledRef })

	if code, body := pay(t, app, "/v1/billing/topup/token"); code != http.StatusOK {
		t.Fatalf("browser: %d %s", code, body)
	}
	if code, body := pay(t, app, paymentsDoor); code != http.StatusCreated {
		t.Fatalf("typed: %d %s", code, body)
	}

	first, second := await(t, seen), await(t, seen)
	if first.in.Settlement != settledRef || second.in.Settlement != settledRef {
		t.Fatalf("the two doors keyed one payment as %q and %q — the plane deduplicates on the "+
			"settlement id, so two keys is the same money counted twice and a customer frozen "+
			"for paying once", first.in.Settlement, second.in.Settlement)
	}
	// And on one subject, so the converging key lands in one tenant's one record.
	if first.in.Subject != second.in.Subject || first.org != second.org {
		t.Errorf("one payment was filed under %q/%q and %q/%q",
			first.org, first.in.Subject, second.org, second.in.Subject)
	}
}
