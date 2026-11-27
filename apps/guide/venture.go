package guide

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// ventureSlugRE validates a venture handle within a studio portfolio.
var ventureSlugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// VentureSetupState records the idempotent setup checkpoints for a venture so an
// interrupted or repeated launch resumes without duplicating its organization,
// playbook, budget, or team.
type VentureSetupState struct {
	// OrgProvisioned is true once the venture's isolated organization store has been opened.
	OrgProvisioned bool `json:"orgProvisioned"`
	// PlaybookEnrolled is true once the versioned curriculum has been persisted in the venture's store.
	PlaybookEnrolled bool `json:"playbookEnrolled"`
	// BudgetAllocated is true once the initial micro-USD budget has been committed, preventing duplicate allocation.
	BudgetAllocated bool `json:"budgetAllocated"`
	// TeamSeeded is true once the venture's team roster has been enrolled.
	TeamSeeded bool `json:"teamSeeded"`
	// Members lists the teammate subjects explicitly assigned access to this venture.
	Members []string `json:"members,omitempty"`
	// RevokedMembers lists teammate subjects whose seat access to this venture has been revoked.
	RevokedMembers []string `json:"revokedMembers,omitempty"`
}

// VentureRecord is one venture in a studio organization's portfolio, combining
// its thesis, governance, budget, and setup checkpoints with its live checklist
// progress read from the venture's own isolated organization store.
type VentureRecord struct {
	// Slug is the venture's unique handle inside the studio portfolio.
	Slug string `json:"slug"`
	// VentureOrg is the isolated organization namespace holding this venture's private progress, documents, runs, and secrets.
	VentureOrg string `json:"ventureOrg"`
	// StudioOrg is the parent studio organization operating the portfolio.
	StudioOrg string `json:"studioOrg"`
	// Title is the human-readable name of the venture.
	Title string `json:"title"`
	// Thesis is the falsifiable venture hypothesis being tested before entity formation.
	Thesis string `json:"thesis"`
	// Sponsor is the studio partner or executive sponsoring the venture.
	Sponsor string `json:"sponsor,omitempty"`
	// Owner is the venture lead (person or lead agent handle) accountable for executing the playbook.
	Owner string `json:"owner,omitempty"`
	// Stage is the venture's position in the studio funnel: intake, discovery, validation, formation, or scaling.
	Stage string `json:"stage"`
	// Status is the venture's operating state: active, paused, forming, formed, or offboarded.
	Status string `json:"status"`
	// Imported is true when the venture was imported into the studio rather than started from scratch.
	Imported bool `json:"imported"`
	// PlaybookVersion is the version string of the Guide curriculum enrolled for this venture.
	PlaybookVersion string `json:"playbookVersion"`
	// InitialBudgetMicroUSD is the initial venture budget in integer micro-USD, allocated at creation.
	InitialBudgetMicroUSD int64 `json:"initialBudgetMicroUSD"`
	// SpendMicroUSD is the cumulative spend recorded against this venture in integer micro-USD.
	SpendMicroUSD int64 `json:"spendMicroUSD"`
	// PendingDecisions is how many research decisions, role proposals, or external actions await human approval.
	PendingDecisions int `json:"pendingDecisions"`
	// FailedRuns is how many specialist or automation runs have failed and await retry or review.
	FailedRuns int `json:"failedRuns"`
	// Evidence lists the source citations and evidence URIs attached to the venture.
	Evidence []string `json:"evidence"`
	// LatestEvidence is the most recently recorded evidence citation or URI for this venture.
	LatestEvidence string `json:"latestEvidence,omitempty"`
	// FormationID is the legal entity formation identifier from /v1/company once the venture advances to formation.
	FormationID string `json:"formationId,omitempty"`
	// Members lists the teammate subjects explicitly assigned access to this venture.
	Members []string `json:"members,omitempty"`
	// Setup holds the idempotent setup operation checkpoints for this venture.
	Setup VentureSetupState `json:"setup"`
	// Progress is the venture's live checklist completion read from its isolated organization store.
	Progress progressView `json:"progress"`
	// Templates are the shared studio templates from the playbook visible in this venture.
	Templates []Page `json:"templates,omitempty"`
	// Resumed is true when a repeated or interrupted create call resumed an existing venture without duplicating budget or team.
	Resumed bool `json:"resumed,omitempty"`
	// CreatedAt is when the venture was first registered in the portfolio, as Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the venture record was last updated, as Unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// createVentureIn is the request body for POST /v1/guide/ventures.
type createVentureIn struct {
	// Slug is the venture's unique handle inside the studio portfolio (e.g. "vet-clinic-scheduling").
	Slug string `json:"slug"`
	// VentureOrg optionally names the explicit venture organization slug; when omitted it defaults to "<studioOrg>--<slug>".
	VentureOrg string `json:"ventureOrg,omitempty"`
	// Title is the human-readable name of the venture.
	Title string `json:"title"`
	// Thesis is the falsifiable venture hypothesis to test before entity formation.
	Thesis string `json:"thesis,omitempty"`
	// Sponsor names the studio partner or executive sponsoring the venture.
	Sponsor string `json:"sponsor,omitempty"`
	// Owner names the venture lead (person or lead agent handle) driving the venture.
	Owner string `json:"owner,omitempty"`
	// InitialBudgetMicroUSD is the initial budget allocated to the venture in integer micro-USD.
	InitialBudgetMicroUSD int64 `json:"initialBudgetMicroUSD,omitempty"`
	// Evidence lists initial source citations or evidence URIs for the venture.
	Evidence []string `json:"evidence,omitempty"`
	// Playbook selects a brand playbook name (such as "venture") or supplies a raw YAML/JSON curriculum document; empty defaults to "venture".
	Playbook string `json:"playbook,omitempty"`
	// Stage sets the initial studio funnel stage (intake, discovery, validation, formation, scaling); defaults to "intake".
	Stage string `json:"stage,omitempty"`
	// Imported marks whether this venture is being imported from an existing workspace.
	Imported bool `json:"imported,omitempty"`
	// TeamSeeded marks whether the venture's initial agent roster has been seeded.
	TeamSeeded bool `json:"teamSeeded,omitempty"`
	// Members optionally assigns initial teammate subjects access to this venture.
	Members []string `json:"members,omitempty"`
}

// ventureRef addresses a single venture by its slug in the URL path.
type ventureRef struct {
	// Slug is the venture's unique handle in the studio portfolio, from the path.
	Slug string `json:"slug"`
}

// ventureList is the response shape for GET /v1/guide/ventures.
type ventureList struct {
	// Ventures are the studio organization's ventures in creation order, each with live progress.
	Ventures []VentureRecord `json:"ventures"`
}

// ventureLifecycleIn is the request body for POST /v1/guide/ventures/:slug/lifecycle.
type ventureLifecycleIn struct {
	// Slug is the venture handle from the URL path.
	Slug string `json:"slug"`
	// Action is the lifecycle operation to perform: pause, resume, advance, form, export, sync, offboard, assign_member, or revoke_member.
	Action string `json:"action"`
	// Stage optionally updates the venture's funnel stage (intake, discovery, validation, formation, scaling).
	Stage string `json:"stage,omitempty"`
	// SpendMicroUSD optionally adds or updates recorded venture spend in integer micro-USD.
	SpendMicroUSD *int64 `json:"spendMicroUSD,omitempty"`
	// PendingDecisions optionally updates the count of decisions waiting on human review.
	PendingDecisions *int `json:"pendingDecisions,omitempty"`
	// FailedRuns optionally updates the count of failed runs requiring attention.
	FailedRuns *int `json:"failedRuns,omitempty"`
	// LatestEvidence optionally records a new evidence citation or URI on the venture.
	LatestEvidence string `json:"latestEvidence,omitempty"`
	// FormationID optionally records the /v1/company formation identifier when action is "form".
	FormationID string `json:"formationId,omitempty"`
	// TeamSeeded optionally marks the venture's agent roster checkpoint complete.
	TeamSeeded *bool `json:"teamSeeded,omitempty"`
	// Member names the teammate subject to assign or revoke when action is "assign_member" or "revoke_member".
	Member string `json:"member,omitempty"`
}

// ventureExportBundle is the portable snapshot emitted when a venture lifecycle action is "export".
type ventureExportBundle struct {
	// VentureOrg is the isolated venture organization identifier.
	VentureOrg string `json:"ventureOrg"`
	// PlaybookVersion is the enrolled curriculum version at export time.
	PlaybookVersion string `json:"playbookVersion"`
	// Steps are the venture's checklist steps with their recorded states at export time.
	Steps []stepView `json:"steps"`
	// Actions are the Business AI action ledger rows recorded inside the venture's isolated store.
	Actions []ActionRecord `json:"actions"`
}

// ventureLifecycleOut is the response shape for POST /v1/guide/ventures/:slug/lifecycle.
type ventureLifecycleOut struct {
	// Venture is the updated venture record after the lifecycle operation.
	Venture VentureRecord `json:"venture"`
	// Export is populated when action is "export", carrying the portable snapshot of the venture's checklist and action ledger.
	Export *ventureExportBundle `json:"export,omitempty"`
}

// portfolioView is the studio-wide portfolio operations summary returned by GET /v1/guide/portfolio.
type portfolioView struct {
	// StudioOrg is the studio organization whose portfolio is summarized.
	StudioOrg string `json:"studioOrg"`
	// TotalVentures is the total number of ventures registered in the studio portfolio.
	TotalVentures int `json:"totalVentures"`
	// ActiveVentures is how many ventures are currently in active execution.
	ActiveVentures int `json:"activeVentures"`
	// PausedVentures is how many ventures are currently paused.
	PausedVentures int `json:"pausedVentures"`
	// FormedVentures is how many ventures have advanced to legal entity formation.
	FormedVentures int `json:"formedVentures"`
	// OffboardedVentures is how many ventures have been offboarded from the studio.
	OffboardedVentures int `json:"offboardedVentures"`
	// TotalBudgetMicroUSD is the sum of initial budgets allocated across all ventures in integer micro-USD.
	TotalBudgetMicroUSD int64 `json:"totalBudgetMicroUSD"`
	// TotalSpendMicroUSD is the cumulative spend across all ventures in integer micro-USD.
	TotalSpendMicroUSD int64 `json:"totalSpendMicroUSD"`
	// PendingDecisions is the total count of decisions, role proposals, and external actions waiting on people across the portfolio.
	PendingDecisions int `json:"pendingDecisions"`
	// FailedRuns is the total count of failed runs across all ventures in the portfolio.
	FailedRuns int `json:"failedRuns"`
	// Ventures is the per-venture operational rollup ordered by creation time.
	Ventures []VentureRecord `json:"ventures"`
}

// CreateVenture creates or imports a venture under the caller studio's portfolio,
// captures its thesis, sponsor, owner, initial budget and seed evidence, and
// enrolls its isolated venture organization in a versioned Guide playbook
// (defaulting to venture). Repeated or interrupted calls with the same slug
// resume idempotently without duplicating the venture, its team checkpoint, or
// its initial budget.
func (o ops) createVenture(ctx context.Context, in *createVentureIn) (*VentureRecord, error) {
	studioOrg, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(in.Slug)
	if !ventureSlugRE.MatchString(slug) {
		return nil, zip.ErrBadRequest("slug is required and must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	if in.InitialBudgetMicroUSD < 0 {
		return nil, zip.ErrBadRequest("initialBudgetMicroUSD cannot be negative")
	}
	studioStore, err := storeFor(o.s.State.stores, studioOrg)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide studio store: %v", err)
	}

	existing, exists, err := studioStore.GetVenture(ctx, slug)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide get venture: %v", err)
	}

	bp, rawPlaybook, err := resolvePlaybookChoice(in.Playbook)
	if err != nil {
		return nil, zip.ErrUnprocessable(err.Error())
	}

	now := time.Now().Unix()
	var rec VentureRecord
	if exists {
		rec = existing
		rec.Resumed = true
		if t := strings.TrimSpace(in.Title); t != "" {
			rec.Title = t
		}
		if th := strings.TrimSpace(in.Thesis); th != "" {
			rec.Thesis = th
		}
		if sp := strings.TrimSpace(in.Sponsor); sp != "" {
			rec.Sponsor = sp
		}
		if ow := strings.TrimSpace(in.Owner); ow != "" {
			rec.Owner = ow
		}
		if stg := strings.TrimSpace(in.Stage); stg != "" {
			rec.Stage = normalizeVentureStage(stg)
		}
		if !rec.Setup.BudgetAllocated && in.InitialBudgetMicroUSD > 0 {
			rec.InitialBudgetMicroUSD = in.InitialBudgetMicroUSD
			rec.Setup.BudgetAllocated = true
		}
		if in.TeamSeeded {
			rec.Setup.TeamSeeded = true
		}
		if len(in.Members) > 0 {
			rec.Setup.Members = mergeEvidence(rec.Setup.Members, in.Members)
			rec.Members = rec.Setup.Members
		}
		rec.Evidence = mergeEvidence(rec.Evidence, in.Evidence)
		if len(rec.Evidence) > 0 {
			rec.LatestEvidence = rec.Evidence[len(rec.Evidence)-1]
		}
		rec.UpdatedAt = now
	} else {
		title := strings.TrimSpace(in.Title)
		if title == "" {
			title = slug
		}
		ventureOrg := strings.TrimSpace(in.VentureOrg)
		if ventureOrg == "" {
			ventureOrg = studioOrg + "--" + slug
		}
		if _, err := principal.OrgNamespace(ventureOrg, ""); err != nil {
			return nil, zip.ErrBadRequest("invalid ventureOrg: " + err.Error())
		}
		ev := mergeEvidence(nil, in.Evidence)
		latest := ""
		if len(ev) > 0 {
			latest = ev[len(ev)-1]
		}
		members := mergeEvidence(nil, in.Members)
		rec = VentureRecord{
			Slug:                  slug,
			VentureOrg:            ventureOrg,
			StudioOrg:             studioOrg,
			Title:                 title,
			Thesis:                strings.TrimSpace(in.Thesis),
			Sponsor:               strings.TrimSpace(in.Sponsor),
			Owner:                 strings.TrimSpace(in.Owner),
			Stage:                 normalizeVentureStage(in.Stage),
			Status:                "active",
			Imported:              in.Imported,
			PlaybookVersion:       bp.Version,
			InitialBudgetMicroUSD: in.InitialBudgetMicroUSD,
			Evidence:              ev,
			LatestEvidence:        latest,
			Members:               members,
			Setup: VentureSetupState{
				OrgProvisioned:  true,
				BudgetAllocated: in.InitialBudgetMicroUSD > 0,
				TeamSeeded:      in.TeamSeeded,
				Members:         members,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	// Provision the isolated venture store and enroll the playbook if not already enrolled.
	ventureStore, err := storeFor(o.s.State.stores, rec.VentureOrg)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide venture store: %v", err)
	}
	rec.Setup.OrgProvisioned = true
	if _, hasCustom, err := ventureStore.GetCurriculum(ctx); err == nil && !hasCustom {
		if err := ventureStore.SetCurriculum(ctx, rawPlaybook, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "guide enroll playbook: %v", err)
		}
	}
	rec.Setup.PlaybookEnrolled = true
	if rec.PlaybookVersion == "" {
		rec.PlaybookVersion = bp.Version
	}

	if err := studioStore.SaveVenture(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide save venture: %v", err)
	}
	if err := o.hydrateVenture(ctx, &rec); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide hydrate venture: %v", err)
	}
	return &rec, nil
}

// ListVentures returns every venture in the caller studio organization's
// portfolio that the caller is authorized to access, with its live checklist
// progress and shared studio templates.
func (o ops) listVentures(ctx context.Context, _ *cloud.Unit) (*ventureList, error) {
	studioOrg, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	studioStore, err := storeFor(o.s.State.stores, studioOrg)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide studio store: %v", err)
	}
	rows, err := studioStore.ListVentures(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide list ventures: %v", err)
	}
	visible := make([]VentureRecord, 0, len(rows))
	for i := range rows {
		if !callerCanAccessVenture(ctx, studioOrg, rows, rows[i]) {
			continue
		}
		_ = o.hydrateVenture(ctx, &rows[i])
		visible = append(visible, rows[i])
	}
	return &ventureList{Ventures: visible}, nil
}

// GetVenture returns one venture from the caller studio's portfolio by slug,
// including its setup checkpoints, live checklist progress, and shared templates.
func (o ops) getVenture(ctx context.Context, in *ventureRef) (*VentureRecord, error) {
	studioOrg, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	studioStore, err := storeFor(o.s.State.stores, studioOrg)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide studio store: %v", err)
	}
	rec, ok, err := studioStore.GetVenture(ctx, strings.TrimSpace(in.Slug))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide get venture: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("venture not found")
	}
	allRows, _ := studioStore.ListVentures(ctx)
	if !callerCanAccessVenture(ctx, studioOrg, allRows, rec) {
		return nil, zip.ErrForbidden("caller is not assigned to this venture")
	}
	if err := o.hydrateVenture(ctx, &rec); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide hydrate venture: %v", err)
	}
	return &rec, nil
}

// UpdateVentureLifecycle executes a portfolio lifecycle operation on a venture
// (pause, resume, advance, form, export, sync, offboard, assign_member, or
// revoke_member) and updates its operational telemetry (spend, pending
// decisions, failed runs, latest evidence, or company formation ID).
func (o ops) updateVentureLifecycle(ctx context.Context, in *ventureLifecycleIn) (*ventureLifecycleOut, error) {
	studioOrg, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	studioStore, err := storeFor(o.s.State.stores, studioOrg)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide studio store: %v", err)
	}
	slug := strings.TrimSpace(in.Slug)
	rec, ok, err := studioStore.GetVenture(ctx, slug)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide get venture: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("venture not found")
	}

	action := strings.ToLower(strings.TrimSpace(in.Action))
	var bundle *ventureExportBundle
	switch action {
	case "pause":
		rec.Status = "paused"
	case "resume":
		rec.Status = "active"
	case "advance":
		if strings.TrimSpace(in.Stage) != "" {
			rec.Stage = normalizeVentureStage(in.Stage)
		} else {
			rec.Stage = nextVentureStage(rec.Stage)
		}
	case "form":
		rec.Stage = "formation"
		if strings.TrimSpace(in.FormationID) != "" {
			rec.FormationID = strings.TrimSpace(in.FormationID)
			rec.Status = "formed"
		} else {
			rec.Status = "forming"
		}
	case "offboard":
		rec.Status = "offboarded"
	case "sync":
		if strings.TrimSpace(in.Stage) != "" {
			rec.Stage = normalizeVentureStage(in.Stage)
		}
	case "assign_member":
		m := strings.TrimSpace(in.Member)
		if m == "" {
			return nil, zip.ErrBadRequest("member is required when action is assign_member")
		}
		rec.Setup.Members = mergeEvidence(rec.Setup.Members, []string{m})
		rec.Setup.RevokedMembers = removeMember(rec.Setup.RevokedMembers, m)
		rec.Members = rec.Setup.Members
	case "revoke_member":
		m := strings.TrimSpace(in.Member)
		if m == "" {
			return nil, zip.ErrBadRequest("member is required when action is revoke_member")
		}
		rec.Setup.Members = removeMember(rec.Setup.Members, m)
		rec.Setup.RevokedMembers = mergeEvidence(rec.Setup.RevokedMembers, []string{m})
		rec.Members = rec.Setup.Members
	case "export":
		b, err := o.exportVenture(ctx, rec)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "guide export venture: %v", err)
		}
		bundle = b
	default:
		return nil, zip.ErrBadRequest("action must be pause, resume, advance, form, export, sync, offboard, assign_member, or revoke_member")
	}

	if in.SpendMicroUSD != nil {
		if *in.SpendMicroUSD < 0 {
			return nil, zip.ErrBadRequest("spendMicroUSD cannot be negative")
		}
		rec.SpendMicroUSD = *in.SpendMicroUSD
	}
	if in.PendingDecisions != nil {
		rec.PendingDecisions = max(*in.PendingDecisions, 0)
	}
	if in.FailedRuns != nil {
		rec.FailedRuns = max(*in.FailedRuns, 0)
	}
	if ev := strings.TrimSpace(in.LatestEvidence); ev != "" {
		rec.Evidence = mergeEvidence(rec.Evidence, []string{ev})
		rec.LatestEvidence = ev
	}
	if in.TeamSeeded != nil {
		rec.Setup.TeamSeeded = *in.TeamSeeded
	}
	rec.UpdatedAt = time.Now().Unix()

	if err := studioStore.SaveVenture(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide save venture: %v", err)
	}
	if err := o.hydrateVenture(ctx, &rec); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide hydrate venture: %v", err)
	}
	return &ventureLifecycleOut{Venture: rec, Export: bundle}, nil
}

// Portfolio returns the studio's portfolio operations rollup: stage, status,
// checklist progress, decisions waiting on people, latest evidence, failed runs,
// and spend for every venture operated by the studio organization.
func (o ops) portfolio(ctx context.Context, _ *cloud.Unit) (*portfolioView, error) {
	studioOrg, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	studioStore, err := storeFor(o.s.State.stores, studioOrg)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide studio store: %v", err)
	}
	rows, err := studioStore.ListVentures(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide list ventures: %v", err)
	}
	out := &portfolioView{
		StudioOrg:     studioOrg,
		TotalVentures: len(rows),
		Ventures:      rows,
	}
	for i := range out.Ventures {
		_ = o.hydrateVenture(ctx, &out.Ventures[i])
		v := out.Ventures[i]
		switch v.Status {
		case "active":
			out.ActiveVentures++
		case "paused":
			out.PausedVentures++
		case "forming", "formed":
			out.FormedVentures++
		case "offboarded":
			out.OffboardedVentures++
		}
		out.TotalBudgetMicroUSD += v.InitialBudgetMicroUSD
		out.TotalSpendMicroUSD += v.SpendMicroUSD
		out.PendingDecisions += v.PendingDecisions
		out.FailedRuns += v.FailedRuns
	}
	return out, nil
}

func (o ops) hydrateVenture(ctx context.Context, rec *VentureRecord) error {
	vStore, cur, custom, rows, err := snapshotFor(o.s, ctx, rec.VentureOrg)
	if err != nil {
		return err
	}
	ov := buildOverview(cur, custom, rows)
	rec.Progress = ov.Progress
	bp, _ := o.s.State.resolveBlueprint(ctx, vStore)
	if len(bp.Templates) > 0 {
		templates := make([]Page, 0, len(bp.Templates))
		for _, t := range bp.Templates {
			if on(t.Enabled) {
				templates = append(templates, t)
			}
		}
		rec.Templates = templates
	}
	return nil
}

func (o ops) exportVenture(ctx context.Context, rec VentureRecord) (*ventureExportBundle, error) {
	vStore, cur, custom, rows, err := snapshotFor(o.s, ctx, rec.VentureOrg)
	if err != nil {
		return nil, err
	}
	ov := buildOverview(cur, custom, rows)
	acts, err := vStore.ListActions(ctx, listActionsLimit)
	if err != nil {
		return nil, err
	}
	return &ventureExportBundle{
		VentureOrg:      rec.VentureOrg,
		PlaybookVersion: ov.Version,
		Steps:           ov.Steps,
		Actions:         acts,
	}, nil
}

func resolvePlaybookChoice(choice string) (Blueprint, []byte, error) {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		choice = "venture"
	}
	if bp, ok := brandBlueprint(choice); ok {
		raw, err := json.Marshal(bp)
		return bp, raw, err
	}
	bp, err := Parse([]byte(choice))
	if err != nil {
		return Blueprint{}, nil, fmt.Errorf("unknown brand playbook or invalid curriculum %q: %w", choice, err)
	}
	raw, err := json.Marshal(bp)
	return bp, raw, err
}

func normalizeVentureStage(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "intake", "discovery", "validation", "formation", "scaling":
		return strings.ToLower(strings.TrimSpace(stage))
	default:
		return "intake"
	}
}

func nextVentureStage(stage string) string {
	switch stage {
	case "intake":
		return "discovery"
	case "discovery":
		return "validation"
	case "validation":
		return "formation"
	case "formation":
		return "scaling"
	default:
		return "scaling"
	}
}

func mergeEvidence(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, list := range [][]string{existing, incoming} {
		for _, item := range list {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func removeMember(existing []string, target string) []string {
	target = strings.TrimSpace(target)
	if len(existing) == 0 || target == "" {
		return existing
	}
	out := make([]string, 0, len(existing))
	for _, m := range existing {
		if !sameMemberSubject(m, target) {
			out = append(out, m)
		}
	}
	return out
}

func sameMemberSubject(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "person:")
	b = strings.TrimPrefix(strings.TrimSpace(b), "person:")
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	if _, ua, ok := strings.Cut(a, "/"); ok && !strings.Contains(b, "/") && strings.EqualFold(ua, b) {
		return true
	}
	if _, ub, ok := strings.Cut(b, "/"); ok && !strings.Contains(a, "/") && strings.EqualFold(a, ub) {
		return true
	}
	return false
}

func callerCanAccessVenture(ctx context.Context, studioOrg string, allVentures []VentureRecord, v VentureRecord) bool {
	sub, _ := principal.SubjectFrom(ctx)
	sub = strings.TrimSpace(sub)
	if sub == "" || sub == studioOrg || sub == "org:"+studioOrg || sub == "u-"+studioOrg {
		return true
	}
	if sameMemberSubject(sub, v.Sponsor) || sameMemberSubject(sub, v.Owner) {
		return true
	}
	for _, rev := range v.Setup.RevokedMembers {
		if sameMemberSubject(sub, rev) {
			return false
		}
	}
	for _, m := range v.Setup.Members {
		if sameMemberSubject(sub, m) {
			return true
		}
	}
	if len(v.Setup.Members) > 0 {
		return false
	}
	// If any venture in the studio has seat-scoped members or revocations, a
	// non-sponsor/non-owner person can access only ventures they are assigned to.
	for _, other := range allVentures {
		if len(other.Setup.Members) > 0 || len(other.Setup.RevokedMembers) > 0 {
			return false
		}
	}
	return true
}

// SaveVenture upserts a venture record in the studio organization's guide database.
func (s *Store) SaveVenture(ctx context.Context, v VentureRecord) error {
	evJSON, err := json.Marshal(v.Evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	setupJSON, err := json.Marshal(v.Setup)
	if err != nil {
		return fmt.Errorf("marshal setup: %w", err)
	}
	imported := 0
	if v.Imported {
		imported = 1
	}
	if err := dialect.Text(v.Slug, v.VentureOrg, v.StudioOrg, v.Title, v.Thesis, v.Sponsor, v.Owner, v.Stage, v.Status,
		v.PlaybookVersion, string(evJSON), v.LatestEvidence, v.FormationID, string(setupJSON)); err != nil {
		return fmt.Errorf("save venture: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO guide_ventures (
			slug, venture_org, studio_org, title, thesis, sponsor, owner,
			stage, status, imported, playbook_version, initial_budget_micro_usd,
			spend_micro_usd, pending_decisions, failed_runs, evidence,
			latest_evidence, formation_id, setup_json, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT(slug) DO UPDATE SET
			title=excluded.title,
			thesis=excluded.thesis,
			sponsor=excluded.sponsor,
			owner=excluded.owner,
			stage=excluded.stage,
			status=excluded.status,
			imported=excluded.imported,
			playbook_version=excluded.playbook_version,
			initial_budget_micro_usd=excluded.initial_budget_micro_usd,
			spend_micro_usd=excluded.spend_micro_usd,
			pending_decisions=excluded.pending_decisions,
			failed_runs=excluded.failed_runs,
			evidence=excluded.evidence,
			latest_evidence=excluded.latest_evidence,
			formation_id=excluded.formation_id,
			setup_json=excluded.setup_json,
			updated_at=excluded.updated_at`,
		v.Slug, v.VentureOrg, v.StudioOrg, v.Title, v.Thesis, v.Sponsor, v.Owner,
		v.Stage, v.Status, imported, v.PlaybookVersion, v.InitialBudgetMicroUSD,
		v.SpendMicroUSD, v.PendingDecisions, v.FailedRuns, string(evJSON),
		v.LatestEvidence, v.FormationID, string(setupJSON), v.CreatedAt, v.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("save venture: %w", err)
	}
	return nil
}

// GetVenture loads one venture record by slug from the studio organization's database.
func (s *Store) GetVenture(ctx context.Context, slug string) (VentureRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT slug, venture_org, studio_org, title, thesis, sponsor, owner,
		       stage, status, imported, playbook_version, initial_budget_micro_usd,
		       spend_micro_usd, pending_decisions, failed_runs, evidence,
		       latest_evidence, formation_id, setup_json, created_at, updated_at
		FROM guide_ventures WHERE slug = $1`, slug)
	v, err := scanVenture(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return VentureRecord{}, false, nil
	}
	if err != nil {
		return VentureRecord{}, false, err
	}
	return v, true, nil
}

// ListVentures returns every venture registered in the studio organization's database.
func (s *Store) ListVentures(ctx context.Context) ([]VentureRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT slug, venture_org, studio_org, title, thesis, sponsor, owner,
		       stage, status, imported, playbook_version, initial_budget_micro_usd,
		       spend_micro_usd, pending_decisions, failed_runs, evidence,
		       latest_evidence, formation_id, setup_json, created_at, updated_at
		FROM guide_ventures ORDER BY created_at ASC, slug ASC`)
	if err != nil {
		return nil, fmt.Errorf("list ventures: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]VentureRecord, 0, 8)
	for rows.Next() {
		v, err := scanVenture(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanVenture(scan func(dest ...any) error) (VentureRecord, error) {
	var v VentureRecord
	var imported int
	var evJSON, setupJSON string
	if err := scan(
		&v.Slug, &v.VentureOrg, &v.StudioOrg, &v.Title, &v.Thesis, &v.Sponsor, &v.Owner,
		&v.Stage, &v.Status, &imported, &v.PlaybookVersion, &v.InitialBudgetMicroUSD,
		&v.SpendMicroUSD, &v.PendingDecisions, &v.FailedRuns, &evJSON,
		&v.LatestEvidence, &v.FormationID, &setupJSON, &v.CreatedAt, &v.UpdatedAt,
	); err != nil {
		return VentureRecord{}, err
	}
	v.Imported = imported != 0
	_ = json.Unmarshal([]byte(evJSON), &v.Evidence)
	if v.Evidence == nil {
		v.Evidence = []string{}
	}
	_ = json.Unmarshal([]byte(setupJSON), &v.Setup)
	v.Members = v.Setup.Members
	return v, nil
}
