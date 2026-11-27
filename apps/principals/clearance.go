package principals

// clearance.go — whether the caller may pay another org, asked BEFORE the payment.
//
// Withholding is decided when money moves, not when it is reported, so the time
// to know what a payment owes is before it is made. A clearance gathers the facts
// — payer's and payee's identity and screening, the form the payer holds for the payee,
// where the service is performed — applies rules.go to them, and records the
// decision whole: a clearance is never rewritten, and every one is on the audit
// trail with the rules that decided it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/tax"
	"github.com/hanzoai/cloud/client"
	taxpeer "github.com/hanzoai/cloud/client/tax"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// clearIn is a payment the caller means to make.
type clearIn struct {
	// Payee is the org id of the org to be paid.
	Payee string `json:"payee"`
	// Amount is the gross amount in U.S. dollars, e.g. "1250.00".
	Amount string `json:"amount"`
	// Category is what is bought: services, attorney, rents, royalties, other or
	// merchandise. Services when omitted.
	Category tax.Category `json:"category,omitempty"`
	// Rail is how it will move: x402, ledger, chain, ach, wire, card, network or
	// other — the rails an economic event names. Ledger when omitted.
	Rail string `json:"rail,omitempty"`
	// Performed is where the service is performed — for rents, where the property
	// is; for royalties, where it is used — as an ISO 3166-1 alpha-2 code. It
	// decides the source of a foreign payee's income, and so its withholding.
	Performed string `json:"performed,omitempty"`
	// Platform is whether the caller operates the platform through which the payee
	// provides this to the platform's users, which decides platform-operator
	// reporting. Omitted, it is asked back as a fact when it matters.
	Platform *bool `json:"platform,omitempty"`
}

// Decides whether the caller's org may pay another org, BEFORE the payment: what
// must happen first, what must be withheld and why, what must be reported, by
// whom and when, and how the payment may settle net of withholding. Any member of
// the paying org; the decision is recorded and on the audit trail in one
// transaction.
//
// The rules, each cited on the answer: payer's and payee's founders identity-verified
// (Hanzo policy); payer and payee screened clear of the OFAC, UN, EU and UK lists
// and the embargoed jurisdictions — a confirmed match blocks, a possible one waits
// on a Hanzo platform reviewer, and lists that are not loaded and fresh clear
// nobody; the payee's status is what the form the payee granted the payer says,
// and until it grants one the payee furnishes it; a U.S. payee with no TIN on
// file owes its W-9, and paid without one 24% backup withholding applies (IRC
// §3406); a foreign payee on a valid W-8BEN or W-8BEN-E is withheld on at 30% on
// U.S.-source income (IRC §1441, §1442), reduced by a treaty rate the form
// claims; a service performed outside the United States is foreign-source and
// not withheld on; Form 1099 by the payer for a reportable U.S. payee at the
// year's threshold, Forms 1042-S and 1042 for U.S.-source income to a foreign
// person, and platform-operator reports (EU DAC7, UK, Canada, Australia) where
// the payer is the payee's platform. Every filer named is an org, never Hanzo.
//
// Nothing of the payee is answered but what the payer must know to act: that the
// payment clears, what the payee must furnish, or that it cannot clear and the
// payer should contact support — never why, and never the payee's status before
// the payee grants its form. A payer asks about at most 50 new payees a day and
// 120 payments an hour.
//
// Example: {"payee": "globex", "amount": "5000.00", "category": "services", "performed": "DE"}
func (o ops) clear(ctx context.Context, in *clearIn) (*principal.Clearance, error) {
	payer, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	cl, err := o.cleared(ctx, payer, in, "")
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return cl, nil
}

// cleared gathers the facts about one payment, applies the rules, and records the
// decision on the trail in the transaction that writes it. It is the whole of a
// clearance, reached from the route and from the plane alike. again is the
// clearance it asks again, from the plane only.
func (o ops) cleared(ctx context.Context, payer string, in *clearIn, again string) (*principal.Clearance, error) {
	if o.s.State.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	c, err := in.check(payer)
	if err != nil {
		return nil, err
	}
	st := o.s.State.store
	if _, err := admit(ctx, st.db, payer, c.Payee, again, time.Now().Unix()); err != nil {
		return nil, err
	}
	mine, err := read(ctx, false)
	if err != nil {
		return nil, err
	}
	theirs, err := counterpart(c.Payee)
	if err != nil {
		return nil, err
	}
	held, err := taxpeer.TaxPayee(ctx, &client.TaxPayeeIn{Payee: c.Payee})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		held = &client.PayeeForm{Status: "none"}
	case err != nil:
		return nil, zip.Errorf(http.StatusBadGateway, "principal: tax: %v", err)
	}
	if c.PayerScreen, err = o.screen(ctx, payer, mine.known(), true); err != nil {
		return nil, err
	}
	if c.PayeeScreen, err = o.screen(ctx, c.Payee, theirs.known(), true); err != nil {
		return nil, err
	}
	c.PayerID, c.PayeeID = mine.identity(), theirs.identity()
	c.Held, c.Wallets = *held, len(theirs.wallets) > 0

	cl := &principal.Clearance{ID: mint.ID("clr"), Payer: payer, Payee: c.Payee, Amount: usd(c.Cents), Category: string(c.Category),
		Rail: c.Rail, Performed: c.Performed, Platform: c.Platform, Decision: Decide(c), DecidedAt: time.Now().Unix(), Notice: principal.Notice}
	body, err := json.Marshal(cl)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "principal: %v", err)
	}
	// The payer asked as an org that may pay: its own founders verified and its
	// own screening clear. Only such a clearance is evidence apps/tax takes that
	// the payer is about to pay the payee (intent).
	ok := c.PayerID.Status == idVerified && c.PayerScreen.Status == screenClear
	err = o.commit(ctx, func(tx *sql.Tx) ([]audit.Record, error) {
		// Asked again inside the write: concurrent clearances each passed the check
		// above, and only as many as the bounds allow are recorded and answered.
		root, err := admit(ctx, tx, payer, c.Payee, again, cl.DecidedAt)
		if err != nil {
			return nil, err
		}
		if err := o.s.State.store.putClearance(ctx, tx, cl.ID, payer, c.Payee, cl.Allowed, ok, string(body), cl.DecidedAt); err != nil {
			return nil, err
		}
		after := map[string]any{
			"payee": c.Payee, "allowed": cl.Allowed, "status": cl.Status, "category": c.Category, "rail": c.Rail,
			"rate": cl.Withholding.Rate, "required": codes(cl.Required), "reporting": codes(cl.Reporting), "rules": decided(cl.Rules),
		}
		if root != "" {
			if err := putRepeat(ctx, tx, cl.ID, root, cl.DecidedAt); err != nil {
				return nil, err
			}
			after["repeats"] = root
		}
		rec, err := o.record(ctx, payer, "principal.clearance.decided", audit.Resource{Type: "principal.clearance", ID: cl.ID}, after, true)
		return []audit.Record{rec}, err
	})
	if err != nil {
		return nil, err
	}
	return cl, nil
}

// Decides whether the caller may pay a payee, before the payment, and records the
// decision exactly as POST /v1/principal/clearance does — the same facts, rules,
// bounds and trail — answering the verdict a payer that must act on it needs:
// cleared with what the payee nets on the rail, needs_form when the payee must
// furnish its W-9 or W-8 first and nothing else stands in the way, or blocked.
// The category is the economic event's word for what is bought; the rules decide
// what it is for tax.
func (o ops) planeClear(ctx context.Context, in *client.ClearIn) (*client.Cleared, error) {
	payer, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("principal clear: no org on the call")
	}
	cat, ok := taxCategory[in.Category]
	if !ok {
		return nil, zip.ErrBadRequest(fmt.Sprintf("category is one of service, goods, transfer or royalty, not %q", in.Category))
	}
	cl, err := o.cleared(ctx, payer, &clearIn{Payee: in.Payee, Amount: in.Amount, Category: cat, Rail: in.Rail, Performed: in.Performed},
		strings.TrimSpace(in.Again))
	if err != nil {
		return nil, err
	}
	return judged(cl), nil
}

// taxCategory is the category the rules decide each economic category under. A
// refund returns a payment and is never a new one, so it is not cleared.
var taxCategory = map[string]tax.Category{
	"":                      tax.Services,
	client.CategoryService:  tax.Services,
	client.CategoryGoods:    tax.Merchandise,
	client.CategoryRoyalty:  tax.Royalties,
	client.CategoryTransfer: tax.Other,
}

// formSteps are the steps a payee takes by furnishing its tax form to the payer,
// and formFacts the blocking facts that form answers.
var (
	formSteps = []string{"tax_form", "w9", "w8"}
	formFacts = []string{"us_person"}
)

// judged is a clearance as the payer acts on it.
func judged(cl *principal.Clearance) *client.Cleared {
	out := &client.Cleared{ID: cl.ID, Verdict: client.ClearBlocked, Required: codes(cl.Required)}
	furnish := len(cl.Required) > 0
	for _, s := range cl.Required {
		furnish = furnish && slices.Contains(formSteps, s.Code)
	}
	for _, f := range cl.Facts {
		if f.Blocks {
			out.Required = append(out.Required, f.Code)
			furnish = furnish && slices.Contains(formFacts, f.Code)
		}
	}
	switch {
	case cl.Allowed:
		out.Verdict = client.ClearCleared
		rail := client.RailLedger
		if cl.Rail == client.RailChain {
			rail = client.RailChain
		}
		for _, m := range cl.Settlement {
			if m.Rail == rail {
				out.Net, out.Withheld = m.Net, m.Withheld
			}
		}
	case furnish:
		out.Verdict = client.ClearNeedsForm
	}
	return out
}

// The bounds on how much a payer may ask. A clearance reads the payee's
// verification and screening, so an unbounded one is a way to read the platform's
// verdict on every org; the bound is on NEW payees, which is what reading every
// org takes, and on volume, which is what screening costs.
//
// Volume is counted in payments. A payment agreed before it is made — a job's —
// is cleared again at each step its money takes, so a clearance that asks again of
// the payer's own earlier one, to the same payee, counts against that payment, at
// most againPerHour, and not against the payer's hour: a payer that spends its
// hour asking cannot stall the release of a payment it already agreed.
const (
	newPayeesPerDay = 50
	clearsPerHour   = 120
	againPerHour    = 12
	dayWindow       = 24 * time.Hour
	hourWindow      = time.Hour
)

// admit refuses a clearance past the payer's bounds, and answers the payment an
// ask again repeats: the first clearance of it. again names the clearance asked
// again; one that is not the payer's own to this payee is not, and is bounded as
// any other ask.
func admit(ctx context.Context, q querier, payer, payee, again string, now int64) (string, error) {
	if again != "" {
		root, n, found, err := repeated(ctx, q, payer, payee, again, now)
		switch {
		case err != nil:
			return "", zip.Errorf(http.StatusServiceUnavailable, "%v", err)
		case found && n >= againPerHour:
			return "", zip.Errorf(http.StatusTooManyRequests, "principal: one payment is cleared again at most %d times an hour; ask again later", againPerHour)
		case found:
			return root, nil
		}
	}
	hour, fresh, known, err := load(ctx, q, payer, payee, now)
	switch {
	case err != nil:
		return "", zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	case hour >= clearsPerHour:
		return "", zip.Errorf(http.StatusTooManyRequests, "principal: at most %d clearances an hour; ask again later", clearsPerHour)
	case !known && fresh >= newPayeesPerDay:
		return "", zip.Errorf(http.StatusTooManyRequests, "principal: at most %d new payees a day; ask again tomorrow, or contact support", newPayeesPerDay)
	}
	return "", nil
}

// check validates the payment into a Case.
func (in *clearIn) check(payer string) (Case, error) {
	c := Case{Payer: payer, Payee: strings.TrimSpace(in.Payee), Category: in.Category, Rail: in.Rail,
		Performed: strings.ToUpper(strings.TrimSpace(in.Performed)), Platform: in.Platform, Year: time.Now().UTC().Year()}
	if c.Category == "" {
		c.Category = tax.Services
	}
	if c.Rail == "" {
		c.Rail = client.RailLedger
	}
	amount, err := money.ParseUSD(in.Amount)
	switch {
	case !principal.NamesOrg(c.Payee):
		return Case{}, zip.ErrBadRequest("payee is an org id")
	case c.Payee == payer:
		return Case{}, zip.ErrBadRequest("an org does not clear a payment to itself")
	case err != nil || amount.Sign() <= 0 || amount.Cmp(money.FromCents(amount.Cents())) != 0:
		return Case{}, zip.ErrBadRequest("amount is a positive U.S. dollar amount in whole cents, e.g. \"1250.00\"")
	case !c.Category.Valid():
		return Case{}, zip.ErrBadRequest("category is services, attorney, rents, royalties, other or merchandise")
	case !slices.Contains(client.Rails, c.Rail):
		return Case{}, zip.ErrBadRequest(fmt.Sprintf("rail is one of %v", client.Rails))
	case c.Performed != "" && (len(c.Performed) != 2 || strings.Trim(c.Performed, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != ""):
		return Case{}, zip.ErrBadRequest("performed is a two-letter ISO 3166-1 country code")
	}
	c.Cents = amount.Cents()
	return c, nil
}

// codes are the codes of a list of steps or obligations, for the trail.
func codes[T interface {
	principal.Step | principal.Obligation
}](xs []T) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		switch v := any(x).(type) {
		case principal.Step:
			out = append(out, v.Code)
		case principal.Obligation:
			out = append(out, v.Code)
		}
	}
	return out
}

// decided is each field's rule code, for the trail.
func decided(ds []principal.Decided) map[string]string {
	out := make(map[string]string, len(ds))
	for _, d := range ds {
		out[d.Field] = d.Rule.Code
	}
	return out
}

// clearanceRef addresses one clearance.
type clearanceRef struct {
	// ID is the clearance, "clr_"-prefixed.
	ID string `json:"id"`
}

// Reads one of the caller org's clearances, as it was decided. Any member of the
// paying org; another org's clearance is not found.
func (o ops) clearance(ctx context.Context, in *clearanceRef) (*principal.Clearance, error) {
	payer, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	body, err := o.s.State.store.clearance(ctx, payer, strings.TrimSpace(in.ID))
	if err == errNotFound {
		return nil, zip.ErrNotFound("clearance not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	var cl principal.Clearance
	if err := json.Unmarshal([]byte(body), &cl); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "principal: stored clearance: %v", err)
	}
	noStore(ctx)
	return &cl, nil
}

// clearancesIn narrows the list.
type clearancesIn struct {
	// Payee narrows to one payee.
	Payee string `json:"payee,omitempty" query:"payee"`
}

// clearanceList is the caller's clearances, newest first.
type clearanceList struct {
	Clearances []principal.Clearance `json:"clearances"`
}

// Lists the caller org's clearances, newest first, 200 at most; a payee narrows
// them. Any member of the paying org.
func (o ops) clearances(ctx context.Context, in *clearancesIn) (*clearanceList, error) {
	payer, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.clearances(ctx, payer, strings.TrimSpace(in.Payee), 200)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	out := &clearanceList{Clearances: make([]principal.Clearance, 0, len(rows))}
	for _, body := range rows {
		var cl principal.Clearance
		if err := json.Unmarshal([]byte(body), &cl); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "principal: stored clearance: %v", err)
		}
		out.Clearances = append(out.Clearances, cl)
	}
	noStore(ctx)
	return out, nil
}

// expose publishes the plane ops. Use calls it.
func expose(s *cloud.Service[state]) {
	o := ops{s: s}
	cloud.Plane().Post("/principal/intent", o.intent,
		zip.WithOperationID(client.PrincipalIntent),
		zip.WithSummary("Whether the caller asked to pay a payee"))
	cloud.Plane().Post("/principal/clear", o.planeClear,
		zip.WithOperationID(client.PrincipalClear),
		zip.WithSummary("Decide whether the caller may pay a payee, before the payment"))
	cloud.Plane().Post("/principal/standing", o.planeStanding,
		zip.WithOperationID(client.PrincipalStanding),
		zip.WithSummary("The caller's org as a principal, as far as it may see about itself"))
	cloud.Plane().Post("/principal/statements", o.planeStatements,
		zip.WithOperationID(client.PrincipalStatements),
		zip.WithSummary("Bring the statements signed for the caller's agents in line with its tax form"))
	cloud.Plane().Post("/principal/public", o.planePublic,
		zip.WithOperationID(client.PrincipalPublic),
		zip.WithSummary("What anyone may know about orgs that sell in public"))
}

// Answers whether the caller's org has asked, within the last 90 days, for a
// clearance to pay a payee on which the caller was itself identity-verified and
// screened clear — the evidence apps/tax accepts, beside a payment already made,
// that the caller is that payee's payer and may ask for its tax form before the
// first payment. A clearance an unverified or unscreened payer asked for is not:
// it could not have paid.
func (o ops) intent(ctx context.Context, in *client.IntentIn) (*client.Intent, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("principal intent: no org on the call")
	}
	since := time.Now().Add(-intentWindow).Unix()
	asked, err := o.s.State.store.asked(ctx, org, strings.TrimSpace(in.Payee), since)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	return &client.Intent{Asked: asked}, nil
}

// intentWindow is how recent a clearance must be to say a payment is about to be
// made.
const intentWindow = 90 * 24 * time.Hour
