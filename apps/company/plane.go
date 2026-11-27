package company

// plane.go — the entity, published for the processes that compose an org as an
// economic principal.
//
// /v1/principal reads an org's legal entity and its founders' identity checks to
// answer who the org is and whether it is verified, and to screen its names.
// The formation is this process's, so the question is asked here, never answered
// from a copy.
//
// THE ORG RIDES THE CALLER: client.EntityIn carries nothing, so the entity
// answered is the caller's own. Founders' emails and provider references stay
// here — a reader needs a founder's name and status, not the address a KYC
// invitation went to.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/zap-proto/zip"
)

// exposeEntity publishes the entity read. Use calls it.
func exposeEntity(s *cloud.Service[state]) {
	p := plane{s}
	cloud.Plane().Post("/company/entity", p.entity,
		zip.WithOperationID(client.CompanyEntity),
		zip.WithSummary("The caller's legal entity and its founders' identity checks"))
}

// plane binds the service to the plane handlers; a method value is the only bound
// form zipdoc can lift prose from.
type plane struct{ s *cloud.Service[state] }

// Answers the caller's legal entity: the formation's name, structure, state and
// stage, whether it was imported rather than formed here, and each founder with
// ownership and identity-verification status, verbatim. An org that never began a
// formation answers found=false, which is a fact and not an error.
func (p plane) entity(ctx context.Context, _ *client.EntityIn) (*client.Entity, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("company entity: no org on the call")
	}
	f, err := p.s.State.store.Get(ctx, org)
	if err == errNotFound {
		return &client.Entity{Founders: []iam.Founder{}}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "company entity: %v", err)
	}
	out := &client.Entity{
		Found: true, Name: f.Name, Structure: string(f.Structure), Jurisdiction: string(f.Jurisdiction),
		Stage: string(f.Stage), Imported: f.AlreadyIncorporated, Founders: make([]iam.Founder, 0, len(f.Founders)),
	}
	for _, fd := range f.Founders {
		out.Founders = append(out.Founders, iam.Founder{Name: fd.Name, EquityBps: fd.EquityBps, KYC: fd.KYCStatus})
	}
	return out, nil
}
