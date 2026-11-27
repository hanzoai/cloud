package tax

// plane.go — an org's form, published for the process that composes an org as an
// economic principal (/v1/principal), and the 1099s it received, for the one that
// shows a seller where it stands (/v1/marketplace).
//
// Three reads, and none answers a full number. tax_standing is the caller's own
// form; tax_payee is what the caller, as a payer, holds for one payee — the same
// answer GET /v1/tax/w9/{id} gives, decided by the same grant in the payee's own
// file. A payer that has not been granted a payee's form learns where its request
// stands and nothing of the form.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	principalpeer "github.com/hanzoai/cloud/client/principal"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// expose publishes the two reads. Use calls it.
func expose(s *cloud.Service[state]) {
	p := rpc{ops{s: s}}
	cloud.Plane().Post("/tax/standing", p.standing,
		zip.WithOperationID(client.TaxStanding),
		zip.WithSummary("The caller's own tax form, every number masked"))
	cloud.Plane().Post("/tax/payee", p.payee,
		zip.WithOperationID(client.TaxPayee),
		zip.WithSummary("What the caller, as payer, holds for one payee"))
	cloud.Plane().Post("/tax/inbox", p.inbox,
		zip.WithOperationID(client.TaxInbox),
		zip.WithSummary("The 1099s furnished to the caller"))
}

// plane binds the ops to the plane handlers; a method value is the only bound
// form zipdoc can lift prose from.
type rpc struct{ o ops }

// Answers the caller's own tax form — its W-9, W-8BEN or W-8BEN-E — every number
// masked, with whether it is certified for its current facts and, for a W-8,
// whether it is valid today and when it expires. An org with no profile answers
// an empty form, which is a fact and not an error. Read only on the replica that
// is the org's elected writer, so a revoked or rewritten form is never answered
// from a stale copy.
func (p rpc) standing(ctx context.Context, _ *client.StandingIn) (*client.TaxForm, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tax standing: no org on the call")
	}
	st, err := p.o.other(org)
	if err != nil {
		return nil, err
	}
	r, n, row, err := p.o.loadProfile(ctx, org, st)
	if err == errNotFound {
		return &client.TaxForm{}, nil
	}
	if err != nil {
		return nil, err
	}
	return formOf(r, n, row), nil
}

// Answers what the caller, as a payer, holds for one payee: where its request for
// the payee's form stands, and — only while the payee's grant is live — the
// payee's form, every number masked. The grant is read in the payee's own file, on
// the replica that is its elected writer.
func (p rpc) payee(ctx context.Context, in *client.TaxPayeeIn) (*client.PayeeForm, error) {
	payer, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tax payee: no org on the call")
	}
	payee := strings.TrimSpace(in.Payee)
	if !principal.NamesOrg(payee) {
		return nil, zip.ErrBadRequest("tax payee: payee is an org id")
	}
	pst, err := p.o.storeOf(payer)
	if err != nil {
		return nil, err
	}
	cp, err := p.o.counterparty(ctx, payer, pst, payee)
	if err != nil {
		return nil, err
	}
	out := &client.PayeeForm{Status: cp.status()}
	if !cp.granted() || cp.w9 == nil {
		return out, nil
	}
	est, err := p.o.other(payee)
	if err != nil {
		return nil, err
	}
	r, n, row, err := p.o.loadProfile(ctx, payee, est)
	switch {
	case err == errNotFound:
		return out, nil
	case err != nil:
		return nil, err
	}
	out.Form = formOf(r, n, row)
	out.Form.Birth = "" // the payee's date of birth is not the payer's to read
	return out, nil
}

// payer reports whether payer pays payee: it has paid it on a rail, or it asked
// /v1/principal whether it may. A deployment that runs no principal app answers
// only from the rails; any other failure to ask is an outage and fails the
// request.
func (o ops) payer(ctx context.Context, payer, payee string) (bool, error) {
	ok, err := o.paid(ctx, payer, payee)
	if err != nil || ok {
		return ok, err
	}
	out, err := principalpeer.PrincipalIntent(ctx, &client.IntentIn{Payee: payee})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return false, nil
	case err != nil:
		return false, zip.Errorf(http.StatusBadGateway, "tax: principal: %v", err)
	}
	return out.Asked, nil
}

// Answers the Copy B statements furnished to the caller — every 1099 a payer on
// Hanzo delivered to it, oldest first — without their figures: who furnished each,
// its kind and year, and whether a correction replaced it. GET /v1/tax/inbox reads
// the figures.
func (p rpc) inbox(ctx context.Context, in *client.InboxIn) (*client.Inbox, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tax inbox: no org on the call")
	}
	st, err := p.o.other(org)
	if err != nil {
		return nil, err
	}
	rows, err := st.statements(ctx, in.Year)
	if err != nil {
		return nil, err
	}
	replaced := map[string]bool{}
	for _, r := range rows {
		if r.supersedes != "" {
			replaced[r.supersedes] = true
		}
	}
	out := &client.Inbox{Received: []client.Received{}}
	for _, r := range rows {
		out.Received = append(out.Received, client.Received{ID: r.id, Payer: r.payer, Kind: string(r.kind), Year: r.year,
			Corrected: r.supersedes != "", Superseded: replaced[r.id], Furnished: r.furnished})
	}
	return out, nil
}

// restate asks principal to bring the org's TaxPrincipalCredentials in line with
// the form just certified or rewritten. It never fails the act that changed the
// form: the certification is the org's, and the statements downstream of it are
// principal's to converge — its sweep reconciles what a failed ask leaves.
func (o ops) restate(ctx context.Context, org string) {
	out, err := principalpeer.PrincipalStatements(ctx, &client.StatementsIn{})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
	case err != nil:
		o.s.Log.Warn("tax: statements not reconciled with the form", "org", org, "err", err)
	default:
		o.s.Log.Info("tax: statements reconciled with the form", "org", org, "issued", out.Issued, "revoked", out.Revoked)
	}
}
