// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// THE LEDGER. Every operation this subsystem serves is a TYPED OP, or it is named
// below with the reason it is not — and the gate reads the LIVE ROUTER, so neither
// half can rot into prose.
//
// A typed op is ONE registry entry with five projections: the REST route, the
// OpenAPI operation WITH its schema, an MCP tool, a CLI command and a typed SDK
// method. An untyped route gets the route and nothing else. That is the whole cost,
// and on this surface it is 176 of 183 operations — so an agent can read
// `POST /v1/commerce/product` in the document and reach it by no projection but REST.
//
// THERE ARE TWO LEDGERS BECAUSE THERE ARE TWO FACTS, and one list cannot hold both.
// apps/captable and apps/dataroom learned this the expensive way: a package whose
// only list is "cannot" reads as a package at its floor when most of it is merely
// unwritten.
//
//	untypedByDesign  a WIRE this stack cannot describe. Nothing to do here until an
//	                 upstream capability lands, and the entry names which.
//	typingOwed       work that is OWED. The blocker is named and it is REAL, but it
//	                 is somebody's to remove; the entry is deleted when the op is
//	                 written.
//
// The two must SUM with the typed ops to the served surface, so a route added
// anywhere — including inside the hanzoai/commerce module this app embeds — is
// typed by default and an entry naming an address commerce no longer serves goes
// red. That is why this is a test and not the comment in mount.go it replaces:
// a comment cannot notice a route it does not mention, and the module bundles
// below bind 175 addresses none of which any comment enumerated.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// surfaceApp mounts the WHOLE commerce surface through the REAL Mount, so every
// assertion below reads the router the published document is generated from rather
// than a reconstruction of it. Measured: this router and the committed
// plugin/commerce/openapi.json carry the SAME 183 operations, address for address.
//
// The embed check is not decoration. `Mount` answers nil when
// commercemod.Embed fails and serves mountCommerceFailClosed's `All(prefix+"/*")`
// instead — a different and far smaller route table. A harness that skipped the
// check would measure the DEGRADED surface and report every ledger entry below as
// stale, which reads exactly like a finished conversion.
func surfaceApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// A FRESH data dir per call: t.TempDir answers a new one each time, and the
	// embed opens its per-org SQLite stores under it, so two mounts sharing one
	// would open the same files twice.
	if err := Use(app, cloud.Deps{Brand: "hanzo", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	if currentEmbedded() == nil {
		t.Fatal("the commerce embed did not boot, so this router is the fail-closed 503 wildcard " +
			"and not the served surface — every assertion in this file would measure the wrong table")
	}
	return app
}

// commerceOps reads BOTH projections of that router at their one shared address
// form: what the document says is SERVED, and which of those carry a TYPED registry
// entry. Reading the router rather than the source is what makes this a gate.
func commerceOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := surfaceApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "commerce", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// ---- ledger one: a wire this stack cannot describe ----

// untypedByDesign is the CLOSED list of commerce operations that CANNOT be typed
// ops, each with the wire fact that keeps it out. Every reason is re-derived
// against the pinned zip (v1.36.3) and the pinned module (hanzoai/commerce
// v1.50.81) rather than inherited, and cites the line it turns on — a file:line
// nobody can land on is how a refusal stops being re-checkable.
var untypedByDesign = map[string]string{
	"PUT /v1/commerce/catalog/entries/{wildcard1}":    wildcardReason,
	"DELETE /v1/commerce/catalog/entries/{wildcard1}": wildcardReason,
	"POST /v1/commerce/webhooks/{provider}":           webhookReason,
}

const wildcardReason = "a greedy fiber wildcard, and the cost is the WHOLE document rather than one " +
	"parameter name. The route is registered `g.Put(\"/entries/*\")` at " +
	"hanzoai/commerce@v1.50.81/api/catalog/handlers.go:98 (:99 for the DELETE) and mounted here by " +
	"catalogapi.AdminRoute, because a model slug IS its callable id and carries a slash " +
	"(\"anthropic/claude-opus-5\") — a `:slug` param stops at the slash, so every real row 404'd on " +
	"both verbs. zip's Template rewrites only `:name` segments and returns the pattern unchanged when " +
	"it holds no colon (zip@v1.36.3/address.go:61), so a typed op would publish the path VERBATIM as " +
	"/v1/commerce/catalog/entries/*, while cloud's router reading names that segment {wildcard1}. Fold " +
	"looks the op up by zip's spelling, finds no live route at that key, and refuses the ENTIRE " +
	"document — `make -C apps/commerce describe` fails and this app publishes nothing at all."

const webhookReason = "the processor's SIGNATURE over the RAW received bytes is the authentication. " +
	"The handler takes `payload := c.Body()` " +
	"(hanzoai/commerce@v1.50.81/api/billing/webhooks.go:52) and verifies the provider's signature over " +
	"exactly those bytes at :70 — Square signs the notification URL concatenated with them — so a " +
	"re-encoded In is not the value that was signed. zip decodes any non-empty body into In BEFORE the " +
	"handler is entered and answers ErrBadRequest(\"invalid body: …\") on a parse failure " +
	"(zip@v1.36.3/typed.go:485-490), so a typed op would 400 a delivery that verifies today and the " +
	"processor would retry it for days. Registered in mount.go, under this app's own root, because it " +
	"also SETTLES: the handler credits the wallet out of the store this process owns."

// ---- ledger two: work that is owed ----

// moduleHandler is the blocker every typingOwed entry shares, stated ONCE because
// it is one fact about one boundary rather than 173 facts.
//
// It is a REAL blocker and not an awkward shape. mount.go has said so in prose for
// as long as the embed has existed ("the only honest typed op is the payments.go
// pattern"); what it could not do is fail.
const moduleHandler = "registered by github.com/hanzoai/commerce and not by this package. The handler " +
	"is a func(*zip.Ctx) error; a zip.TypedHandler is func(context.Context, *In) (*Out, error) " +
	"(zip@v1.36.3/typed.go:20), which receives no *zip.Ctx on any arm — so a typed op cannot invoke it, " +
	"and typing means REPLACING it. The module's handler is what produces the wire, on three axes at " +
	"once: it writes its own body with commerce's OWN encoder through http.Render " +
	"(hanzoai/commerce@v1.50.81/util/json/http/http.go:13-16, which sets Content-Type itself), it " +
	"refuses through http.Fail's {\"error\":{type,code,message,param}} envelope (:21-61) — which " +
	"OVERRIDES the handler's own status to 402 whenever the error type is \"authorization-error\" " +
	"(:47-49) — and it resolves BOTH its tenant and its permissions off the request " +
	"(util/rest/rest.go:321 datastore.NewNamespaced, :203 middleware.GetPermissions, whose refusal at " +
	":228 writes the 403 body and returns nil, a success a typed op cannot express). A cloud-side " +
	"rewrite is therefore a SECOND implementation of the same merchant and money surface, answering a " +
	"different refusal body, a different status on one error class and a different tenancy resolution. " +
	"The honest conversion is the payments.go pattern — export a value-taking core from the module, " +
	"then declare the op on it here — and that first step is module work, in hanzoai/commerce."

// typingOwed is every operation this app serves that the module registers. The
// entries are keyed by ADDRESS, one per operation, because that is the only key
// that can go red in BOTH directions: a route added upstream is not covered, and
// a route retired upstream is named by nothing served.
//
// The families are the six registrars, each named at its call site in mount.go, so
// a reader who wants to know what to convert next knows which bundle to open.
var typingOwed = owed(
	// commerceresources.Route (mount.go) — 17 merchant kinds x the 7 addresses
	// rest.Rest.defaultRoutes binds per kind (util/rest/rest.go:264-303). The
	// POST on a `{...id}` address is the METHOD OVERRIDE (rest.go:588), which
	// reads `_method` from a FORM value, so that one carries a second blocker of
	// its own on top of the shared one.
	"the merchant resource CRUD (commerceresources.Route). "+moduleHandler,
	"DELETE /v1/commerce/collection/{collectionid}",
	"DELETE /v1/commerce/disclosure/{disclosureid}",
	"DELETE /v1/commerce/discount/{discountid}",
	"DELETE /v1/commerce/movie/{movieid}",
	"DELETE /v1/commerce/note/{noteid}",
	"DELETE /v1/commerce/product/{productid}",
	"DELETE /v1/commerce/return/{returnid}",
	"DELETE /v1/commerce/saleschannel/{saleschannelid}",
	"DELETE /v1/commerce/stocklocation/{stocklocationid}",
	"DELETE /v1/commerce/submission/{submissionid}",
	"DELETE /v1/commerce/subscriber/{subscriberid}",
	"DELETE /v1/commerce/tokentransaction/{tokentransactionid}",
	"DELETE /v1/commerce/transfer/{transferid}",
	"DELETE /v1/commerce/variant/{variantid}",
	"DELETE /v1/commerce/wallet/{walletid}",
	"DELETE /v1/commerce/watchlist/{watchlistid}",
	"DELETE /v1/commerce/webhook/{webhookid}",
	"GET /v1/commerce/collection/",
	"GET /v1/commerce/collection/{collectionid}",
	"GET /v1/commerce/disclosure/",
	"GET /v1/commerce/disclosure/{disclosureid}",
	"GET /v1/commerce/discount/",
	"GET /v1/commerce/discount/{discountid}",
	"GET /v1/commerce/movie/",
	"GET /v1/commerce/movie/{movieid}",
	"GET /v1/commerce/note/",
	"GET /v1/commerce/note/{noteid}",
	"GET /v1/commerce/product/",
	"GET /v1/commerce/product/{productid}",
	"GET /v1/commerce/return/",
	"GET /v1/commerce/return/{returnid}",
	"GET /v1/commerce/saleschannel/",
	"GET /v1/commerce/saleschannel/{saleschannelid}",
	"GET /v1/commerce/stocklocation/",
	"GET /v1/commerce/stocklocation/{stocklocationid}",
	"GET /v1/commerce/submission/",
	"GET /v1/commerce/submission/{submissionid}",
	"GET /v1/commerce/subscriber/",
	"GET /v1/commerce/subscriber/{subscriberid}",
	"GET /v1/commerce/tokentransaction/",
	"GET /v1/commerce/tokentransaction/{tokentransactionid}",
	"GET /v1/commerce/transfer/",
	"GET /v1/commerce/transfer/{transferid}",
	"GET /v1/commerce/variant/",
	"GET /v1/commerce/variant/{variantid}",
	"GET /v1/commerce/wallet/",
	"GET /v1/commerce/wallet/{walletid}",
	"GET /v1/commerce/watchlist/",
	"GET /v1/commerce/watchlist/{watchlistid}",
	"GET /v1/commerce/webhook/",
	"GET /v1/commerce/webhook/{webhookid}",
	"PATCH /v1/commerce/collection/{collectionid}",
	"PATCH /v1/commerce/disclosure/{disclosureid}",
	"PATCH /v1/commerce/discount/{discountid}",
	"PATCH /v1/commerce/movie/{movieid}",
	"PATCH /v1/commerce/note/{noteid}",
	"PATCH /v1/commerce/product/{productid}",
	"PATCH /v1/commerce/return/{returnid}",
	"PATCH /v1/commerce/saleschannel/{saleschannelid}",
	"PATCH /v1/commerce/stocklocation/{stocklocationid}",
	"PATCH /v1/commerce/submission/{submissionid}",
	"PATCH /v1/commerce/subscriber/{subscriberid}",
	"PATCH /v1/commerce/tokentransaction/{tokentransactionid}",
	"PATCH /v1/commerce/transfer/{transferid}",
	"PATCH /v1/commerce/variant/{variantid}",
	"PATCH /v1/commerce/wallet/{walletid}",
	"PATCH /v1/commerce/watchlist/{watchlistid}",
	"PATCH /v1/commerce/webhook/{webhookid}",
	"POST /v1/commerce/collection/",
	"POST /v1/commerce/collection/{collectionid}",
	"POST /v1/commerce/disclosure/",
	"POST /v1/commerce/disclosure/{disclosureid}",
	"POST /v1/commerce/discount/",
	"POST /v1/commerce/discount/{discountid}",
	"POST /v1/commerce/movie/",
	"POST /v1/commerce/movie/{movieid}",
	"POST /v1/commerce/note/",
	"POST /v1/commerce/note/{noteid}",
	"POST /v1/commerce/product/",
	"POST /v1/commerce/product/{productid}",
	"POST /v1/commerce/return/",
	"POST /v1/commerce/return/{returnid}",
	"POST /v1/commerce/saleschannel/",
	"POST /v1/commerce/saleschannel/{saleschannelid}",
	"POST /v1/commerce/stocklocation/",
	"POST /v1/commerce/stocklocation/{stocklocationid}",
	"POST /v1/commerce/submission/",
	"POST /v1/commerce/submission/{submissionid}",
	"POST /v1/commerce/subscriber/",
	"POST /v1/commerce/subscriber/{subscriberid}",
	"POST /v1/commerce/tokentransaction/",
	"POST /v1/commerce/tokentransaction/{tokentransactionid}",
	"POST /v1/commerce/transfer/",
	"POST /v1/commerce/transfer/{transferid}",
	"POST /v1/commerce/variant/",
	"POST /v1/commerce/variant/{variantid}",
	"POST /v1/commerce/wallet/",
	"POST /v1/commerce/wallet/{walletid}",
	"POST /v1/commerce/watchlist/",
	"POST /v1/commerce/watchlist/{watchlistid}",
	"POST /v1/commerce/webhook/",
	"POST /v1/commerce/webhook/{webhookid}",
	"PUT /v1/commerce/collection/{collectionid}",
	"PUT /v1/commerce/disclosure/{disclosureid}",
	"PUT /v1/commerce/discount/{discountid}",
	"PUT /v1/commerce/movie/{movieid}",
	"PUT /v1/commerce/note/{noteid}",
	"PUT /v1/commerce/product/{productid}",
	"PUT /v1/commerce/return/{returnid}",
	"PUT /v1/commerce/saleschannel/{saleschannelid}",
	"PUT /v1/commerce/stocklocation/{stocklocationid}",
	"PUT /v1/commerce/submission/{submissionid}",
	"PUT /v1/commerce/subscriber/{subscriberid}",
	"PUT /v1/commerce/tokentransaction/{tokentransactionid}",
	"PUT /v1/commerce/transfer/{transferid}",
	"PUT /v1/commerce/variant/{variantid}",
	"PUT /v1/commerce/wallet/{walletid}",
	"PUT /v1/commerce/watchlist/{watchlistid}",
	"PUT /v1/commerce/webhook/{webhookid}",
)

func init() {
	// commercestore.Route (mount.go) — the storefront and the checkout it fronts:
	// the store CRUD on the same rest.Rest scaffold, plus the authorize/capture/
	// charge family and the listing reads and writes.
	add(typingOwed, "the storefront and checkout surface (commercestore.Route). "+moduleHandler,
		"DELETE /v1/commerce/store/{storeid}",
		"DELETE /v1/commerce/store/{storeid}/listing/{key}",
		"GET /v1/commerce/store/",
		"GET /v1/commerce/store/access",
		"GET /v1/commerce/store/current",
		"GET /v1/commerce/store/{storeid}",
		"GET /v1/commerce/store/{storeid}/bundle/{key}",
		"GET /v1/commerce/store/{storeid}/listing",
		"GET /v1/commerce/store/{storeid}/listing/{key}",
		"GET /v1/commerce/store/{storeid}/product/{key}",
		"GET /v1/commerce/store/{storeid}/variant/{key}",
		"PATCH /v1/commerce/store/{storeid}",
		"PATCH /v1/commerce/store/{storeid}/listing/{key}",
		"POST /v1/commerce/store/",
		"POST /v1/commerce/store/token",
		"POST /v1/commerce/store/{storeid}",
		"POST /v1/commerce/store/{storeid}/authorize",
		"POST /v1/commerce/store/{storeid}/authorize/{orderid}",
		"POST /v1/commerce/store/{storeid}/capture/{orderid}",
		"POST /v1/commerce/store/{storeid}/charge",
		"POST /v1/commerce/store/{storeid}/checkout/authorize",
		"POST /v1/commerce/store/{storeid}/checkout/authorize/{orderid}",
		"POST /v1/commerce/store/{storeid}/checkout/capture/{orderid}",
		"POST /v1/commerce/store/{storeid}/checkout/charge",
		"POST /v1/commerce/store/{storeid}/checkout/paypal/cancel/{payKey}",
		"POST /v1/commerce/store/{storeid}/checkout/paypal/confirm/{payKey}",
		"POST /v1/commerce/store/{storeid}/checkout/paypal/pay",
		"POST /v1/commerce/store/{storeid}/listing/{key}",
		"POST /v1/commerce/store/{storeid}/paypal/cancel/{payKey}",
		"POST /v1/commerce/store/{storeid}/paypal/confirm/{payKey}",
		"POST /v1/commerce/store/{storeid}/paypal/pay",
		"POST /v1/commerce/store/{storeid}/trial",
		"PUT /v1/commerce/store/{storeid}",
		"PUT /v1/commerce/store/{storeid}/listing/{key}",
	)

	// catalogapi.AdminRoute (mount.go) — the platform-admin product CMS, minus the
	// two wildcard verbs, which are in untypedByDesign for a reason of their own.
	add(typingOwed, "the platform-admin catalog CMS (catalogapi.AdminRoute). "+moduleHandler,
		"GET /v1/commerce/catalog/entries",
		"POST /v1/commerce/catalog/entries",
		"POST /v1/commerce/catalog/models",
		"POST /v1/commerce/catalog/models/refresh",
		"POST /v1/commerce/catalog/seed",
	)

	// planapi.AdminRoute (mount.go) — the subscription plan authority.
	add(typingOwed, "the plan authority CRUD (planapi.AdminRoute). "+moduleHandler,
		"DELETE /v1/commerce/plans/entries/{slug}",
		"GET /v1/commerce/plans/entries",
		"POST /v1/commerce/plans/entries",
		"POST /v1/commerce/plans/seed",
		"PUT /v1/commerce/plans/entries/{slug}",
	)

	// rateapi.AdminRoute (mount.go) — what one unit of each metered thing costs.
	// Its gate is the sharpest instance of the shared blocker: every handler asks
	// iammiddleware.GetIAMClaims(c).IsSuperAdmin() off the REQUEST
	// (api/rate/handlers.go:52-58), which is a different predicate and a different
	// source from cloud's principal — so a cloud-side rewrite would move the
	// authorization on a CROSS-TENANT money authority.
	add(typingOwed, "the rate authority CRUD (rateapi.AdminRoute). "+moduleHandler,
		"DELETE /v1/commerce/rates/entries/{product}/{meter}",
		"GET /v1/commerce/rates/entries",
		"POST /v1/commerce/rates/entries",
		"POST /v1/commerce/rates/import",
		"PUT /v1/commerce/rates/entries/{product}/{meter}",
	)

	// commercemod.Embed's own setupRoutes (mount.go) — the five the MODULE binds
	// on the host app itself. These are the hardest of the six: cloud does not
	// even name their registrar, so retiring them is not a line this repo can
	// delete.
	add(typingOwed, "bound by hanzoai/commerce's own setupRoutes inside commercemod.Embed, which this "+
		"app calls as one unit. "+moduleHandler,
		"GET /v1/commerce/admin/catalog",
		"GET /v1/commerce/catalog",
		"GET /v1/commerce/currencies",
		"GET /v1/commerce/deposits",
		"GET /v1/commerce/org",
	)
}

// owed builds a ledger; add extends one. Both exist so a family states its reason
// once instead of once per address — 173 copies of one sentence is 173 chances for
// one of them to stop being true unnoticed.
func owed(why string, ops ...string) map[string]string {
	m := make(map[string]string, len(ops))
	add(m, why, ops...)
	return m
}

func add(m map[string]string, why string, ops ...string) {
	for _, op := range ops {
		if _, dup := m[op]; dup {
			panic("typingOwed names " + op + " twice")
		}
		m[op] = why
	}
}

// ---- the gates ----

// TestEveryRouteIsTypedOrNamed fails three ways, and each is a different defect.
//
//   - a served operation that is neither typed nor named: the next route added
//     here — or upstream, inside a bundle nobody in this repo enumerates — is
//     typed by default, and dropping one out of the registry takes a deliberate
//     edit carrying a reason;
//   - an entry naming an address commerce no longer serves: a reason cannot
//     outlive the route it explains;
//   - an entry naming an address that IS a typed op: a conversion that landed
//     without its ledger line being deleted, which is how a finished package goes
//     on reading as blocked.
//
// It was proven to bite in all three directions before it was committed.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := commerceOps(t)

	var unclassified []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		if _, named := typingOwed[key]; named {
			continue
		}
		unclassified = append(unclassified, key)
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Errorf("operation(s) with no registry entry and no reason:\n\t%s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/… in this package), or name it in untypedByDesign with "+
			"the wire fact that typing it would move, or in typingOwed with the blocker and who owns it.",
			strings.Join(unclassified, "\n\t"))
	}

	for _, ledger := range []struct {
		name string
		m    map[string]string
	}{{"untypedByDesign", untypedByDesign}, {"typingOwed", typingOwed}} {
		for key := range ledger.m {
			if !served[key] {
				t.Errorf("%s names %q, which commerce no longer serves — a reason cannot outlive "+
					"the route it explains", ledger.name, key)
			}
			if _, ok := typed[key]; ok {
				t.Errorf("%s names %q, which IS a typed op — delete the entry in the same change "+
					"that converted it", ledger.name, key)
			}
		}
	}
}

// TestTheLedgersSumToTheServedSurface is the arithmetic the per-key checks imply
// and nothing else states: three disjoint sets whose union IS what this app
// serves. It also RATCHETS — typed may only rise and owed may only fall — which is
// the property a count can enforce and a name cannot, and it is what makes the
// next agent's progress visible in one number.
func TestTheLedgersSumToTheServedSurface(t *testing.T) {
	served, typed := commerceOps(t)

	sum := len(typed) + len(untypedByDesign) + len(typingOwed)
	if sum != len(served) {
		t.Errorf("typed %d + untypedByDesign %d + typingOwed %d = %d, but commerce serves %d — "+
			"the three sets must partition the served surface",
			len(typed), len(untypedByDesign), len(typingOwed), sum, len(served))
	}
	t.Logf("commerce: %d served = %d typed + %d cannot + %d owed",
		len(served), len(typed), len(untypedByDesign), len(typingOwed))

	// The seed, measured on the router this file reads. Lowering the owed floor is
	// automatic on a conversion; RAISING it is a hand edit in the same commit,
	// where a reviewer sees the number go up next to its reason.
	//
	// typedFloor went 7 → 5 for a DELETION, which is the one reason it may fall.
	// takePayment and getPayment were a second public address onto the card money
	// move the browser top-up already reached — one act, two operation ids, two MCP
	// tools — and they were retired rather than kept in step. The receipt read they
	// carried is now the member of the collection that already existed,
	// GET /v1/billing/transactions/{id}, so no capability went with them.
	const (
		typedFloor = 5
		owedCeil   = 173
	)
	if len(typed) < typedFloor {
		t.Errorf("typed ops fell to %d, below the floor of %d — a typed op was removed or stopped "+
			"registering; a surface does not un-convert by accident", len(typed), typedFloor)
	}
	if len(typingOwed) > owedCeil {
		t.Errorf("typingOwed grew to %d, above the ceiling of %d — a raw route was added rather "+
			"than converted. Raise the ceiling only with the reason beside it", len(typingOwed), owedCeil)
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS product surface: it becomes the OpenAPI description AND the
// MCP tool description a model reads to pick the tool. zipdoc_gen.go is what
// carries it into the binary, so an op added without regenerating shows up here as
// a nameless tool rather than as a missing one.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := commerceOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed commerce ops in the registry at all — the gate below would pass vacuously")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: "+
				"GOOS= GOARCH= GOWORK=off go generate -run zipdoc ./apps/commerce/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed gates the RESPONSE side, which the operation
// gate above cannot see: an app can be fully typed and still publish a wholly
// undescribed shape, because op prose and FIELD prose are lifted from different
// comments. On a money surface the difference is what a reader can act on — that
// `arr` is an integer is visible from the schema; that it is CENTS is not.
//
// It walks the MARSHALLED document through openapi.Bare, the one walker, which
// descends into nested shapes, array items, additionalProperties and every
// alternative of allOf/anyOf/oneOf — so an inline object inside a property cannot
// ship bare while a top-level count reads clean.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := surfaceApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "commerce", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(doc.Components.Schemas) == 0 {
		t.Fatal("this subset publishes no schema at all, so this gate measured nothing")
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published propert(ies) carry no description:\n\t%s\n"+
			"Each reaches openapi.yaml, every generated SDK and every MCP inputSchema bare. Write the "+
			"comment on the field of the declaring type. Say the UNITS, the sign convention, the closed "+
			"vocabulary and what ABSENCE means — a description that restates the field's name is worse "+
			"than none.", len(bare), strings.Join(bare, "\n\t"))
	}
}
