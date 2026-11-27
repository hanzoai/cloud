// Package ledger is the production ledger of a Hanzo deployment: the prepaid wallet in
// this process when commerce is co-resident, commerce over HTTP when it is standalone,
// and the process that owns the books over the internal plane otherwise.
//
// sdk/meter names the contract (meter.Ledger) and links none of this; the host links
// this package and it registers itself from init, as the KV and pubsub drivers do for
// sdk/coord. The debit of the prepaid wallet is here and nowhere in the SDK.
package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/plane"
)

func init() { meter.Register(open) }

// books is the ledger of a deployment that bills. It is safe for concurrent use.
type books struct {
	baseURL string
	http    meter.Doer
}

// open builds the ledger from cfg. It returns an error only for an unparseable
// BaseURL; an empty BaseURL is valid (the books are then asked over the plane).
func open(cfg meter.Config) (meter.Ledger, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base != "" {
		if _, err := url.Parse(base); err != nil {
			return nil, fmt.Errorf("metering: invalid BaseURL %q: %w", cfg.BaseURL, err)
		}
	}

	doer := cfg.HTTPClient
	if doer == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		doer = &http.Client{Timeout: timeout}
	}

	return &books{baseURL: base, http: doer}, nil
}

// Enabled reports whether the ledger answers from THIS process — a commerce
// BaseURL is configured, which cloud sets whenever commerce is co-resident. False
// means the ledger lives elsewhere, not that nothing bills: the gate and the debit
// then cross the internal plane to the process that owns it, and only
// [plane.ErrNoPeer] says no such process exists.
func (c *books) Enabled() bool { return c != nil && c.baseURL != "" }

// chargeOf is the exact charge a gate weighs, in the ledger's credit unit.
func chargeOf(in meter.AuthInput) money.Amount { return money.FromDecimal(in.Money().Decimal()) }

// moneyOf is the exact debit a usage carries, in the ledger's credit unit.
func moneyOf(u meter.Usage) money.Amount { return money.FromDecimal(u.Money().Decimal()) }

// Canonical commerce billing READ (mounted under /v1). Keep in lockstep with
// commerce/api/billing/handlers.go — a wrong prefix 404s and, fail-closed,
// denies every request.
//
// It is the one read a standalone commerce still answers over HTTP. The spend
// cap is not one of them: its verdict is asked by name over the internal plane
// ([Client.scopeAuthorize]), and so is the WRITE — the usage debit — because the
// act's name is `json:"-"` and could not survive a JSON body; see [Client.Record].
const pathBalance = "/v1/billing/balance"

// Header carrying the tenant org slug for commerce namespace resolution.
// Commerce reads the org from X-Org-Id; without it commerce falls back to
// "hanzo" — which would silently debit the wrong tenant.
const headerOrg = "X-Org-Id"

// capAuthorizeTimeout HARD-bounds the per-scope spend-cap check, so a slow, broken or
// hot-looping commerce can never hang the completion path (the SEV1 a legacy-org
// GetById hot-loop caused). A check that runs out of time has not said the request
// fits under the cap, so it is answered as an unreadable cap: refused under the
// fail-closed posture. Long enough that a busy single-writer ledger is not refused for
// queueing; a healthy check answers in tens of milliseconds.
const capAuthorizeTimeout = 5 * time.Second

// peerTimeout bounds one crossing to the process that owns the ledger — the gate
// and the debit alike. Long enough for a per-org SQLite writer under lock
// contention, short enough that an unwell biller does not become this caller's
// latency.
const peerTimeout = 10 * time.Second

// AuthorizeVerdict is the full pre-request gate: it checks FUNDS first and, only
// when funded, the per-scope SPEND CAP verdict. Both reads take the client's ONE
// fail posture: a cap that could not be read is unknown exactly as a balance that
// could not be read is, and is refused under fail-closed (meter.ErrSpendCapUnknown).
//
// What allows without a cap is an answer that there is none: commerce reports allow
// for an org with no covering cap row without reading any spend, and a deployment
// with no commerce at all (plane.ErrNoPeer) holds no caps. An unreachable or slow
// commerce, an enforced cap whose spend cannot be read, or a void reply is not that
// answer.
//
// The returned WarnPct (>0 when at/over a covering cap's soft threshold) lets the
// caller emit X-Spend-Warn from this one round trip.
func (c *books) AuthorizeVerdict(ctx context.Context, in meter.AuthInput) (meter.Verdict, error) {
	// WHO IS ASKING, decided above the topology. Identity is a fact about the
	// caller, not about where the ledger happens to live, so an unattributable
	// request is refused the same on both deployments.
	user := strings.TrimSpace(in.User)
	org := strings.TrimSpace(in.Org)
	// The ledger is not in this process. Ask the one that holds it.
	if !c.Enabled() {
		return c.authorizePeer(ctx, in)
	}

	available, err := c.Balance(ctx, user, org, in.Currency)
	if err != nil {
		return meter.Verdict{}, err // unknown -> deny.
	}
	funded := available > 0
	if want := chargeOf(in); !want.IsZero() {
		// Weigh both sides in the exact domain. Comparing a cents-rounded charge
		// against a cents balance let a sub-cent price round to zero and fall back
		// to the bare "any positive balance" gate — a charge authorized against a
		// figure nobody was going to spend.
		funded = money.FromCents(available).Cmp(want) >= 0
	}
	if !funded {
		return meter.Verdict{Allow: false, Reason: "insufficient_balance"}, nil
	}

	// Funded — the per-scope spend cap, under the same fail posture as the funds.
	sv, serr := c.scopeAuthorize(ctx, in)
	if serr != nil {
		serr = fmt.Errorf("%w: %w", meter.ErrSpendCapUnknown, serr)
		if meter.OnCapError != nil {
			meter.OnCapError(serr)
		}
		return c.unknown(serr)
	}
	if !sv.Allow && sv.Reason == "spend_cap" {
		return meter.Verdict{Allow: false, Reason: "spend_cap", CapCents: sv.CapCents, SpentCents: sv.SpentCents}, nil
	}
	return meter.Verdict{Allow: true, WarnPct: sv.WarnPct}, nil
}

// authorizePeer asks the process that owns the ledger whether this act may run.
//
// [plane.ErrNoPeer] is the ONE answer that means nobody bills in this deployment,
// and it is the only one that may be read as permission. Every other failure is a
// biller that is there and did not answer — unknown, which takes the client's fail
// posture exactly as the funds read does. Reading the two as one fact is how a
// priced act goes free the moment its app is split into its own binary.
func (c *books) authorizePeer(ctx context.Context, in meter.AuthInput) (meter.Verdict, error) {
	ctx, cancel := context.WithTimeout(plane.For(ctx, in.Org), peerTimeout)
	defer cancel()
	v, err := commerce.FinanceAuthorize(ctx, &client.AuthorizeIn{
		Subject:          in.User,
		Amount:           client.Amount(chargeOf(in).Unwrap()),
		Project:          in.Project,
		Service:          in.Service,
		ProjectValidated: in.ProjectValidated,
	})
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return meter.Verdict{Allow: true}, nil
	case err != nil:
		return c.unknown(fmt.Errorf("metering: commerce unreachable: %w", err))
	case v == nil:
		// A void reply from a gate is not permission. Nothing said yes.
		return c.unknown(fmt.Errorf("metering: commerce answered nothing"))
	case v.OK:
		return meter.Verdict{Allow: true}, nil
	case v.NoFunds:
		return meter.Verdict{Allow: false, Reason: "insufficient_balance"}, nil
	case v.CapSpent:
		return meter.Verdict{Allow: false, Reason: "spend_cap"}, nil
	case v.CapUnread:
		return c.unknown(fmt.Errorf("%w: %s", meter.ErrSpendCapUnknown, v.Reason))
	default:
		return c.unknown(fmt.Errorf("metering: %s", v.Reason))
	}
}

// unknown is a verdict that could not be read: a refusal, never permission.
func (c *books) unknown(err error) (meter.Verdict, error) {
	return meter.Verdict{}, err
}

// scopeAuthorize asks commerce, BY NAME, whether this request fits under the caller's
// spend caps. The org rides the call (plane.For), so commerce resolves the caps in
// the caller's own namespace — a cap on org X can never gate org Y.
//
// It is a plane op and not an HTTP read because the HTTP address belongs to another
// process. GET /v1/billing/alerts/authorize is served by the billing app, which relays
// to this same op; the commerce process — where every gate in the fleet lands through
// authorizePeer — answered that path 404 through its in-process transport, and a 404
// read as "no cap" lifted every ceiling. Asked by name, the op runs here when this
// process is commerce (plane.Call) and over the socket when it is not.
//
// plane.ErrNoPeer is the one failure that is an answer: the deployment has no
// commerce, so no cap row exists anywhere to bind. Every other failure, a void reply,
// and a check that outruns capAuthorizeTimeout reach no verdict.
func (c *books) scopeAuthorize(ctx context.Context, in meter.AuthInput) (*client.CapVerdict, error) {
	ask := &client.CapIn{
		Project:          strings.TrimSpace(in.Project),
		Service:          strings.TrimSpace(in.Service),
		ProjectValidated: in.ProjectValidated,
	}
	if want := chargeOf(in); !want.IsZero() {
		// The cap surface speaks whole cents. Send the charge rounded UP, so a sub-cent
		// spend is never weighed against a cap as nothing.
		ask.Amount = want.CentsUp()
	}

	// HARD BOUND: the select returns to the caller even if the op's goroutine is stuck —
	// an unresponsive handler cannot be interrupted, so ctx cancellation alone would not
	// unblock it. A stuck goroutine is abandoned, never waited on.
	tctx, cancel := context.WithTimeout(plane.For(ctx, in.Org), capAuthorizeTimeout)
	defer cancel()
	type answer struct {
		v   *client.CapVerdict
		err error
	}
	done := make(chan answer, 1)
	go func() {
		v, err := commerce.BillingCapAuthorize(tctx, ask)
		done <- answer{v, err}
	}()
	select {
	case a := <-done:
		switch {
		case errors.Is(a.err, plane.ErrNoPeer):
			return &client.CapVerdict{Allow: true}, nil
		case a.err != nil:
			return nil, a.err
		case a.v == nil:
			return nil, errors.New("commerce answered no cap verdict")
		}
		return a.v, nil
	case <-tctx.Done():
		return nil, fmt.Errorf("cap check exceeded %s: %w", capAuthorizeTimeout, tctx.Err())
	}
}

// Balance is the subject's spendable prepaid balance in cents, inside org's books.
//
// It resolves where the ledger lives exactly as [Client.Record] does: this process when
// it owns the file, otherwise the process that does, over the plane. So the gate reads
// the same wallet the debit writes on both topologies, without opening a file it must
// not open and without a hop through the public edge — which validates a CUSTOMER
// credential and answers 401 to a service.
//
// IT ROUNDS DOWN. The ledger keeps eighteen decimals and this figure is weighed against
// a price, so rounding up admits a request the wallet cannot cover; the debit that
// follows is exact, so the difference lands as a negative balance nobody authorized.
// Nothing is billed from this number — it decides admission only.
func (c *books) Balance(ctx context.Context, subject, org, currency string) (int64, error) {
	cur := meter.CurrencyOr(currency)
	// Co-resident native wallet: a DIRECT ledger read, no HTTP. subject is the billing
	// subject, org the wallet namespace; finance derives the account, in the live books.
	if fin := finance.Current(); fin != nil {
		bal, err := fin.Balance(ctx, org, subject, cur, false)
		if err != nil {
			return 0, err
		}
		return bal.CentsDown(), nil
	}
	if !c.Enabled() {
		ctx, cancel := context.WithTimeout(plane.For(ctx, org), peerTimeout)
		defer cancel()
		bal, err := commerce.FinanceBalance(ctx, &client.BalanceIn{Subject: subject, Currency: cur})
		if err != nil {
			return 0, fmt.Errorf("metering: plane balance read: %w", err)
		}
		if bal == nil {
			// A void reply is not a balance. Answering zero would report a funded
			// account as broke and refuse every paid call it makes.
			return 0, fmt.Errorf("metering: commerce answered no balance")
		}
		return bal.Amount.FloorMinor()
	}
	q := url.Values{"user": {subject}, "currency": {cur}}
	body, err := c.get(ctx, pathBalance, q, org)
	if err != nil {
		return 0, err
	}
	var br balanceResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return 0, fmt.Errorf("metering: decode balance: %w", err)
	}
	return br.Available, nil
}

// Record writes a usage event to commerce: every served call is counted, a $0 one
// included, and a wallet is debited when the call is the wallet's to pay.
//
// A negative amount is refused. It is a no-op (nil, nil) only on a deployment that
// runs no commerce at all ([plane.ErrNoPeer]) — never merely because the ledger is
// in another process.
// Usage recording is deliberately decoupled from gating: the work already
// happened and must be recorded, so balance is NOT re-checked here — exactly as
// commerce's RecordUsage documents.
//
// Provider is the service name doing the metering when no model/provider is
// natural (e.g. "search", "functions"); set it on Usage.Provider.
func (c *books) Record(ctx context.Context, u meter.Usage) (*meter.RecordResult, error) {
	amt := moneyOf(u)

	// Co-resident native ledger: post the usage debit DIRECTLY (no HTTP), the ONE
	// money client. finance is an exact 18-decimal USD ledger, so the typed Amount
	// debits with NO rounding — a per-token cost priced at 18-dp is never floored to
	// cents or micros. The debit is idempotent on the act's Ref inside finance, and it
	// lands in the live books.
	if fin := finance.Current(); fin != nil {
		if err := fin.RecordUsage(ctx, c.input(u)); err != nil {
			return nil, err
		}
		return &meter.RecordResult{User: u.User, Amount: amt.Cents(), Currency: u.Currency, Type: "withdraw"}, nil
	}

	// The ledger is in another PROCESS, so the debit crosses the internal plane to the
	// one that owns it — the same crossing the gate, the balance read and every other
	// in-tree money call make.
	//
	// The amount crosses as the EXACT decimal, never a folded cent or micro figure: the
	// receiver parses it and debits it verbatim, so an 18-decimal per-token charge arrives
	// unrounded. client.Amount is the ONE conversion, so an amount cannot be packed by one
	// rule here and read by another there. The whole row crosses with it, so a debit taken
	// off this path is attributed as richly as one taken on the co-resident path.
	ctx, cancel := context.WithTimeout(plane.For(ctx, u.Org), peerTimeout)
	defer cancel()
	if _, err := commerce.FinanceRecord(ctx, &client.RecordIn{
		Subject: u.User,
		Amount:  client.Amount(amt.Unwrap()),
		Usage:   wire(u),
	}); err != nil {
		if errors.Is(err, plane.ErrNoPeer) {
			// No commerce anywhere in this deployment: there is nothing to bill
			// through, which is the one shape a missing debit is correct in.
			return nil, nil
		}
		return nil, fmt.Errorf("metering: plane usage debit: %w", err)
	}
	// The debit's own figure, exactly as the co-resident branch reports it. No transaction
	// id: the peer answers the amount it wrote and nothing else, and inventing one here
	// would hand a caller an identifier that names nothing.
	return &meter.RecordResult{User: u.User, Amount: amt.Cents(), Currency: u.Currency, Type: "withdraw"}, nil
}

// ---- HTTP plumbing -------------------------------------------------------

func (c *books) get(ctx context.Context, path string, q url.Values, org string) ([]byte, error) {
	u := c.baseURL + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req, org)
}

func (c *books) do(req *http.Request, org string) ([]byte, error) {
	if org != "" {
		req.Header.Set(headerOrg, org)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metering: commerce unreachable: %w", err)
	}
	defer resp.Body.Close()

	// Read a bounded body — these are tiny JSON objects.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("metering: read commerce response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("metering: commerce status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// balanceResponse mirrors commerce GET /v1/billing/balance. Amounts are cents;
// available = balance - holds.
type balanceResponse struct {
	User      string `json:"user"`
	Currency  string `json:"currency"`
	Balance   int64  `json:"balance"`
	Holds     int64  `json:"holds"`
	Available int64  `json:"available"`
}
