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
	"go/ast"
	"go/parser"
	"go/token"
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

// TestCreditDoors_EveryDoorOntoTheMintIsComposedWithTheScreen — the STRUCTURAL half,
// and the one that guards the door this file's behavioural tests cannot reach.
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
// IT ACCEPTS EITHER WIRING because the two doors are registered two different ways while
// being screened by one thing: a typed op is composed at registration
// (zip.Post(<router>.With(screen), …)) and a raw route carries the screen in its handler
// LIST (screenChain(screen)). What is asserted is that ONE of those two spellings is
// present on every credit door — never that both doors are spelled alike, which is a fact
// about zip's registrars and not about the control.
//
// AND IT COMPOSES THE ADDRESS, because zip does: a route's path is its router's prefix
// joined with its leaf, so the typed door is registered as `/v1` + `/payments` and no
// single literal in the source spells it. Matching literals alone would have found no
// registration of that door at all — which is why the found-set is asserted below.
// Reading only what the framework reads is the difference between a check and a habit.
//
// Mutation proof: remove `screen` from either registration and this names the file, the
// line and the door.
func TestCreditDoors_EveryDoorOntoTheMintIsComposedWithTheScreen(t *testing.T) {
	fset := token.NewFileSet()
	// The whole package, not mount.go: a door registered from any other file would
	// otherwise be screened by nobody's assertion.
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}

	// held records which doors were actually FOUND, so the check cannot pass by
	// matching nothing — a renamed or recomposed path with the assertion still green is
	// the same blindness in a new costume, and it is the failure this check already had
	// once.
	held := map[string]bool{}
	for _, pkg := range pkgs {
		groups := groupRouters(pkg.Files)
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Post" {
					return true
				}
				door := doorOf(call, sel, groups)
				if door == "" {
					return true
				}
				held[door] = true
				// The WHOLE registration is read — the router it is made on and the handler
				// list — because the screen legitimately appears in either place, and a
				// NAMED router is followed to where it was built. Neither form can be the
				// one nobody reads.
				if carriesScreen(call) || screenedRouter(call, sel, groups) {
					return true
				}
				t.Errorf("%s registers the credit door %s WITHOUT the screen — this address reaches "+
					"commerce's card money move, so an unscreened one is the whole risk gate "+
					"bypassed by calling the other URL",
					fset.Position(call.Pos()), door)
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

// router is a registration target as this check can read it: where it sits, and whether
// the screen is already on it.
type router struct {
	prefix   string
	screened bool
}

// routersOf is where a registration's target can be. zip.Post takes it as the first
// ARGUMENT; a method call carries it as the RECEIVER. Both are consulted, so neither
// registrar is the one nobody reads.
func routersOf(call *ast.CallExpr, sel *ast.SelectorExpr) []ast.Expr {
	return []ast.Expr{sel.X, firstNonLiteral(call.Args)}
}

// screenedRouter reports whether the registration's target was BUILT with the screen —
// the typed door's case, where `mint := app.With(screen).Group("/v1")` puts the screen on
// the router and the Post that uses it names only `mint`. A check that read the Post alone
// would call that door unscreened, which is a false alarm; one that stopped looking at the
// Post would miss the raw door, which is a false pass.
func screenedRouter(call *ast.CallExpr, sel *ast.SelectorExpr, groups map[string]router) bool {
	for _, r := range routersOf(call, sel) {
		if resolve(r, groups).screened {
			return true
		}
	}
	return false
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

// groupRouters records every group ASSIGNED a name in this package — its prefix and
// whether it was built with the screen. A named group is the form zipdoc also requires of
// a router a typed op is declared on, so it is the form this package uses and therefore
// the form this check has to follow.
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

// groupCall reads `<router>.Group("<literal>")` into its prefix, and reports whether the
// chain that built it composed the screen.
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
	return router{
		prefix: strings.Trim(lit.Value, `"`),
		// The screen is composed on the RECEIVER of Group, which is what
		// `app.With(screen).Group("/v1")` means.
		screened: carriesScreen(sel.X),
	}, true
}

// carriesScreen reports whether a registration puts the screen on the route, in either of
// the two spellings the two registrars admit: a `.With(…)` composing it onto the router a
// typed op is declared on, or [screenChain] in a raw route's handler list.
//
// Both names are load-bearing, so both are asserted by NAME. That is the point of a
// structural check: it reads what the source says rather than what the reader remembers,
// and a rename that moved the screen without moving this would fail here rather than in
// production.
func carriesScreen(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if v.Sel.Name == "With" {
				found = true
			}
		case *ast.Ident:
			if v.Name == "screenChain" {
				found = true
			}
		}
		return !found
	})
	return found
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

// typedDoor is the typed credit door, composed the way mount.go composes it and
// answering at the 201 the op DECLARES. It stands in for the handler and for nothing
// else: the registration form, the response type and the status are the shipped ones.
func typedDoor(t *testing.T, app *zip.App, screen zip.Middleware, ref func() string) {
	t.Helper()
	zip.Post(app.With(screen), paymentsDoor, settledPayment(ref),
		zip.WithOperationID("takePaymentProbe"),
		zip.WithStatus(http.StatusCreated))
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
// the shipped screen, plus the app-wide Bridge that parks the validated org a typed op
// reads. Without a screen refusal the request reaches paymentOps.take, which refuses
// with its OWN 503 because commerce is not co-resident in a test binary; that
// difference in STATUS is what tells "the screen refused" apart from "the screen
// allowed and the handler ran".
func realPaymentsApp(t *testing.T, screen zip.Middleware) *zip.App {
	t.Helper()
	isolate(t)

	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	app.Use(zip.H(cloud.Bridge()))
	exposePayments(app, screen)
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
		app := realPaymentsApp(t, func(next zip.Handler) zip.Handler { return next })
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

// TestPayments_TheReceiptReadIsNotScreened. A gate belongs on the act it can prevent.
// GET /v1/payments/:id reads a receipt out of the caller's own ledger namespace and
// mints nothing, so screening it would spend a scorer round trip — and, whenever a
// scorer is present and mute, refuse a customer their own receipt — to guard a mint that
// is not there.
//
// Mutation proof: register the read through app.With(screen) too and this fails.
func TestPayments_TheReceiptReadIsNotScreened(t *testing.T) {
	var asked atomic.Bool
	watch := func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			asked.Store(true)
			return next(c)
		}
	}
	app := realPaymentsApp(t, watch)

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
	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	isolate(t)
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
	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	isolate(t)
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
	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	isolate(t)

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
	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	isolate(t)
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

	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	isolate(t)
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})

	screen := riskGate(luxlog.New("paytest"))
	// ONE screen value on BOTH registrations, exactly as mount.go composes it.
	browser := refs("sq_pay_browser_")
	app.With(screen).Post("/v1/billing/topup/token", func(c *zip.Ctx) error {
		c.Fiber().Response().Header.Set("Content-Type", "application/json")
		return c.Bytes(http.StatusOK,
			[]byte(`{"transactionId":"txn_b","status":"ok","processorRef":"`+browser()+`"}`))
	})
	typedDoor(t, app, screen, refs("sq_pay_typed_"))

	for i := 0; i < each; i++ {
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
	for i := 0; i < 2*each; i++ {
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
	app := zip.New(zip.Config{Logger: luxlog.New("paytest"), DisableStartupMessage: true})
	isolate(t)
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})

	screen := riskGate(luxlog.New("paytest"))
	// ONE gateway payment id, reached through both addresses. Each door still writes its
	// OWN ledger receipt, which is exactly the trap: keyed on the receipt this is two
	// observations of one payment.
	app.With(screen).Post("/v1/billing/topup/token", func(c *zip.Ctx) error {
		c.Fiber().Response().Header.Set("Content-Type", "application/json")
		return c.Bytes(http.StatusOK,
			[]byte(`{"transactionId":"txn_browser_1","status":"ok","processorRef":"`+settledRef+`"}`))
	})
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
