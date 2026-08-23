// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// TAKING A PAYMENT, as a typed op — the agent's door onto the same card rail the
// console's "add credits" button uses.
//
// Money could already be taken here: commerce's TopupWithToken charges a Square
// token and credits the ledger, and mount.go registers it at
// POST /v1/billing/topup/token. What could not happen is an AGENT taking a
// payment, and the reason was not authorization or rails — it was SHAPE. That
// route is a raw func(*zip.Ctx) error, so it is a route and nothing else: no
// registry entry, and therefore no OpenAPI operation, no MCP tool, no SDK method,
// no CLI command. `tools/list` at api.hanzo.ai answered 570 tools and not one of
// them could take a payment, which is why the demo stopped here.
//
// The fix is NOT a second charge path. commerce's money move now lives as a core
// that takes values instead of a request (billing.TakePayment), so the browser
// route and the two ops below are three doors onto ONE implementation of bounds,
// idempotency, processor selection, charge and ledger credit. A second
// implementation would be a second set of bounds to drift and a second
// idempotency derivation to disagree — which is a double charge waiting for the
// right retry.
//
// IDENTITY IS NOT AN INPUT. The paying org is read from the validated principal
// cloud.Bridge parked on the context, never from a field: an In field is
// caller-supplied, so an org read from one is a cross-tenant write the caller
// asserted for itself. There is deliberately no subject field either — a payment
// taken here credits the CALLER'S OWN org account, so there is no value a caller
// could send that steers the money somewhere else.
//
// WHICH MODE IT RUNS IN is the org's, not the caller's. Square sandbox vs
// production follows the org's KMS-hydrated credentials and org.TestMode(), and
// the answer states which bucket it credited (`test`). A caller that could ask
// for a test charge could mint spendable balance from a sandbox card, so it
// cannot ask.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/models/organization"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"
)

// PaymentIn is a card payment to take. It carries WHAT to charge and nothing
// about WHO is paying — see the package note.
type PaymentIn struct {
	// SourceID is the single-use payment token that stands in for the card: a
	// Square Web Payments SDK nonce minted in the browser, or a Square sandbox
	// test nonce when the org's credentials are sandbox ones. The card number
	// itself never reaches this process, which is what keeps it out of PCI scope.
	SourceID string `json:"sourceId"`
	// AmountCents is the amount to charge, in whole cents (5000 is $50.00).
	// Server-side bounds apply and are authoritative — the default floor is $1
	// and the ceiling $5,000, so a fat-fingered or hostile amount is refused
	// before any money moves.
	AmountCents int64 `json:"amountCents"`
	// Currency is the ISO 4217 code, lower-cased. Empty means usd.
	Currency string `json:"currency,omitempty"`
	// IdempotencyKey makes a retry safe: the same key never charges twice, it
	// replays the first result. Sending one is strongly recommended for an agent,
	// which retries by construction. Empty falls back to a windowed key derived
	// from the amount and currency, so a double-submit inside 15 minutes still
	// collapses onto one charge.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// PaymentOut is the settled payment — the receipt.
type PaymentOut struct {
	// ID is the ledger transaction id for the credit. It is what getPayment
	// reads back, and the customer-visible receipt for the money.
	ID string `json:"id"`
	// BalanceCents is the org's balance AFTER this payment, read back from the
	// same key just credited so it matches what the balance endpoint reports.
	BalanceCents int64 `json:"balanceCents"`
	// Status is "ok" on a settled charge. A charge that did not settle is an
	// error with the processor's reason, never a status field to inspect.
	Status string `json:"status"`
	// ProcessorRef is the payment processor's own reference for the charge
	// (Square's payment id). It is the field that proves money actually moved at
	// the gateway rather than only in our ledger — the thing to quote when
	// reconciling against a processor dashboard.
	ProcessorRef string `json:"processorRef,omitempty"`
	// Test reports which bucket this credited: true is a SANDBOX charge crediting
	// the test balance, false is live money. It is always stated so a receipt can
	// never be mistaken for the other kind.
	Test bool `json:"test"`
}

// PaymentRef names one payment to read.
type PaymentRef struct {
	// ID is the ledger transaction id a payment returned.
	ID string `json:"id"`
}

// PaymentRecord is a payment as the ledger holds it.
type PaymentRecord struct {
	// ID is the ledger transaction id.
	ID string `json:"id"`
	// Subject is the billing key this payment credited.
	Subject string `json:"subject"`
	// AmountCents is the credited amount in whole cents.
	AmountCents int64 `json:"amountCents"`
	// Currency is the ISO 4217 code.
	Currency string `json:"currency"`
	// Status is the payment's state. This ledger writes a deposit only AFTER the
	// processor settled, so a payment that can be read is one that succeeded.
	Status string `json:"status"`
	// Test reports whether this was a sandbox charge (test balance) or live money.
	Test bool `json:"test"`
	// Notes is the ledger memo, carrying the processor and its reference.
	Notes string `json:"notes,omitempty"`
	// CreatedAt is when the credit was written, RFC3339.
	CreatedAt string `json:"createdAt,omitempty"`
}

// paymentOps binds the payment ops to the subsystem. A zip.TypedHandler is
// func(context.Context, *In) (*Out, error) — there is no parameter for the
// subsystem — so every op is a method value, which is also the only bound form
// cmd/zipdoc can lift prose from.
type paymentOps struct{}

// exposePayments publishes the payment surface. Mount calls it.
//
// Neither op is registered on a group at its own address with an empty leaf: joining
// a "/v1/commerce/payments" group with "" yields "/v1/commerce/payments/", a different route from the
// one an agent will call. The read is therefore declared on the APP with its whole
// path, and the write on a "/v1" group carrying the screen (below) — same two
// addresses either way. The app-wide cloud.Bridge (serve.go) is what parks the
// validated org these ops read, so neither needs group middleware to have an identity.
//
// THE SCREEN IS A PARAMETER, and it is the one thing about this surface that could
// not be decided here. `take` runs the same credit-minting core as the browser's card
// top-up, so it is a CREDIT DOOR and has to be screened by the same risk gate that
// door is — but which gate that is belongs to the composition root, beside the other
// registration, where both are visible as one decision (mount.go, risk.go). Taking it
// as an argument is also what keeps this file from importing the risk seam to fetch a
// gate it does not own.
//
// AND IT IS COMPOSED ONTO THE HANDLER, which is the only place it reaches the whole
// op. zip records a typed op ONCE and projects it four ways — this REST route, the
// `takePayment` MCP tool, the by-name call plane and the CLI — and all four dispatch
// to the op's HANDLER. Middleware composed at registration (zip.With) wraps the fiber
// handler REST is served through and nothing else, so the screen guarded the URL while
// the tool an agent already holds ran the money core unscreened. `o.take(screen)`
// BUILDS the screened handler, so the screen is inside the one thing every projection
// invokes.
//
// IT IS ON THE WRITE AND NOT ON THE READ. `take` moves money; `get` reads a receipt
// out of the caller's own ledger namespace and mints nothing, so screening it would
// spend a scorer round trip — and, when a scorer is present and mute, REFUSE a
// customer their own receipt — to protect a mint that is not there. A gate belongs on
// the act it can prevent.
//
// WHICH SUBJECT THE MONEY LANDS ON is the PAYER's wallet — [principal.Subject] over the
// payer's org, which is the address principal.WalletOf hands the spend gate — and it is
// recorded HERE, off the published handler prose, because it is a fact about our wiring
// rather than about the wire.
//
// The subject handed to commerce's core below is the org's POOL (org.Name, commerce's
// own orgBillingKey), and it still is: that names the row in commerce's OWN transaction
// store, which nothing in this binary spends from. For every org whose credential
// resolves to its pool the two are the same string; for the two populations where
// account.Payer resolves a PERSON — a member of the shared signup org, or a credential
// carrying a signed `person:` billing_account claim — they are not, and the wallet the
// AI spend gate reads is the person's. That is why the SPENDABLE credit is posted by
// the settlement, through the same payer rule the screen judges by (settle.go): one
// payment, one payer, one wallet, whichever door took it.
func exposePayments(app *zip.App, s screen) {
	o := paymentOps{}
	// BOTH OPS ARE DECLARED ON THE APP WITH THEIR WHOLE PATH, and the screen is in the
	// write's HANDLER rather than on a router the write is declared through.
	//
	// The `/v1` group this used to open existed only to give the screen somewhere to
	// ride that zipdoc could still read a prefix out of: zipdoc resolves a router's
	// prefix from an *zip.App or from an assigned `g := <router>.Group("…")` and can
	// read neither out of a With, so an inline `zip.Post(app.With(screen), …)` made it
	// refuse the op — and prose filed under a guessed path is silently dropped from the
	// document AND from the MCP tool, which for the one tool that takes money is the
	// tool becoming uncallable. With the screen off the router there is no router to
	// spell, so the group is gone and both ops read the same way.
	//
	// The prose still reaches the registry because `o.take` is now the BUILDER of the
	// handler rather than the handler: zipdoc reads a `zip.Post(app, p, build(dep))`
	// registration by taking the doc comment off `build`, which is where the operation
	// is described and where "a payment is RISK-SCREENED before the card is charged"
	// has always been written.
	zip.Post(app, "/v1/commerce/payments", o.take(s),
		zip.WithOperationID("takePayment"),
		zip.WithSummary("Take a card payment and credit the org's balance"),
		zip.WithTags("payments"),
		zip.WithStatus(http.StatusCreated))
	zip.Get(app, "/v1/commerce/payments/:id", o.get,
		zip.WithOperationID("getPayment"),
		zip.WithSummary("Read one settled payment by its id"),
		zip.WithTags("payments"))
}

// Takes a payment: charges a single-use card token and credits the caller's org
// balance, exactly once.
//
// This is the operation behind "collect money from a customer". It runs the SAME
// core the console's card top-up runs (commerce billing.TakePayment), so the
// server-side amount bounds, the idempotency guard and the ledger credit are
// shared rather than reimplemented — a second charge path would eventually
// double-charge somebody.
//
// The ORG is the caller's, taken from the validated principal and never from the
// input, so a payment can only ever credit the account of whoever made the call.
//
// A payment is RISK-SCREENED before the card is charged, so this can be refused
// without any money moving: 403 means the screen did not authorise it, and 503 means
// the screen could not reach a decision — that one is worth retrying, and no charge
// was attempted either way.
//
// Send an idempotencyKey. An agent retries by construction, and the key is what
// turns a retry into a replay of the first receipt instead of a second charge.
//
// The answer states whether it settled in SANDBOX or live mode (`test`), and
// carries the processor's own reference (`processorRef`) so the charge can be
// reconciled against the processor rather than taken on trust.
//
// A named builder, not a closure, so zipdoc can lift this prose into the registry.
//
// It BUILDS the handler rather than being it, because the screen has to sit inside
// the value every projection of this op dispatches to — see exposePayments. `charge`
// is the money move, `take` is the screened door onto it, and the only registrable
// one is the second.
func (o paymentOps) take(s screen) zip.TypedHandler[PaymentIn, PaymentOut] {
	return s.op(o.charge)
}

// charge is the money move `take` screens: commerce's ONE card core, projected onto
// this op's types. It is deliberately registered nowhere — the registered handler is
// the screened one — so no address in this binary reaches it directly.
func (paymentOps) charge(ctx context.Context, in *PaymentIn) (*PaymentOut, error) {
	org, err := payingOrg(ctx, "take payment")
	if err != nil {
		return nil, err
	}
	out, f := commercebilling.TakePayment(ctx, org, commercebilling.TakePaymentIn{
		SourceID:       strings.TrimSpace(in.SourceID),
		AmountCents:    in.AmountCents,
		Currency:       in.Currency,
		Subject:        strings.ToLower(strings.TrimSpace(org.Name)),
		IdempotencyKey: strings.TrimSpace(in.IdempotencyKey),
	})
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return &PaymentOut{
		ID:           out.TransactionID,
		BalanceCents: out.BalanceCents,
		Status:       out.Status,
		ProcessorRef: out.ProcessorRef,
		Test:         out.Test,
	}, nil
}

// Reads one settled payment out of the caller's org ledger.
//
// The org scopes the read by construction — the ledger is namespaced to it — so
// an id belonging to another tenant is simply not found rather than found and
// then filtered. A ledger row that is not a payment is likewise not found, so
// this cannot be used to walk the org's usage debits.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (paymentOps) get(ctx context.Context, in *PaymentRef) (*PaymentRecord, error) {
	org, err := payingOrg(ctx, "read payment")
	if err != nil {
		return nil, err
	}
	rec, f := commercebilling.ReadPayment(ctx, org, in.ID)
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return &PaymentRecord{
		ID:          rec.ID,
		Subject:     rec.Subject,
		AmountCents: rec.AmountCents,
		Currency:    rec.Currency,
		Status:      rec.Status,
		Test:        rec.Test,
		Notes:       rec.Notes,
		CreatedAt:   rec.CreatedAt,
	}, nil
}

// payingOrg resolves the caller's org to the commerce organization the money
// core needs, through commerce's OWN canonical resolver — the same binding the
// balance read and the browser charge use, so a payment can never land in a
// namespace a read would not look in.
//
// It refuses rather than defaulting. An unvalidated caller has no org, and the
// honest answer to "whose card is this and whose balance goes up" is nobody, not
// the platform's.
func payingOrg(ctx context.Context, op string) (*organization.Organization, error) {
	name, ok := principal.OrgFrom(ctx)
	if !ok {
		// A PLANE op is reached with a stated caller and NO request behind it —
		// plane.For sets zip.Caller{Org} and nothing else — so OrgFrom refuses
		// here by construction: it composes validated-ness AND an org, and there
		// is no user on this context to be validated.
		//
		// callerOrg is the rule the other plane ops in this package already read
		// by, and it is the one plane.Ask documents: the org rides the CALLER,
		// forwarded from the gateway's assertion or stated once and explicitly,
		// never an argument. Nothing is widened by reading it — the org stated
		// here is the one billing's door already resolved and validated before it
		// called, so this reads that decision rather than making a second one.
		orgName, err := callerOrg(ctx, op)
		if err != nil {
			return nil, err
		}
		name = orgName
	}
	// Commerce must be co-resident for its ledger to be writable in-process. A
	// missing embed is an ERROR, never a silent no-op: money that quietly did not
	// move is worse than money that loudly refused to.
	if e := currentEmbedded(); e == nil || e.App() == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"%s: commerce is not co-resident in this process", op)
	}
	org, err := commerceorg.Resolve(ctx, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%s: resolve org %q: %v", op, name, err)
	}
	return org, nil
}

// paymentsPrefix is the subtree the payment ops own. It is declared in Prefixes
// (mount.go) and, by hand, in manifest/apps.go — the light host must not import
// an app package, so the two copies are kept equal deliberately.
const paymentsPrefix = "/v1/commerce/payments"

var _ = cloud.Bridge // payments read identity through the app-wide bridge
