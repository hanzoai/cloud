package principals

// facts.go — what the owning apps hold about one org, asked over the plane.
//
// An owner that is not part of this deployment is ABSENT and the answer says so;
// an owner that answered badly is an outage and fails the read. A principal
// composed from the owners that happened to answer is a wrong principal that
// looks right.

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/sdk/iam"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	companypeer "github.com/hanzoai/cloud/client/company"
	taxpeer "github.com/hanzoai/cloud/client/tax"
	walletpeer "github.com/hanzoai/cloud/client/wallet"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// facts are one org's economic facts as their owners answered them.
type facts struct {
	entity  client.Entity
	form    client.TaxForm
	wallets []iam.WalletBrief
	agents  []iam.AgentBrief
	sources []iam.Source
}

// ask calls one owner and records whether it answered. ErrNoPeer is absence and
// leaves the zero value; anything else is an outage.
func ask[In, Out any](ctx context.Context, f *facts, app string, call func(context.Context, *In) (*Out, error), in *In, into *Out) error {
	out, err := call(ctx, in)
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		f.sources = append(f.sources, iam.Source{App: app, Status: "absent"})
		return nil
	case err != nil:
		return zip.Errorf(http.StatusBadGateway, "principal: %s: %v", app, err)
	}
	f.sources = append(f.sources, iam.Source{App: app, Status: "read"})
	*into = *out
	return nil
}

// read asks every owner about the org ctx acts for. agents is asked only for the
// org's own view: a payee's agents are nothing a clearance turns on.
func read(ctx context.Context, agents bool) (facts, error) {
	var f facts
	if err := ask(ctx, &f, companypeer.App, companypeer.CompanyEntity, &client.EntityIn{}, &f.entity); err != nil {
		return f, err
	}
	if err := ask(ctx, &f, taxpeer.App, taxpeer.TaxStanding, &client.StandingIn{}, &f.form); err != nil {
		return f, err
	}
	var w client.Wallets
	if err := ask(ctx, &f, walletpeer.App, walletpeer.WalletsList, &client.WalletsIn{}, &w); err != nil {
		return f, err
	}
	f.wallets = w.Wallets
	if agents {
		var r client.AgentRoster
		if err := ask(ctx, &f, agentpeer.App, agentpeer.AgentsRoster, &client.RosterIn{}, &r); err != nil {
			return f, err
		}
		f.agents = r.Agents
	}
	return f, nil
}

// counterpart asks every owner about another org, as the platform's own act: the
// call carries that org and no person, so the owners answer the org's own facts
// — which this package screens and verifies and never answers to the caller.
func counterpart(org string) (facts, error) {
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), askBudget)
	defer cancel()
	return read(ctx, false)
}

// askBudget bounds a read of another org end to end.
const askBudget = 20 * time.Second

// known is what an org's facts give screening: every name, with where it came
// from and whether it names a person or an entity, every address, and every
// country its form ties it to.
func (f facts) known() known {
	var k known
	add := func(name, role, kind, birth string) {
		if name == "" {
			return
		}
		for _, s := range k.subjects {
			if s.Name == name && s.Kind == kind {
				return
			}
		}
		k.subjects = append(k.subjects, Subject{Name: name, Role: role, Kind: kind, Birth: birth})
	}
	if f.entity.Found {
		add(f.entity.Name, "entity", "entity", "")
		for _, fd := range f.entity.Founders {
			add(fd.Name, "founder", "individual", "")
		}
	}
	switch f.form.Form {
	case "w9":
		kind := "entity"
		if f.form.Classification == "individual" {
			kind = "individual"
		}
		add(f.form.Name, "form", kind, "")
		add(f.form.BusinessName, "form", "entity", "")
	case "w8ben":
		add(f.form.Name, "form", "individual", f.form.Birth)
		k.places = append(k.places, Place{Country: f.form.Country, Role: "citizenship"})
	case "w8bene":
		add(f.form.Name, "form", "entity", "")
		k.places = append(k.places, Place{Country: f.form.Country, Role: "organization"})
	}
	if f.form.Residence != "" {
		k.places = append(k.places, Place{Country: f.form.Residence, Role: "residence"})
	}
	k.addresses = f.wallets
	return k
}

const (
	idVerified = "verified"
	idPending  = "pending"
	idFailed   = "failed"
	idNone     = "none"
)

// identity is an org's verification verdict from its formation. A founder passes
// on a provider-reported pass or a platform reviewer's confirmation — apps/company
// admits no other way, and no org admin can record one for its own founders.
func (f facts) identity() iam.Identity {
	if !f.entity.Found || len(f.entity.Founders) == 0 {
		return iam.Identity{Status: idNone, Reason: "No formation naming founders: the org's beneficial owners have not been identified."}
	}
	var pending, failed int
	for _, fd := range f.entity.Founders {
		switch fd.KYC {
		case "verified", "reviewer_confirmed":
		case "failed":
			failed++
		default:
			pending++
		}
	}
	switch {
	case failed > 0:
		return iam.Identity{Status: idFailed, Reason: "A founder failed identity verification."}
	case pending > 0:
		return iam.Identity{Status: idPending, Reason: "A founder's identity verification is outstanding."}
	}
	return iam.Identity{Status: idVerified, Reason: "Every founder of the formation passed identity verification."}
}
