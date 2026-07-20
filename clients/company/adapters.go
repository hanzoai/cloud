package company

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/captable"
	"github.com/hanzoai/cloud/clients/dataroom"
)

// adapters.go wires the company provider interfaces to the REAL sibling subsystems
// via their in-process facades: DocumentSink → clients/dataroom.Ingest, CapTable →
// clients/captable.*, OrgUpgrader → the captable company row. Each adapter is a thin
// mapping from the company domain model to the sibling's contract; the composition
// (which stakeholders, which share class, how many shares) lives HERE so the machine
// and the sibling facades stay orthogonal.

// foundingAuthorizedShares is the common-stock pool the founding allocation divides.
// A founder's shares = equityBps × sharesPerBps, so 10,000 bps (100%) == the whole
// pool.
const (
	foundingAuthorizedShares int64 = 10_000_000
	sharesPerBps             int64 = 1_000 // 10_000_000 / 10_000 bps
)

// ---- DocumentSink → dataroom ----

type dataroomSink struct{}

func (dataroomSink) Ingest(ctx context.Context, org, name, contentType string, data []byte) (string, error) {
	return dataroom.Ingest(ctx, org, name, contentType, data)
}

// ---- CapTable → captable ----

type captableAdapter struct{}

// SetIncorporation records the entity kind on the tenant's canonical company row.
func (captableAdapter) SetIncorporation(ctx context.Context, org, companyName, incType, country, state string) error {
	return captable.SetIncorporation(ctx, org, companyName, incType, country, state)
}

// SeedFounders writes the founding allocation into the cap table: it sets the
// company row, adds every founder as a FOUNDER stakeholder, ensures a Common share
// class, and issues each founder shares proportional to their equity (bps × 1000 of
// the 10M pool). Idempotent enough to re-run: stakeholders dedupe by email, the
// share class is ensured-by-name, and certificate ids are per-founder.
func (a captableAdapter) SeedFounders(ctx context.Context, org, companyName string, founders []Founder) error {
	holders := make([]captable.StakeholderInput, 0, len(founders))
	for _, fo := range founders {
		holders = append(holders, captable.StakeholderInput{
			Name: fo.Name, Email: fo.Email,
			StakeholderType: "INDIVIDUAL", CurrentRelationship: "FOUNDER",
		})
	}
	if _, err := captable.AddStakeholders(ctx, org, holders); err != nil {
		return fmt.Errorf("seed founders: add stakeholders: %w", err)
	}
	classID, err := captable.EnsureShareClass(ctx, org, captable.ShareClassInput{
		Name: "Common", ClassType: "COMMON", InitialSharesAuthorized: foundingAuthorizedShares,
		VotesPerShare: 1, ParValue: 0.0001, PricePerShare: 0.0001,
	})
	if err != nil || classID == "" {
		return fmt.Errorf("seed founders: ensure common class: %w", err)
	}
	ids, err := captable.StakeholderIDsByEmail(ctx, org)
	if err != nil {
		return fmt.Errorf("seed founders: resolve ids: %w", err)
	}
	for i, fo := range founders {
		sid := ids[fo.Email]
		if sid == "" || fo.EquityBps <= 0 {
			continue // no stakeholder id or no equity → nothing to issue
		}
		cert, err := genID("CS")
		if err != nil {
			return err
		}
		if err := captable.IssueShares(ctx, org, captable.ShareInput{
			StakeholderID: sid, ShareClassID: classID, CertificateID: fmt.Sprintf("%s-%d", cert, i+1),
			Quantity: int64(fo.EquityBps) * sharesPerBps, Status: "ACTIVE",
		}); err != nil {
			return fmt.Errorf("seed founders: issue shares to %s: %w", fo.Email, err)
		}
	}
	return nil
}

// AddStakeholders maps company stakeholders (used by the cap-table import path) to
// the captable contract.
func (captableAdapter) AddStakeholders(ctx context.Context, org string, holders []Stakeholder) (int, error) {
	in := make([]captable.StakeholderInput, 0, len(holders))
	for _, h := range holders {
		st := h.StakeholderType
		if st == "" {
			st = "INDIVIDUAL"
		}
		rel := h.CurrentRelationship
		if rel == "" {
			rel = "INVESTOR"
		}
		in = append(in, captable.StakeholderInput{
			Name: h.Name, Email: h.Email, StakeholderType: st,
			CurrentRelationship: rel, InstitutionName: h.InstitutionName,
		})
	}
	return captable.AddStakeholders(ctx, org, in)
}

// RecordRound maps a company fundraising round to the captable contract.
func (captableAdapter) RecordRound(ctx context.Context, org string, r RoundInput) (string, error) {
	return captable.RecordRound(ctx, org, captable.RoundInput{
		Name: r.Name, RoundType: r.RoundType, TargetAmount: r.TargetAmount,
		PreMoneyValuation: r.PreMoneyValuation, PricePerShare: r.PricePerShare,
		ShareClassID: r.ShareClassID,
	})
}

// ---- OrgUpgrader → captable company row ----

// captableUpgrader records the "org is now a company" fact on the canonical captable
// company row (incorporation_type + jurisdiction). Reflecting the fact onto the IAM
// Organization (e.g. appending a "company" tag via iamClient.UpdateOrganization) is a
// documented follow-on: cloud has no org-write path today (it only reads
// gateway-minted identity), so adding one is a separate, security-reviewed change.
type captableUpgrader struct{}

func (captableUpgrader) MarkCompany(ctx context.Context, f *Formation) error {
	name := f.Name
	if name == "" {
		name = f.Org
	}
	return captable.SetIncorporation(ctx, f.Org, name,
		f.Structure.incorporationType(), "US", string(f.Jurisdiction))
}
