package billing

// record.go answers POST /v1/billing/usage: an APPLICATION reporting usage it
// metered, debited from the org it acts for.
//
// It is the HTTP door onto the one debit the platform already has. The act lands
// through cloud.Meter.Debit, which is metering.Client.Record — the same call the
// in-process meters make, reaching the same ledger whether it sits in this process
// or one socket away — so a usage row written here is indistinguishable from one
// the edge gate wrote, and there is no second ledger to reconcile.
//
// WHO MAY CALL IT. An application acting as itself: an IAM client_credentials
// token, which the identity boundary marks X-User-IsApp. A person may not report
// their own usage, and an sk- key is a person's credential, so both are refused.
//
// WHOSE MONEY. The org the token acts in and nothing else: the application's own
// organization, or an organization that granted the application membership, which
// IAM signs into the token's `orgs` and the caller selects with X-Org-Id. The body
// NAMES that org, and a body naming any other is refused before anything is
// debited — a selection the token does not carry falls back to the application's
// own org at the boundary, and the one place that fallback must not be silent is a
// charge. The wallet inside the org is resolved exactly as GET /v1/billing/balance
// resolves it for the same caller, so the balance a service gates on and the
// account it debits are one account.
//
// EXACTLY ONCE. The caller names the act (`id`) and the ledger keys the debit on
// that name, scoped to the reporting application: a retry of one act moves the
// money once and answers the same receipt, and the same name spent on a different
// amount is refused as a conflict rather than deduped. The name is the reporting
// application's, never the payer's — the application is the merchant stating what
// it did, which is why an idempotency key is safe to take from it here and is not
// taken from a person anywhere else.

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	credit "github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// usageReport is one metered act an application reports.
type usageReport struct {
	// ID names the act, chosen by the reporting application and stable across its
	// retries: the ledger debits one act once. At most 128 printable characters,
	// no whitespace. Reusing an ID for a different amount is a 409.
	ID string `json:"id" url:"-"`
	// Org is the organization this usage is billed to. It must be the org the
	// token acts in — the application's own, or one that granted it membership and
	// is selected with X-Org-Id — and any other is refused before money moves.
	Org string `json:"org" url:"-"`
	// Amount is what the act costs, as an exact decimal in USD. It must be
	// greater than zero.
	Amount client.Money `json:"amount" url:"-"`
	// Service scopes the debit for spend caps and attributes it to a product, as
	// a lowercase slug. Empty takes the reporting application's name.
	Service string `json:"service,omitempty" url:"-"`
	// Model names the unit the amount prices (a machine size, a model id), and is
	// recorded with the debit.
	Model string `json:"model,omitempty" url:"-"`
	// Project attributes the debit to one project of the org. Empty is the org's
	// default project.
	Project string `json:"project,omitempty" url:"-"`
}

// usageReceipt is what a recorded act answers. A retry of the same act answers
// the same receipt.
type usageReceipt struct {
	// ID is the act's name, echoed.
	ID string `json:"id"`
	// Org is the organization whose ledger holds the debit.
	Org string `json:"org"`
	// Account is the wallet the debit drew from, the same key GET
	// /v1/billing/balance reports for this caller.
	Account string `json:"account"`
	// Amount is the debit, exactly as recorded.
	Amount client.Money `json:"amount"`
}

// Bounds on the caller-named strings. Each is a key or a label the ledger and
// the measurements keep, so an unbounded one is unbounded storage.
const (
	maxActID  = 128
	maxLabel  = 128
	usdCode   = "usd"
	usageRefs = "usage"
)

// slug is the shape a service name takes: it becomes a spend-cap axis and a
// measurement label, so it is a closed alphabet.
var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// RecordUsage debits one act an application metered to the org it acts for, and
// answers the receipt.
//
// The caller is an application acting as itself — an IAM client_credentials
// token — and the org is the one that token acts in: the application's own, or
// one that granted it membership, selected with X-Org-Id and named again in
// `org`. A person, an API key, an unauthenticated caller, and a body naming an
// org the token does not act in are all refused before anything is debited.
//
// The debit lands in the same ledger every other meter writes, in the wallet GET
// /v1/billing/balance reports for the same caller. It is exactly-once on `id`: a
// retry answers the same receipt, and the same id for a different amount is 409.
// Recording does not gate — the work already happened — so a caller that must
// refuse unfunded work asks GET /v1/billing/balance and GET
// /v1/billing/alerts/authorize first.
func (o ops) recordUsage(ctx context.Context, in *usageReport) (*usageReceipt, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, zip.ErrUnauthorized("an application identity is required to record usage")
	}
	if !principal.IsApp(c) {
		return nil, zip.ErrForbidden("usage is recorded by an application acting as itself (IAM client_credentials)")
	}
	if in.Org != org {
		return nil, zip.ErrForbidden("this token acts for " + org + ", not " + in.Org +
			": an organization is billed only by an application it granted membership, selected with X-Org-Id")
	}
	amount, err := in.valid()
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	payer := meter.Payer(c)
	_, name, _ := strings.Cut(c.User(), "/")
	app := cmp.Or(strings.TrimSpace(c.Header("X-User-Name")), name)
	err = o.s.Bill.Debit(ctx, payer, meter.Usage{
		Amount:   amount.Unwrap(),
		Currency: usdCode,
		Provider: app,
		Service:  cmp.Or(in.Service, app),
		Model:    in.Model,
		Project:  in.Project,
		Actor:    c.User(),
		// The act's name, scoped to the application that named it, so two
		// applications reporting into one wallet cannot spend each other's names.
		Ref:       usageRefs + ":" + c.User() + ":" + in.ID,
		RequestID: c.Header("X-Request-Id"),
	})
	if err != nil {
		return nil, debitRefusal(err)
	}
	return &usageReceipt{ID: in.ID, Org: org, Account: payer.Subject(), Amount: client.Amount(amount.Unwrap())}, nil
}

// valid checks the report's own fields and returns its amount.
func (in *usageReport) valid() (credit.Amount, error) {
	if in.ID == "" || len(in.ID) > maxActID || !printable(in.ID) {
		return credit.Amount{}, errors.New("id is required: at most 128 printable characters, no whitespace")
	}
	if in.Service != "" && !slug.MatchString(in.Service) {
		return credit.Amount{}, errors.New("service must be a lowercase slug of at most 64 characters")
	}
	if len(in.Model) > maxLabel || strings.ContainsFunc(in.Model, isControl) {
		return credit.Amount{}, errors.New("model must be at most 128 characters with no control characters")
	}
	if len(in.Project) > maxLabel || (in.Project != "" && !printable(in.Project)) {
		return credit.Amount{}, errors.New("project must be at most 128 printable characters, no whitespace")
	}
	if !strings.EqualFold(in.Amount.Currency, usdCode) {
		return credit.Amount{}, errors.New("amount.currency must be usd")
	}
	amount, err := credit.ParseUSD(in.Amount.Decimal)
	if err != nil {
		return credit.Amount{}, errors.New("amount: " + err.Error())
	}
	if amount.Sign() <= 0 {
		return credit.Amount{}, errors.New("amount must be greater than zero")
	}
	return amount, nil
}

// debitRefusal answers a failed debit with the status its cause names: a name
// already spent on another amount is the caller's conflict to resolve, a ledger
// the process cannot reach is the operator's, and a refusal the ledger chose
// keeps its own status.
func debitRefusal(err error) error {
	switch {
	case errors.Is(err, finance.ErrRefReused):
		return zip.Errorf(http.StatusConflict, "usage id already records a different amount")
	case errors.Is(err, cloud.ErrNoMeter):
		return zip.Errorf(http.StatusServiceUnavailable, "this deployment runs no ledger")
	case errors.Is(err, cloud.ErrNoLedger):
		return zip.ErrForbidden("the caller resolves to no account to debit")
	}
	if he, ok := errors.AsType[*zip.HTTPError](err); ok {
		return he
	}
	return zip.Errorf(http.StatusBadGateway, "usage not recorded: ledger unavailable")
}

// printable reports whether s is visible ASCII: no whitespace, no control bytes,
// nothing a key or a path could be confused by.
func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
