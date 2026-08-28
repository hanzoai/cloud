package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// MaxCustomerConcurrency bounds the per-org enrichment fan-out so a large fleet does
// not open one upstream connection per org at once. Admin is low-QPS; 8 keeps latency
// low without hammering IAM/commerce.
const MaxCustomerConcurrency = 8

// ListOrgs reads the org directory (owner = admin org) as the typed shape the
// overview/orgs/usage/customer/revenue/finance aggregators fold over.
func ListOrgs(s *cloud.Service[State], ctx context.Context, cr iam.Creds) ([]iam.Org, error) {
	q := url.Values{}
	q.Set("owner", authz.AdminOrg)
	res, err := s.State.IAM.Orgs(ctx, cr, q)
	if err != nil {
		return nil, err
	}
	var orgs []iam.Org
	if len(res.Rows) > 0 {
		if err := json.Unmarshal(res.Rows, &orgs); err != nil {
			return nil, fmt.Errorf("orgs decode: %w", err)
		}
	}
	return orgs, nil
}

// OrgMoney returns one org's consumption and wallet — the ONE per-org money read every
// fleet aggregator (overview, orgs, customers, revenue, finance) folds over. The error is
// non-nil ONLY when a read FAILED, so a caller folds the per-org failure into a degraded
// source rather than presenting the resulting undercount as authoritative. An org that
// simply has no money yet reads a clean (0, 0, nil), never a failure.
//
// IT ASKS THE LEDGER BY NAME. The money lives in a SQLite file that only the process
// mounting commerce may open, and admin is a different process, so this was an HTTP read
// against commerce's own /v1/billing/* — routes that are behind `//go:build cloud` and
// are compiled into no binary here. GET /v1/billing/usage/rollup is registered nowhere
// and answered 404 for every org on every load, which is what marked the money source
// degraded on a fleet whose money was fine. That is the same 404 the referral, affiliate,
// author and usage surfaces each hit, and they were moved to plane.FinanceSpend; admin
// was the last caller left on the dead path.
//
// So there is one way now, and it holds wherever commerce runs: ask the process that owns
// the ledger. ONE call answers both halves — the ledger reports the window's consumption
// beside the wallet it is drawn from — so a spend and a balance shown side by side can no
// longer come from two reads that disagreed. No URL, no service token, nothing a
// deployment can set wrong.
//
// ABSENCE IS THE ROUTER'S WORD. ErrNoLedger — and only it — means this deployment runs
// no commerce, which is a clean answer rather than an outage. The predicate it replaces
// asked whether a base URL and a service token were set; neither exists for a peer
// reached by name, and both were set on a deployment whose every read was 404ing, so the
// money source could read healthy while nothing had been read at all.
func OrgMoney(s *cloud.Service[State], as Delegated, org string) (spend, credits int64, err error) {
	money, err := commercepeer.FinanceSpend(as.at(org),
		&plane.SpendIn{Since: time.Now().UTC().AddDate(0, 0, -spendWindowDays).Unix()})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return 0, 0, ErrNoLedger
	case err != nil:
		return 0, 0, fmt.Errorf("money: %s: %w", org, err)
	case money == nil:
		// A void reply is not a zero month. Nothing was read, so nothing is known.
		return 0, 0, fmt.Errorf("money: %s: the ledger answered nothing", org)
	}
	// Per-token debits are routinely finer than a cent; the rounding is explicit here
	// rather than an exactness guard turning a real ledger into an error.
	spend, serr := money.Consumed.RoundMinor()
	credits, cerr := money.Balance.RoundMinor()
	if serr != nil || cerr != nil {
		return 0, 0, fmt.Errorf("money: %s: unreadable amount", org)
	}
	return spend, credits, nil
}

// Delegated is THIS request's authority, carried onto a context with no request behind
// it and ready to be pointed at any tenant. A money read takes one instead of a plain
// context, so the delegation cannot be skipped: the compiler asks for it.
//
// It exists because building it READS the request's headers, and fasthttp's header store
// shares one scratch buffer across reads — so a dozen goroutines each building their own
// race on it, which -race proves on the overview's twelve-wide read. Delegate once, ahead
// of the fan-out; each per-tenant read is then a cheap re-pointing of a value nobody
// else holds.
type Delegated struct{ ctx context.Context }

// Delegate carries this request's principal off the request. Call it ONCE per read,
// ahead of any fan-out.
func Delegate(ctx context.Context) Delegated {
	if c, ok := cloud.Request(ctx); ok {
		return Delegated{cloud.As(c, "")} // the principal whole, the tenant still the caller's own
	}
	return Delegated{ctx}
}

// at points the delegated authority at one tenant — the operator acting on someone
// else's books, which is exactly what a fleet read is. The tenant has to ride the call:
// a read that named no tenant would be answered, correctly, with the operator's own
// books every time.
func (d Delegated) at(org string) context.Context {
	who := zip.CallerOf(d.ctx)
	who.Org = org
	return zip.WithCaller(d.ctx, who)
}

// ErrNoLedger reports that this deployment runs no commerce at all, so there is no money
// to read and none missing. It is the ONE absence a fleet board may fold into a zero.
var ErrNoLedger = errors.New("this deployment runs no commerce")

// MoneyFailed reports whether an OrgMoney error is an OUTAGE — the rule stated once, so
// the boards that fold this read cannot disagree about which absences are failures.
func MoneyFailed(err error) bool { return err != nil && !errors.Is(err, ErrNoLedger) }

// spendWindowDays is the trailing window every fleet money surface means by "spend":
// thirty days, which is what the SpendCents30d field it feeds is named for.
const spendWindowDays = 30

// FindOrg returns the IAM org by slug (nil, nil when it does not exist) so a management
// action can validate its target before acting — never credit or suspend an org that
// isn't real.
func FindOrg(s *cloud.Service[State], ctx context.Context, cr iam.Creds, org string) (*iam.Org, error) {
	orgs, err := ListOrgs(s, ctx, cr)
	if err != nil {
		return nil, err
	}
	for i := range orgs {
		if orgs[i].Name == org {
			return &orgs[i], nil
		}
	}
	return nil, nil
}

// Display returns displayName when non-blank, else the fallback.
func Display(displayName, fallback string) string {
	if strings.TrimSpace(displayName) != "" {
		return displayName
	}
	return fallback
}
