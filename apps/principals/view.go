package principals

// view.go — one org, composed as an economic principal.

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Returns the caller's org as an economic principal, composed from the apps that
// own each fact: its legal entity and founders' identity verification (company),
// the W-9 or W-8 it certified (tax), its wallets (wallet), the agents that answer
// to it — spawned ones with the agent that spawned them — (agents), its screening
// against the OFAC, UN, EU and UK sanctions lists and the embargoed jurisdictions,
// run now, and what it still lacks to pay and be paid. The read changes nothing:
// a possible match it finds is recorded for a platform reviewer when a payment to
// or from the org is cleared.
//
// Any member reads it. The detail behind a verdict — founders' names and
// statuses, masked tax numbers, treaty claims, sanctions matches — is answered to
// the org's admins only. An owning app this deployment does not run is named in
// sources as absent rather than read as empty.
func (o ops) view(ctx context.Context, _ *cloud.Unit) (*iam.View, error) {
	org, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	f, err := read(ctx, true)
	if err != nil {
		return nil, err
	}
	sc, err := o.screen(ctx, org, f.known(), false)
	if err != nil {
		return nil, err
	}
	admin := full(ctx)
	v := &iam.View{Org: org, Identity: f.identity(), Sanctions: sc, Wallets: f.wallets, Agents: f.agents, Sources: f.sources}
	if v.Wallets == nil {
		v.Wallets = []iam.WalletBrief{}
	}
	if v.Agents == nil {
		v.Agents = []iam.AgentBrief{}
	}
	if !admin {
		v.Sanctions.Matches = nil
	}
	if e := f.entity; e.Found {
		v.Entity = &iam.Entity{Name: e.Name, Structure: e.Structure, Jurisdiction: e.Jurisdiction, Stage: e.Stage,
			Imported: e.Imported, EIN: "absent"}
		if f.form.Form == "w9" && f.form.TINType == "ein" && f.form.TIN != "" {
			v.Entity.EIN = "on_file"
		}
		if admin {
			v.Entity.Founders = e.Founders
		}
	}
	if t := f.form; t.Form != "" {
		v.Tax = &iam.Form{Form: t.Form, USPerson: t.Form == "w9", Country: t.Country, Residence: t.Residence,
			Certified: t.Certified, Valid: t.Valid, Expires: t.Expires}
		if admin {
			v.Tax.TIN, v.Tax.ForeignTIN, v.Tax.Chapter3, v.Tax.Chapter4, v.Tax.Treaty = t.TIN, t.ForeignTIN, t.Chapter3, t.Chapter4, t.Treaty
		}
	}
	v.Compliance = summary(v)
	noStore(ctx)
	return v, nil
}

// summary is what the view says an org lacks, each with its rule.
func summary(v *iam.View) iam.Summary {
	var missing []principal.Step
	if v.Identity.Status != idVerified {
		missing = append(missing, principal.Step{Code: "identity", Who: "org", Where: "POST /v1/company/kyc", What: v.Identity.Reason, Rule: ruleIdentity})
	}
	switch v.Sanctions.Status {
	case screenClear:
	case screenBlocked:
		missing = append(missing, principal.Step{Code: "sanctions_blocked", Who: "none", What: v.Sanctions.Reason, Rule: ruleBlocked})
	case screenReview:
		missing = append(missing, principal.Step{Code: "sanctions_review", Who: "Hanzo platform reviewer", What: v.Sanctions.Reason, Rule: ruleReview})
	case screenUnscreened:
		missing = append(missing, principal.Step{Code: "sanctions_unscreened", Who: "org", What: v.Sanctions.Reason, Rule: ruleUnscreened})
	default:
		missing = append(missing, principal.Step{Code: "sanctions_unavailable", Who: "Hanzo platform", What: v.Sanctions.Reason, Rule: ruleUnavailable})
	}
	switch {
	case v.Tax == nil:
		missing = append(missing, principal.Step{Code: "tax_form", Who: "org", Where: "PUT /v1/tax/profile",
			What: "Certify a W-9 (a U.S. person) or a W-8BEN or W-8BEN-E (a foreign person): a payer asks for it before it pays.", Rule: ruleUnknown})
	case !v.Tax.Valid && v.Tax.USPerson:
		missing = append(missing, principal.Step{Code: "w9", Who: "org", Where: "PUT /v1/tax/profile",
			What: "The W-9 carries no TIN.", Rule: ruleW9})
	case !v.Tax.Valid:
		missing = append(missing, principal.Step{Code: "w8", Who: "org", Where: "POST /v1/tax/profile/certify",
			What: "The W-8 is uncertified for its current facts or expired: certify it.", Rule: ruleW8})
	}
	if missing == nil {
		missing = []principal.Step{}
	}
	return iam.Summary{Ready: len(missing) == 0, Missing: missing}
}

// Answers the caller's org as a principal, as far as the org may see about itself:
// its founders' identity verification, its own screening status and the reason in
// words — never a match or a list entry — and the statements signed for its
// agents. The read changes nothing.
func (o ops) planeStanding(ctx context.Context, _ *client.PrincipalIn) (*client.Standing, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("principal standing: no org on the call")
	}
	f, err := read(ctx, false)
	if err != nil {
		return nil, err
	}
	sc, err := o.screen(ctx, org, f.known(), false)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ofOrgStatements(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	out := &client.Standing{Identity: f.identity().Status, Sanctions: sc.Status, Reason: sc.Reason, Statements: []client.Statement{}}
	if f.entity.Found {
		out.Entity = f.entity.Stage
	}
	now := time.Now().Unix()
	for _, r := range rows {
		st := r.Statement
		st.Status, st.JWS = r.status(now), r.body
		out.Statements = append(out.Statements, st)
	}
	return out, nil
}
