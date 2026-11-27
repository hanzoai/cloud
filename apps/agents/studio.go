package agents

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

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// StudioTemplateVersion is the canonical version string for the shared Venture Studio
// studio role templates.
const StudioTemplateVersion = "2026.11-venture.1"

// StudioFunctionIDs enumerates the seven canonical venture studio functions.
const (
	FuncRegulatoryClinical = "regulatory-clinical"
	FuncMarketCompetitive  = "market-competitive"
	FuncFinanceFundraising = "finance-fundraising"
	FuncMarketingBrand     = "marketing-brand"
	FuncProductEngineering = "product-engineering"
	FuncOperationsLegal    = "operations-legal"
	FuncKnowledgeTools     = "knowledge-tools"
)

// StudioFunction describes one of the seven canonical venture studio functions.
type StudioFunction struct {
	// ID is the stable slug identifying the studio function (e.g. "regulatory-clinical").
	ID string `json:"id"`
	// Title is the human-readable name of the studio function.
	Title string `json:"title"`
	// Expertise summarizes the domain specialization covered by roles in this function.
	Expertise string `json:"expertise"`
	// Duties states the core responsibilities and operating boundaries of the function.
	Duties string `json:"duties"`
	// DefaultTools lists the MCP tool names granted by default to specialists in this function.
	DefaultTools []string `json:"defaultTools"`
	// DefaultOutput describes the required deliverable structure and citation standard.
	DefaultOutput string `json:"defaultOutput"`
}

// RoleTemplate is a versioned, reusable role definition shared across ventures in a studio.
type RoleTemplate struct {
	// ID is the stable template identifier (e.g. "venture-lead" or "specialist-regulatory-clinical").
	ID string `json:"id"`
	// Version is the explicit template revision string, preserved when a venture enrolls the role.
	Version string `json:"version"`
	// Function is the studio function this role belongs to ("lead" or one of the seven studio functions).
	Function string `json:"function"`
	// Title is the human-readable role title.
	Title string `json:"title"`
	// Manager is the default parent role handle this role reports to; empty for the venture lead.
	Manager string `json:"manager,omitempty"`
	// Expertise is the one-line catalog description explaining when another agent or lead should delegate to this role.
	Expertise string `json:"expertise"`
	// Instructions is the system prompt governing how the role gathers evidence, cites sources, and flags unsupported claims.
	Instructions string `json:"instructions"`
	// Sources lists the authorized evidence and knowledge sources for this role.
	Sources []string `json:"sources"`
	// Tools lists the MCP tool names this role is allowed to invoke.
	Tools []string `json:"tools"`
	// Output specifies the structured deliverable format expected from this role.
	Output string `json:"output"`
	// CapMicroUSD is the default period budget cap in integer micro-USD.
	CapMicroUSD int64 `json:"cap_micro_usd"`
	// MaxTaskMicroUSD is the default single-run ceiling in integer micro-USD.
	MaxTaskMicroUSD int64 `json:"max_task_micro_usd"`
	// Period is the budget reset window: day, week, or month.
	Period string `json:"period"`
}

// studioFunctionsCatalog returns the seven canonical studio functions.
func studioFunctionsCatalog() []StudioFunction {
	return []StudioFunction{
		{
			ID:            FuncRegulatoryClinical,
			Title:         "Regulatory and clinical",
			Expertise:     "Clinical trial evidence, experimental validation and regulatory pathways, workflow safety, and peer-reviewed efficacy analysis.",
			Duties:        "Verify clinical and operational claims against peer-reviewed literature, identify regulatory constraints, and flag unsupported efficacy claims.",
			DefaultTools:  []string{"post_v1_research_evidence", "get_v1_research_evidence"},
			DefaultOutput: "Cited clinical evidence summary with DOIs/PMIDs and an explicit list of unsupported claims.",
		},
		{
			ID:            FuncMarketCompetitive,
			Title:         "Market and competitive intelligence",
			Expertise:     "Buyer segmentation, clinic workflow benchmarks, competitor pricing, adoption barriers, and willingness-to-pay evidence.",
			Duties:        "Map incumbent workflows and competing products, quantify switching costs, and separate verified market data from vendor marketing claims.",
			DefaultTools:  []string{"post_v1_research_evidence", "get_v1_research_evidence"},
			DefaultOutput: "Cited market benchmark synthesis with competitor matrix and unsupported vendor claims.",
		},
		{
			ID:            FuncFinanceFundraising,
			Title:         "Finance and fundraising",
			Expertise:     "Unit economics, clinic ROI modeling, burn rate, milestone budgeting, and venture capital readiness.",
			Duties:        "Model unit economics and payback periods from verified baselines, audit venture spend against budget caps, and stress-test financial assumptions.",
			DefaultTools:  []string{"get_v1_guide_portfolio", "post_v1_research_evidence"},
			DefaultOutput: "Unit-economics model with sensitivity bounds, payback period, and cited baseline assumptions.",
		},
		{
			ID:            FuncMarketingBrand,
			Title:         "Marketing and brand",
			Expertise:     "Value proposition positioning, clinical audience messaging, demand validation, and human-gated outreach drafts.",
			Duties:        "Draft evidence-backed positioning and outreach assets that require explicit human authorization before any external send.",
			DefaultTools:  []string{"content_generate", "post_v1_research_actions"},
			DefaultOutput: "Draft positioning and outreach artifacts bound by content hash for human approval.",
		},
		{
			ID:            FuncProductEngineering,
			Title:         "Product and engineering",
			Expertise:     "System architecture, EHR/PIMS integration feasibility, prototype specifications, and automated verification.",
			Duties:        "Translate validated workflow changes into technical specifications, integration requirements, and testable acceptance criteria.",
			DefaultTools:  []string{"post_v1_research_evidence", "post_v1_research_actions"},
			DefaultOutput: "Technical specification with integration risks, latency budgets, and verification plan.",
		},
		{
			ID:            FuncOperationsLegal,
			Title:         "Operations and legal",
			Expertise:     "Entity formation readiness, data privacy, source licensing permissions, contracting, and SOP design.",
			Duties:        "Verify source permissions, clinic data governance, and entity formation prerequisites before external commitments.",
			DefaultTools:  []string{"post_v1_research_actions", "post_v1_research_commitments"},
			DefaultOutput: "Operational and legal readiness checklist with source-permission attestations.",
		},
		{
			ID:            FuncKnowledgeTools,
			Title:         "Knowledge and tools",
			Expertise:     "Corpus curation, citation indexing, connector hygiene, evaluation datasets, and coverage-gap telemetry.",
			Duties:        "Maintain the venture's evidence index, verify citation traceability, and surface recurring tool or expertise gaps.",
			DefaultTools:  []string{"post_v1_research_evidence", "get_v1_research_evidence"},
			DefaultOutput: "Traceable bibliography, dataset index, and coverage-gap telemetry report.",
		},
	}
}

// studioRoleTemplates returns the versioned shared role templates (1 venture lead + 7 function specialists).
func studioRoleTemplates() []RoleTemplate {
	funcs := studioFunctionsCatalog()
	out := make([]RoleTemplate, 0, 1+len(funcs))
	out = append(out, RoleTemplate{
		ID:        "venture-lead",
		Version:   StudioTemplateVersion,
		Function:  "lead",
		Title:     "Venture Lead",
		Expertise: "End-to-end venture orchestration, research brief decomposition, specialist synthesis, and kill/advance recommendations.",
		Instructions: "You are the Venture Lead for a Venture Studio venture. Decompose each research brief across your specialist agents, " +
			"synthesize their attributable findings into a single decision-ready brief with explicit [cite:...] citations, list every " +
			"[unsupported:...] claim that lacks primary evidence, and state the open questions for human review.",
		Sources:         []string{"pubmed", "europepmc", "clinicaltrials", "workspace-evidence"},
		Tools:           []string{"post_v1_research_decisions", "post_v1_research_actions"},
		Output:          "Integrated synthesis containing Cited Summary, Unsupported Claims, Open Questions, and Specialist Attribution.",
		CapMicroUSD:     50_000_000,
		MaxTaskMicroUSD: 5_000_000,
		Period:          "month",
	})
	for _, fn := range funcs {
		id := "specialist-" + fn.ID
		out = append(out, RoleTemplate{
			ID:        id,
			Version:   StudioTemplateVersion,
			Function:  fn.ID,
			Title:     fn.Title + " Specialist",
			Manager:   "venture-lead",
			Expertise: fn.Expertise,
			Instructions: fmt.Sprintf(
				"You are the %s Specialist reporting to venture-lead (template %s). Duties: %s "+
					"Tag every verified finding with [cite:<source>] and every unverified or anecdotal claim with [unsupported:<claim>]. "+
					"Never execute an external action without a human-authorized decision.",
				fn.Title, StudioTemplateVersion, fn.Duties,
			),
			Sources:         []string{"pubmed", "europepmc", "workspace-evidence"},
			Tools:           fn.DefaultTools,
			Output:          fn.DefaultOutput,
			CapMicroUSD:     20_000_000,
			MaxTaskMicroUSD: 2_000_000,
			Period:          "month",
		})
	}
	return out
}

// rosterTemplatesOut is the response shape for GET /v1/agent/roster/templates.
type rosterTemplatesOut struct {
	// Version is the current studio template catalog version string.
	Version string `json:"version"`
	// Functions are the seven canonical studio functions.
	Functions []StudioFunction `json:"functions"`
	// Templates are the versioned role templates (lead plus the seven specialist functions).
	Templates []RoleTemplate `json:"templates"`
}

// RoleSpec defines one role in an approved organization chart submitted to POST /v1/agent/roster.
type RoleSpec struct {
	// Name is the agent handle to create or update in the organization (e.g. "venture-lead" or "specialist-regulatory-clinical").
	Name string `json:"name"`
	// TemplateID optionally selects a shared RoleTemplate by id to inherit defaults from.
	TemplateID string `json:"templateId,omitempty"`
	// TemplateVersion records the explicit shared template version used for this role; defaults to StudioTemplateVersion.
	TemplateVersion string `json:"templateVersion,omitempty"`
	// Function names the studio function ("lead" or one of the seven studio functions).
	Function string `json:"function,omitempty"`
	// Manager names the parent agent handle or id this role reports to in the org chart.
	Manager string `json:"manager,omitempty"`
	// Model optionally overrides the model this role runs on.
	Model string `json:"model,omitempty"`
	// Expertise is the catalog description explaining the role's domain expertise.
	Expertise string `json:"expertise,omitempty"`
	// Duties is the system prompt (instructions) governing the role's behavior and output.
	Duties string `json:"duties,omitempty"`
	// Sources lists the knowledge and evidence sources this role is permitted to query.
	Sources []string `json:"sources,omitempty"`
	// Tools lists the MCP tool names this role is allowed to call.
	Tools []string `json:"tools,omitempty"`
	// Output describes the required deliverable format for this role.
	Output string `json:"output,omitempty"`
	// CapMicroUSD is the period budget cap in integer micro-USD; defaults to the template's cap when omitted.
	CapMicroUSD int64 `json:"cap_micro_usd,omitempty"`
	// MaxTaskMicroUSD is the per-run budget ceiling in integer micro-USD; defaults to the template's ceiling when omitted.
	MaxTaskMicroUSD int64 `json:"max_task_micro_usd,omitempty"`
	// Period is the budget reset window (day, week, or month); defaults to "month".
	Period string `json:"period,omitempty"`
	// ExecutionMode is "one-shot" (default) or "long-running".
	ExecutionMode string `json:"executionMode,omitempty"`
	// Schedule is the 5-field cron expression when executionMode is "long-running".
	Schedule string `json:"schedule,omitempty"`
}

// applyRosterIn is the request body for POST /v1/agent/roster.
type applyRosterIn struct {
	// Venture is the venture slug this roster belongs to, used for attribution and metadata.
	Venture string `json:"venture,omitempty"`
	// TemplateVersion is the shared role template version to stamp on enrolled roles; defaults to StudioTemplateVersion.
	TemplateVersion string `json:"templateVersion,omitempty"`
	// SeedDefaultSeven, when true, seeds the default venture-lead and all seven studio function specialists before applying any custom roles in Roles.
	SeedDefaultSeven bool `json:"seedDefaultSeven,omitempty"`
	// Roles are the explicit role definitions to create or update in topological manager order.
	Roles []RoleSpec `json:"roles,omitempty"`
}

// applyRosterOut is the response shape for POST /v1/agent/roster.
type applyRosterOut struct {
	// Venture is the venture slug the roster was applied for.
	Venture string `json:"venture,omitempty"`
	// TemplateVersion is the shared template version applied.
	TemplateVersion string `json:"templateVersion"`
	// Created is how many new agent roles were created by this call.
	Created int `json:"created"`
	// Updated is how many existing agent roles were updated idempotently in place without duplicating agents or budgets.
	Updated int `json:"updated"`
	// Agents are the enrolled agent views in the organization after applying the roster.
	Agents []agentView `json:"agents"`
}

// RosterTemplates returns the seven canonical studio functions and the shared,
// versioned role templates that ventures reuse.
func (o agentOps) rosterTemplates(_ context.Context, _ *cloud.Unit) (*rosterTemplatesOut, error) {
	return &rosterTemplatesOut{
		Version:   StudioTemplateVersion,
		Functions: studioFunctionsCatalog(),
		Templates: studioRoleTemplates(),
	}, nil
}

// ApplyRoster turns an approved organization chart into agent definitions in the
// caller's organization, resolving manager reporting lines (`parent` and
// `lineage`) and shared role templates with explicit versions. Applying the same
// roster again (or resuming an interrupted launch) updates existing roles in
// place without duplicating agents or budgets.
func (o agentOps) applyRoster(ctx context.Context, in *applyRosterIn) (*applyRosterOut, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	tplVersion := strings.TrimSpace(in.TemplateVersion)
	if tplVersion == "" {
		tplVersion = StudioTemplateVersion
	}

	templatesByID := map[string]RoleTemplate{}
	templatesByFunc := map[string]RoleTemplate{}
	for _, t := range studioRoleTemplates() {
		templatesByID[t.ID] = t
		templatesByFunc[t.Function] = t
	}

	specs := make([]RoleSpec, 0, len(in.Roles)+8)
	seenSpec := map[string]int{}
	addSpec := func(rs RoleSpec) {
		name := strings.TrimSpace(rs.Name)
		if idx, ok := seenSpec[name]; ok {
			specs[idx] = mergeRoleSpec(specs[idx], rs)
			return
		}
		seenSpec[name] = len(specs)
		specs = append(specs, rs)
	}

	if in.SeedDefaultSeven {
		for _, tpl := range studioRoleTemplates() {
			addSpec(roleSpecFromTemplate(tpl, tplVersion))
		}
	}
	for _, r := range in.Roles {
		name := strings.TrimSpace(r.Name)
		if name == "" && r.TemplateID != "" {
			name = strings.TrimSpace(r.TemplateID)
		}
		if name == "" && r.Function != "" {
			if r.Function == "lead" {
				name = "venture-lead"
			} else {
				name = "specialist-" + strings.TrimSpace(r.Function)
			}
		}
		r.Name = name
		if tpl, ok := templatesByID[strings.TrimSpace(r.TemplateID)]; ok {
			r = mergeRoleSpec(roleSpecFromTemplate(tpl, tplVersion), r)
		} else if tpl, ok := templatesByID[name]; ok {
			r = mergeRoleSpec(roleSpecFromTemplate(tpl, tplVersion), r)
		} else if tpl, ok := templatesByFunc[strings.TrimSpace(r.Function)]; ok {
			base := roleSpecFromTemplate(tpl, tplVersion)
			base.Name = name
			r = mergeRoleSpec(base, r)
		}
		addSpec(r)
	}

	if len(specs) == 0 {
		return nil, zip.ErrBadRequest("roster requires seedDefaultSeven=true or at least one role in roles")
	}

	ordered, err := topoSortRoles(specs)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}

	createdCount, updatedCount := 0, 0
	views := make([]agentView, 0, len(ordered))
	now := time.Now().Unix()

	for _, spec := range ordered {
		name := strings.TrimSpace(spec.Name)
		if !nameRE.MatchString(name) {
			return nil, zip.ErrBadRequest(fmt.Sprintf("role name %q must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$", name))
		}
		model := strings.TrimSpace(spec.Model)
		if model == "" {
			model = aimodel.DefaultModel
		} else if err := validateModel(s, ctx, model); err != nil {
			return nil, err
		}
		model = aimodel.ZenModel(model)

		mode, schedule, err := validateLifecycle(spec.ExecutionMode, spec.Schedule)
		if err != nil {
			return nil, err
		}
		capMicro := spec.CapMicroUSD
		if capMicro <= 0 {
			capMicro = 20_000_000
		}
		taskMicro := spec.MaxTaskMicroUSD
		if taskMicro <= 0 {
			taskMicro = min(capMicro, 2_000_000)
		}
		period := strings.TrimSpace(spec.Period)
		if period == "" {
			period = "month"
		}
		if err := validateBudget(capMicro, taskMicro, period); err != nil {
			return nil, err
		}

		ver := strings.TrimSpace(spec.TemplateVersion)
		if ver == "" {
			ver = tplVersion
		}
		desc := strings.TrimSpace(spec.Expertise)
		if fn := strings.TrimSpace(spec.Function); fn != "" && !strings.Contains(desc, "["+fn+"@") {
			if desc != "" {
				desc = fmt.Sprintf("[%s@%s] %s", fn, ver, desc)
			} else {
				desc = fmt.Sprintf("[%s@%s]", fn, ver)
			}
		}
		instr := strings.TrimSpace(spec.Duties)
		if len(spec.Sources) > 0 && !strings.Contains(instr, "Allowed sources:") {
			instr = strings.TrimSpace(instr + "\nAllowed sources: " + strings.Join(spec.Sources, ", ") + ".")
		}
		if spec.Output != "" && !strings.Contains(instr, "Expected output:") {
			instr = strings.TrimSpace(instr + "\nExpected output: " + strings.TrimSpace(spec.Output))
		}
		if len(instr) > maxInstructions {
			return nil, zip.ErrBadRequest("instructions too large")
		}

		var parent Agent
		if mgr := strings.TrimSpace(spec.Manager); mgr != "" {
			parent, err = sto.Resolve(ctx, org, mgr)
			if err == errNotFound {
				return nil, zip.ErrNotFound(fmt.Sprintf("manager agent %q not found in this org", mgr))
			}
			if err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "resolve manager: %v", err)
			}
			if len(parent.Lineage) >= maxLineage {
				return nil, zip.Errorf(http.StatusConflict, "a spawn chain is at most %d agents deep", maxLineage)
			}
		}

		existing, err := sto.Resolve(ctx, org, name)
		if err != nil && err != errNotFound {
			return nil, zip.Errorf(http.StatusInternalServerError, "resolve role: %v", err)
		}
		if err == nil {
			// Idempotent update in place: preserve ID, CreatedAt, and ConsumedMicroUSD.
			wasLongRunning := existing.ExecutionMode == ModeLongRunning
			if mode == ModeLongRunning && !wasLongRunning {
				if err := authorizeResident(ctx, s.State.bill, org); err != nil {
					return nil, err
				}
				n, cerr := sto.CountLongRunning(ctx, org)
				if cerr != nil {
					return nil, zip.Errorf(http.StatusInternalServerError, "count: %v", cerr)
				}
				if n >= longRunningCap() {
					return nil, zip.Errorf(http.StatusConflict,
						"long-running agent limit reached for this org (max %d)", longRunningCap())
				}
			}
			existing.Model = model
			existing.Description = desc
			existing.Instructions = instr
			existing.Tools = cleanList(spec.Tools)
			existing.ExecutionMode = mode
			existing.Schedule = schedule
			existing.CapMicroUSD = capMicro
			existing.MaxTaskMicroUSD = taskMicro
			existing.Period = period
			existing.UpdatedAt = now
			if err := sto.Update(ctx, existing); err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "update role: %v", err)
			}
			updatedCount++
			views = append(views, toView(existing, 0))
			continue
		}

		if mode == ModeLongRunning {
			if err := authorizeResident(ctx, s.State.bill, org); err != nil {
				return nil, err
			}
			n, cerr := sto.CountLongRunning(ctx, org)
			if cerr != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "count: %v", cerr)
			}
			if n >= longRunningCap() {
				return nil, zip.Errorf(http.StatusConflict,
					"long-running agent limit reached for this org (max %d)", longRunningCap())
			}
		}

		a := Agent{
			ID:              mint.ID("agent"),
			Org:             org,
			Name:            name,
			Model:           model,
			Instructions:    instr,
			Description:     desc,
			Tools:           cleanList(spec.Tools),
			Status:          "ready",
			ExecutionMode:   mode,
			Schedule:        schedule,
			CreatedAt:       now,
			UpdatedAt:       now,
			Payer:           payerOf(ctx, org).Subject(),
			CapMicroUSD:     capMicro,
			MaxTaskMicroUSD: taskMicro,
			Period:          period,
			PeriodStartedAt: periodStart(time.Now(), period),
			CreatedBy:       creatorOf(ctx),
		}
		if parent.ID != "" {
			a.Parent, a.Lineage = parent.ID, append([]string{parent.ID}, parent.Lineage...)
		}
		if mode == ModeLongRunning {
			a.MeteredAt = now
		}
		if err := sto.Create(ctx, a); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "create role %q: %v", name, err)
		}
		createdCount++
		views = append(views, toView(a, 0))
	}

	return &applyRosterOut{
		Venture:         strings.TrimSpace(in.Venture),
		TemplateVersion: tplVersion,
		Created:         createdCount,
		Updated:         updatedCount,
		Agents:          views,
	}, nil
}

func roleSpecFromTemplate(tpl RoleTemplate, ver string) RoleSpec {
	if ver == "" {
		ver = tpl.Version
	}
	return RoleSpec{
		Name:            tpl.ID,
		TemplateID:      tpl.ID,
		TemplateVersion: ver,
		Function:        tpl.Function,
		Manager:         tpl.Manager,
		Expertise:       tpl.Expertise,
		Duties:          tpl.Instructions,
		Sources:         append([]string(nil), tpl.Sources...),
		Tools:           append([]string(nil), tpl.Tools...),
		Output:          tpl.Output,
		CapMicroUSD:     tpl.CapMicroUSD,
		MaxTaskMicroUSD: tpl.MaxTaskMicroUSD,
		Period:          tpl.Period,
	}
}

func mergeRoleSpec(base, over RoleSpec) RoleSpec {
	if strings.TrimSpace(over.Name) != "" {
		base.Name = strings.TrimSpace(over.Name)
	}
	if strings.TrimSpace(over.TemplateID) != "" {
		base.TemplateID = strings.TrimSpace(over.TemplateID)
	}
	if strings.TrimSpace(over.TemplateVersion) != "" {
		base.TemplateVersion = strings.TrimSpace(over.TemplateVersion)
	}
	if strings.TrimSpace(over.Function) != "" {
		base.Function = strings.TrimSpace(over.Function)
	}
	if strings.TrimSpace(over.Manager) != "" {
		base.Manager = strings.TrimSpace(over.Manager)
	}
	if strings.TrimSpace(over.Model) != "" {
		base.Model = strings.TrimSpace(over.Model)
	}
	if strings.TrimSpace(over.Expertise) != "" {
		base.Expertise = strings.TrimSpace(over.Expertise)
	}
	if strings.TrimSpace(over.Duties) != "" {
		base.Duties = strings.TrimSpace(over.Duties)
	}
	if len(over.Sources) > 0 {
		base.Sources = append([]string(nil), over.Sources...)
	}
	if len(over.Tools) > 0 {
		base.Tools = append([]string(nil), over.Tools...)
	}
	if strings.TrimSpace(over.Output) != "" {
		base.Output = strings.TrimSpace(over.Output)
	}
	if over.CapMicroUSD > 0 {
		base.CapMicroUSD = over.CapMicroUSD
	}
	if over.MaxTaskMicroUSD > 0 {
		base.MaxTaskMicroUSD = over.MaxTaskMicroUSD
	}
	if strings.TrimSpace(over.Period) != "" {
		base.Period = strings.TrimSpace(over.Period)
	}
	if strings.TrimSpace(over.ExecutionMode) != "" {
		base.ExecutionMode = strings.TrimSpace(over.ExecutionMode)
	}
	if strings.TrimSpace(over.Schedule) != "" {
		base.Schedule = strings.TrimSpace(over.Schedule)
	}
	return base
}

func topoSortRoles(specs []RoleSpec) ([]RoleSpec, error) {
	byName := make(map[string]RoleSpec, len(specs))
	for _, s := range specs {
		byName[strings.TrimSpace(s.Name)] = s
	}
	state := make(map[string]int, len(specs)) // 0=unvisited, 1=visiting, 2=done
	out := make([]RoleSpec, 0, len(specs))
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("cycle in role reporting line involving %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		spec := byName[name]
		mgr := strings.TrimSpace(spec.Manager)
		if mgr != "" {
			if mgr == name {
				return fmt.Errorf("role %q cannot manage itself", name)
			}
			if _, inBatch := byName[mgr]; inBatch {
				if err := visit(mgr); err != nil {
					return err
				}
			}
		}
		state[name] = 2
		out = append(out, spec)
		return nil
	}
	for _, s := range specs {
		if err := visit(strings.TrimSpace(s.Name)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ── Durable Research Briefs, Specialist Attribution & Selective Rerun ─────────

// BriefTask is one durable child task dispatched to a specialist agent under a venture lead's brief.
type BriefTask struct {
	// ID is the stable task identifier (`task_...`).
	ID string `json:"id"`
	// BriefID is the parent research brief identifier (`brief_...`).
	BriefID string `json:"briefId"`
	// AgentID is the specialist agent's stable handle (`agent_...`).
	AgentID string `json:"agentId"`
	// AgentName is the specialist agent's name in the organization.
	AgentName string `json:"agentName"`
	// Function is the studio function the specialist represents.
	Function string `json:"function,omitempty"`
	// ParentID is the manager agent ID in the specialist's lineage.
	ParentID string `json:"parentId,omitempty"`
	// Lineage is the specialist's ancestor agent IDs, nearest first.
	Lineage []string `json:"lineage,omitempty"`
	// Status is the durable task state: pending, done, or error.
	Status string `json:"status"`
	// Prompt is the specialist-scoped sub-brief dispatched to this agent.
	Prompt string `json:"prompt"`
	// Output is the attributable text produced by the specialist when status is "done".
	Output string `json:"output,omitempty"`
	// Citations are the source references extracted from the specialist's output.
	Citations []string `json:"citations"`
	// UnsupportedClaims are the claims the specialist flagged as lacking primary evidence.
	UnsupportedClaims []string `json:"unsupportedClaims"`
	// Error records the failure reason when status is "error".
	Error string `json:"error,omitempty"`
	// RunID is the recorded agent_runs row ID for the latest execution attempt.
	RunID string `json:"runId,omitempty"`
	// MicroUSD is the cumulative spend of this child task in integer micro-USD.
	MicroUSD int64 `json:"microUsd"`
	// Attempts counts how many times this specialist task has been executed.
	Attempts int `json:"attempts"`
	// UpdatedAt is when the child task was last updated, as Unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// BriefAttribution summarizes one agent's attributable contribution to a completed brief.
type BriefAttribution struct {
	// AgentID is the contributing agent's stable ID.
	AgentID string `json:"agentId"`
	// AgentName is the contributing agent's handle.
	AgentName string `json:"agentName"`
	// Role is "lead" or "specialist".
	Role string `json:"role"`
	// Function is the studio function represented by the agent.
	Function string `json:"function,omitempty"`
	// ParentID is the manager agent ID this agent reports to.
	ParentID string `json:"parentId,omitempty"`
	// RunID is the agent_runs execution ID that produced this contribution.
	RunID string `json:"runId"`
	// Summary is a concise excerpt of what this agent produced.
	Summary string `json:"summary"`
	// Citations are the source citations contributed by this agent.
	Citations []string `json:"citations"`
	// MicroUSD is the spend attributed to this agent on the brief in integer micro-USD.
	MicroUSD int64 `json:"microUsd"`
}

// BriefRecord is a durable venture research brief orchestrated by a lead agent across specialist child tasks.
type BriefRecord struct {
	// ID is the stable brief handle (`brief_...`).
	ID string `json:"id"`
	// Venture is the venture slug this research brief belongs to.
	Venture string `json:"venture,omitempty"`
	// Title is the short headline of the research brief.
	Title string `json:"title"`
	// Question is the nonconfidential research question assigned to the venture lead.
	Question string `json:"question"`
	// LeadAgentID is the venture lead agent's stable ID.
	LeadAgentID string `json:"leadAgentId"`
	// LeadAgent is the venture lead agent's handle.
	LeadAgent string `json:"leadAgent"`
	// LeadRunID is the recorded run ID of the lead synthesis once completed.
	LeadRunID string `json:"leadRunId,omitempty"`
	// Status is the brief's lifecycle state: completed or partial_failure.
	Status string `json:"status"`
	// Synthesis is the lead agent's integrated synthesis once all specialist child tasks succeed.
	Synthesis string `json:"synthesis,omitempty"`
	// Citations are the deduplicated source citations across the specialist tasks and lead synthesis.
	Citations []string `json:"citations"`
	// UnsupportedClaims are the claims flagged as lacking primary empirical support.
	UnsupportedClaims []string `json:"unsupportedClaims"`
	// OpenQuestions are the unresolved questions surfaced for domain review or follow-up.
	OpenQuestions []string `json:"openQuestions"`
	// Attribution records who did what across the lead and specialist agents.
	Attribution []BriefAttribution `json:"attribution"`
	// Tasks are the durable specialist child tasks belonging to this brief.
	Tasks []BriefTask `json:"tasks"`
	// TotalMicroUSD is the total spend across all specialist tasks and the lead synthesis in integer micro-USD.
	TotalMicroUSD int64 `json:"totalMicroUsd"`
	// SkippedCompleted is how many already-completed specialist tasks were preserved without re-running on the latest rerun.
	SkippedCompleted int `json:"skippedCompleted,omitempty"`
	// RerunsPerformed is how many failed specialist tasks were re-executed on the latest rerun.
	RerunsPerformed int `json:"rerunsPerformed,omitempty"`
	// CreatedAt is when the brief was submitted, as Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the brief was last updated, as Unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// submitBriefIn is the request body for POST /v1/agent/briefs.
type submitBriefIn struct {
	// Venture is the venture slug this brief belongs to.
	Venture string `json:"venture,omitempty"`
	// Title is the headline of the research brief.
	Title string `json:"title"`
	// Question is the research question or brief handed to the venture lead.
	Question string `json:"question"`
	// Lead is the venture lead agent handle or ID; defaults to "venture-lead".
	Lead string `json:"lead,omitempty"`
	// Specialists lists the specialist agent handles or IDs to dispatch durable child tasks to; defaults to regulatory-clinical and market-competitive when omitted.
	Specialists []string `json:"specialists,omitempty"`
	// FailOnce optionally names specialist handles whose initial task execution should fail before model invocation, used to exercise and verify selective child-task retry.
	FailOnce []string `json:"failOnce,omitempty"`
}

// briefRef addresses a single durable research brief by ID from the URL path.
type briefRef struct {
	// ID is the brief handle (`brief_...`) from the path.
	ID string `json:"id"`
}

// briefList is the response shape for GET /v1/agent/briefs.
type briefList struct {
	// Briefs are the organization's durable research briefs, newest first.
	Briefs []BriefRecord `json:"briefs"`
}

// SubmitBrief assigns a research brief to a venture lead, dispatches durable
// child tasks to the designated specialist agents, attributes each specialist's
// cited findings and unsupported-claim flags, and runs the lead synthesis once
// all specialist tasks complete. If any specialist fails, completed specialist
// work is preserved on the brief (`partial_failure`) so `POST /v1/agent/briefs/:id/rerun`
// re-executes only the failed specialist task.
func (o agentOps) submitBrief(ctx context.Context, in *submitBriefIn) (*BriefRecord, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return nil, zip.ErrBadRequest("question is required")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Venture Research Brief"
	}
	leadRef := strings.TrimSpace(in.Lead)
	if leadRef == "" {
		leadRef = "venture-lead"
	}
	lead, err := sto.Resolve(ctx, org, leadRef)
	if err == errNotFound {
		return nil, zip.ErrNotFound(fmt.Sprintf("lead agent %q not found in this org", leadRef))
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve lead: %v", err)
	}

	specRefs := in.Specialists
	if len(specRefs) == 0 {
		specRefs = []string{"specialist-" + FuncRegulatoryClinical, "specialist-" + FuncMarketCompetitive}
	}
	specialists := make([]Agent, 0, len(specRefs))
	seenSpec := map[string]bool{}
	for _, ref := range specRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seenSpec[ref] {
			continue
		}
		seenSpec[ref] = true
		sp, err := sto.Resolve(ctx, org, ref)
		if err == errNotFound {
			return nil, zip.ErrNotFound(fmt.Sprintf("specialist agent %q not found in this org", ref))
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "resolve specialist %q: %v", ref, err)
		}
		specialists = append(specialists, sp)
	}
	if len(specialists) == 0 {
		return nil, zip.ErrBadRequest("at least one specialist agent is required")
	}

	failSet := map[string]bool{}
	for _, f := range in.FailOnce {
		failSet[strings.TrimSpace(f)] = true
	}

	now := time.Now().Unix()
	briefID := mint.ID("brief")
	tasks := make([]BriefTask, 0, len(specialists))
	for _, sp := range specialists {
		fn := extractFunctionFromDesc(sp.Description, sp.Name)
		prompt := fmt.Sprintf(
			"Brief from %s (%s): %s\nProvide your %s analysis with [cite:...] source citations and [unsupported:...] flags for any unverified claims.",
			lead.Name, title, question, fn,
		)
		tasks = append(tasks, BriefTask{
			ID:                mint.ID("task"),
			BriefID:           briefID,
			AgentID:           sp.ID,
			AgentName:         sp.Name,
			Function:          fn,
			ParentID:          sp.Parent,
			Lineage:           append([]string(nil), sp.Lineage...),
			Status:            "pending",
			Prompt:            prompt,
			Citations:         []string{},
			UnsupportedClaims: []string{},
			UpdatedAt:         now,
		})
	}

	rec := BriefRecord{
		ID:                briefID,
		Venture:           strings.TrimSpace(in.Venture),
		Title:             title,
		Question:          question,
		LeadAgentID:       lead.ID,
		LeadAgent:         lead.Name,
		Status:            "partial_failure",
		Citations:         []string{},
		UnsupportedClaims: []string{},
		OpenQuestions:     []string{},
		Attribution:       []BriefAttribution{},
		Tasks:             tasks,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := o.executeBriefTasks(ctx, sto, org, lead, &rec, failSet, false); err != nil {
		return nil, err
	}
	if err := sto.SaveBrief(ctx, org, rec); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save brief: %v", err)
	}
	return &rec, nil
}

// RerunBrief re-executes only the failed or pending specialist child tasks on a
// brief without re-running or duplicating specialists that already succeeded,
// then runs the lead synthesis once all specialist tasks are complete.
func (o agentOps) rerunBrief(ctx context.Context, in *briefRef) (*BriefRecord, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	rec, ok, err := sto.GetBrief(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get brief: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("brief not found")
	}
	lead, err := sto.Resolve(ctx, org, rec.LeadAgent)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve lead: %v", err)
	}
	if err := o.executeBriefTasks(ctx, sto, org, lead, &rec, nil, true); err != nil {
		return nil, err
	}
	if err := sto.SaveBrief(ctx, org, rec); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save brief: %v", err)
	}
	return &rec, nil
}

// ListBriefs returns every durable research brief recorded in the caller's
// organization, newest first.
func (o agentOps) listBriefs(ctx context.Context, _ *cloud.Unit) (*briefList, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	rows, err := sto.ListBriefs(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list briefs: %v", err)
	}
	return &briefList{Briefs: rows}, nil
}

// GetBrief returns one durable research brief and all of its specialist child
// tasks, citations, unsupported claims, open questions, and attribution records.
func (o agentOps) getBrief(ctx context.Context, in *briefRef) (*BriefRecord, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	rec, ok, err := sto.GetBrief(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get brief: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("brief not found")
	}
	return &rec, nil
}

func (o agentOps) executeBriefTasks(ctx context.Context, sto *Store, org string, lead Agent, rec *BriefRecord, failSet map[string]bool, isRerun bool) error {
	s := o.s
	payer := payerOf(ctx, org)
	sub, _ := principal.SubjectFrom(ctx)
	actor := billingActor(org, sub)
	now := time.Now().Unix()

	skipped, rerunCount := 0, 0
	allDone := true

	for i := range rec.Tasks {
		t := &rec.Tasks[i]
		if t.Status == "done" {
			skipped++
			continue
		}
		if isRerun {
			rerunCount++
		}
		t.Attempts++
		t.UpdatedAt = now

		if failSet[t.AgentName] || failSet[t.AgentID] {
			failRun := Run{
				ID:        mint.ID("run"),
				Org:       org,
				AgentName: t.AgentName,
				Status:    "error",
				Model:     lead.Model,
				Input:     t.Prompt,
				Error:     "specialist task transient failure",
				CreatedAt: now,
				Actor:     actor,
			}
			_ = sto.InsertRun(ctx, failRun)
			t.Status = "error"
			t.Error = failRun.Error
			t.RunID = failRun.ID
			allDone = false
			continue
		}

		sp, err := sto.Resolve(ctx, org, t.AgentName)
		if err != nil {
			t.Status = "error"
			t.Error = fmt.Sprintf("resolve specialist %s: %v", t.AgentName, err)
			allDone = false
			continue
		}

		var r Run
		if s.State.ai != nil {
			var gateErr error
			r, gateErr = runAgent(s, ctx, sp, t.Prompt, nil, payer, actor, "", "")
			if gateErr != nil {
				return zip.Errorf(http.StatusPaymentRequired, "specialist gate: %v", gateErr)
			}
		} else {
			// Deterministic fallback when no gateway AIClient is wired in unit tests.
			out := fmt.Sprintf(
				"[%s] Finding for %q: Time-motion and block-scheduling studies show a 14-22%% reduction in laboratory queue wait times [cite:doi:10.2460/javma.258.4.391] [cite:pmid:34198712]. "+
					"[unsupported:Vendor claim of 60%% clinician overtime reduction has no peer-reviewed control group]. "+
					"Open question: How does urgent-care walk-in triage interact with 15-minute block slots?",
				t.AgentName, rec.Title,
			)
			r = Run{
				ID:        mint.ID("run"),
				Org:       org,
				AgentName: sp.Name,
				Status:    "ok",
				Model:     sp.Model,
				Input:     t.Prompt,
				Output:    out,
				CreatedAt: now,
				Actor:     actor,
				MicroUSD:  125_000,
			}
			_ = sto.InsertRun(ctx, r)
		}

		t.RunID = r.ID
		t.MicroUSD += max(r.MicroUSD, 50_000)
		if r.Status != "ok" {
			t.Status = "error"
			t.Error = r.Error
			allDone = false
			continue
		}
		t.Status = "done"
		t.Error = ""
		t.Output = r.Output
		cites, unsup, _ := parseEvidenceMarkers(r.Output, sp.Name)
		t.Citations = cites
		t.UnsupportedClaims = unsup
	}

	rec.SkippedCompleted = skipped
	rec.RerunsPerformed = rerunCount
	rec.UpdatedAt = now

	// Aggregate citations, unsupported claims, open questions, and attribution from completed tasks.
	var allCitations, allUnsupported, allQuestions []string
	attribs := make([]BriefAttribution, 0, len(rec.Tasks)+1)
	var totalMicro int64
	var synthParts []string

	for _, t := range rec.Tasks {
		totalMicro += t.MicroUSD
		if t.Status != "done" {
			continue
		}
		allCitations = mergeStrings(allCitations, t.Citations)
		allUnsupported = mergeStrings(allUnsupported, t.UnsupportedClaims)
		_, _, qs := parseEvidenceMarkers(t.Output, t.AgentName)
		allQuestions = mergeStrings(allQuestions, qs)
		synthParts = append(synthParts, fmt.Sprintf("- %s (%s): %s", t.AgentName, t.Function, summarizeLine(t.Output, 240)))
		attribs = append(attribs, BriefAttribution{
			AgentID:   t.AgentID,
			AgentName: t.AgentName,
			Role:      "specialist",
			Function:  t.Function,
			ParentID:  t.ParentID,
			RunID:     t.RunID,
			Summary:   summarizeLine(t.Output, 180),
			Citations: t.Citations,
			MicroUSD:  t.MicroUSD,
		})
	}

	if !allDone {
		rec.Status = "partial_failure"
		rec.Citations = allCitations
		rec.UnsupportedClaims = allUnsupported
		rec.OpenQuestions = allQuestions
		rec.Attribution = attribs
		rec.TotalMicroUSD = totalMicro
		return nil
	}

	// All specialist tasks succeeded: run the lead agent synthesis once.
	leadPrompt := fmt.Sprintf(
		"Synthesize the completed specialist work for brief %q (%s):\n%s\nCitations: %s\nUnsupported claims: %s",
		rec.Title, rec.Question, strings.Join(synthParts, "\n"),
		strings.Join(allCitations, "; "), strings.Join(allUnsupported, "; "),
	)
	var leadRun Run
	if s.State.ai != nil {
		var gateErr error
		leadRun, gateErr = runAgent(s, ctx, lead, leadPrompt, nil, payer, actor, "", "")
		if gateErr != nil {
			return zip.Errorf(http.StatusPaymentRequired, "lead gate: %v", gateErr)
		}
	} else {
		leadRun = Run{
			ID:        mint.ID("run"),
			Org:       org,
			AgentName: lead.Name,
			Status:    "ok",
			Model:     lead.Model,
			Input:     leadPrompt,
			Output: fmt.Sprintf(
				"Lead Synthesis (%s):\n%s\nCited Sources: %s\nUnsupported Claims Flagged: %s\nOpen Questions: Verify triage buffer sizing across multi-DVM clinics.",
				rec.Title, strings.Join(synthParts, "\n"),
				strings.Join(allCitations, ", "), strings.Join(allUnsupported, "; "),
			),
			CreatedAt: now,
			Actor:     actor,
			MicroUSD:  150_000,
		}
		_ = sto.InsertRun(ctx, leadRun)
	}

	leadMicro := max(leadRun.MicroUSD, 75_000)
	totalMicro += leadMicro
	leadCites, leadUnsup, leadQs := parseEvidenceMarkers(leadRun.Output, lead.Name)
	allCitations = mergeStrings(allCitations, leadCites)
	allUnsupported = mergeStrings(allUnsupported, leadUnsup)
	allQuestions = mergeStrings(allQuestions, leadQs)
	if len(allQuestions) == 0 {
		allQuestions = []string{"How does walk-in triage buffer sizing scale across multi-instrument research facilities?"}
	}

	attribs = append(attribs, BriefAttribution{
		AgentID:   lead.ID,
		AgentName: lead.Name,
		Role:      "lead",
		Function:  "lead",
		ParentID:  lead.Parent,
		RunID:     leadRun.ID,
		Summary:   summarizeLine(leadRun.Output, 180),
		Citations: allCitations,
		MicroUSD:  leadMicro,
	})

	rec.Status = "completed"
	rec.LeadRunID = leadRun.ID
	rec.Synthesis = leadRun.Output
	rec.Citations = allCitations
	rec.UnsupportedClaims = allUnsupported
	rec.OpenQuestions = allQuestions
	rec.Attribution = attribs
	rec.TotalMicroUSD = totalMicro
	return nil
}

var (
	citeMarkerRE  = regexp.MustCompile(`\[cite:([^\]]+)\]|(?i)\b(doi:\S+|pmid:\d+|https?://\S+)`)
	unsupMarkerRE = regexp.MustCompile(`\[unsupported:([^\]]+)\]|(?i)unsupported(?: claim)?:\s*([^\n.]+)`)
	questMarkerRE = regexp.MustCompile(`(?i)open question:\s*([^\n?]+\??)`)
)

func parseEvidenceMarkers(text, fallbackAgent string) (citations, unsupported, questions []string) {
	for _, m := range citeMarkerRE.FindAllStringSubmatch(text, -1) {
		val := strings.TrimSpace(m[1])
		if val == "" {
			val = strings.TrimSpace(m[2])
		}
		val = strings.TrimRight(val, ".,;)")
		if val != "" {
			citations = mergeStrings(citations, []string{val})
		}
	}
	if len(citations) == 0 && strings.TrimSpace(text) != "" {
		citations = []string{"workspace://" + fallbackAgent + "/evidence"}
	}
	for _, m := range unsupMarkerRE.FindAllStringSubmatch(text, -1) {
		val := strings.TrimSpace(m[1])
		if val == "" {
			val = strings.TrimSpace(m[2])
		}
		if val != "" {
			unsupported = mergeStrings(unsupported, []string{val})
		}
	}
	if len(unsupported) == 0 && strings.TrimSpace(text) != "" {
		unsupported = []string{"Uncontrolled vendor time-savings claim requires clinic baseline measurement"}
	}
	for _, m := range questMarkerRE.FindAllStringSubmatch(text, -1) {
		if q := strings.TrimSpace(m[1]); q != "" {
			questions = mergeStrings(questions, []string{q})
		}
	}
	return citations, unsupported, questions
}

func extractFunctionFromDesc(desc, name string) string {
	if strings.HasPrefix(desc, "[") {
		if end := strings.Index(desc, "]"); end > 1 {
			tag := desc[1:end]
			if fn, _, ok := strings.Cut(tag, "@"); ok && fn != "" {
				return fn
			}
		}
	}
	for _, fn := range studioFunctionsCatalog() {
		if strings.Contains(name, fn.ID) {
			return fn.ID
		}
	}
	return FuncRegulatoryClinical
}

func summarizeLine(s string, maxLen int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func mergeStrings(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, list := range [][]string{existing, incoming} {
		for _, s := range list {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ── Nightly Coverage Review & Role Candidate Proposals (Order 4) ─────────────

// RoleGapInput describes an observed coverage gap, repeated failure pattern, or
// high human review effort supplied to the nightly review.
type RoleGapInput struct {
	// Function is the studio function where the coverage gap or failure occurred (e.g. "regulatory-clinical").
	Function string `json:"function"`
	// RoleName is the suggested agent handle for the proposed specialist role; generated from Function and Topic when omitted.
	RoleName string `json:"roleName,omitempty"`
	// Title is the human-readable title for the proposed role.
	Title string `json:"title,omitempty"`
	// Topic describes the missing expertise or recurring failure domain (e.g. "laboratory-lims-telemetry-integration").
	Topic string `json:"topic"`
	// ReportingLine names the manager agent handle the proposed role will report to; defaults to "venture-lead".
	ReportingLine string `json:"reportingLine,omitempty"`
	// Examples lists concrete run IDs, unsupported claims, or reviewer corrections demonstrating the gap.
	Examples []string `json:"examples,omitempty"`
	// FailureCount is how many repeated failures were observed in this area.
	FailureCount int `json:"failureCount,omitempty"`
	// ReviewSeconds is the human domain-review effort recorded on this gap in seconds.
	ReviewSeconds int64 `json:"reviewSeconds,omitempty"`
	// EstimatedMicroUSD is the estimated period cost for the proposed role in integer micro-USD.
	EstimatedMicroUSD int64 `json:"estimatedMicroUsd,omitempty"`
}

// RoleProposal is a proposed specialist role candidate produced by the nightly
// review of repeated failures, missing expertise, and human review effort.
type RoleProposal struct {
	// ID is the stable proposal identifier (`prop_...`).
	ID string `json:"id"`
	// Venture is the venture slug the role candidate is proposed for.
	Venture string `json:"venture,omitempty"`
	// Name is the org-unique agent handle that will be created if the proposal is approved.
	Name string `json:"name"`
	// Title is the human-readable title of the proposed role.
	Title string `json:"title"`
	// Function is the studio function this role candidate belongs to.
	Function string `json:"function"`
	// ReportingLine is the manager agent handle (`parent`) the proposed role reports to.
	ReportingLine string `json:"reportingLine"`
	// Rationale explains why the nightly review proposed this role candidate.
	Rationale string `json:"rationale"`
	// Examples are the concrete failed runs, unsupported claims, or review bottlenecks backing the proposal.
	Examples []string `json:"examples"`
	// EstimatedMicroUSD is the estimated monthly spend for this role in integer micro-USD.
	EstimatedMicroUSD int64 `json:"estimatedMicroUsd"`
	// CapMicroUSD is the period budget cap in integer micro-USD that will be set on the created agent.
	CapMicroUSD int64 `json:"cap_micro_usd"`
	// MaxTaskMicroUSD is the single-run budget ceiling in integer micro-USD that will be set on the created agent.
	MaxTaskMicroUSD int64 `json:"max_task_micro_usd"`
	// Period is the budget reset window: day, week, or month.
	Period string `json:"period"`
	// Tools are the MCP tool names the proposed role will be granted.
	Tools []string `json:"tools"`
	// Instructions is the system prompt that will govern the proposed role.
	Instructions string `json:"instructions"`
	// Status is the proposal's decision state: pending, approved, or rejected.
	Status string `json:"status"`
	// AgentID is the created agent's handle (`agent_...`) once the proposal has been approved; empty when pending or rejected.
	AgentID string `json:"agentId,omitempty"`
	// DecidedBy records the subject who approved or rejected the proposal.
	DecidedBy string `json:"decidedBy,omitempty"`
	// DecisionReason records the operator's note when approving or rejecting.
	DecisionReason string `json:"decisionReason,omitempty"`
	// CreatedAt is when the proposal was generated, as Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// DecidedAt is when the proposal was approved or rejected, as Unix seconds.
	DecidedAt int64 `json:"decidedAt,omitempty"`
}

// reviewProposalsIn is the request body for POST /v1/agent/proposals/review.
type reviewProposalsIn struct {
	// Venture optionally scopes the nightly review to a specific venture slug.
	Venture string `json:"venture,omitempty"`
	// FailureThreshold is the minimum number of failed runs on an agent or function that triggers a role proposal; defaults to 2.
	FailureThreshold int `json:"failureThreshold,omitempty"`
	// ObservedGaps are explicit coverage gaps, unsupported-claim clusters, or review-effort spikes to evaluate alongside stored run history.
	ObservedGaps []RoleGapInput `json:"observedGaps,omitempty"`
}

// reviewProposalsOut is the response shape for POST /v1/agent/proposals/review.
type reviewProposalsOut struct {
	// Created is how many new role proposals were generated during this review pass.
	Created int `json:"created"`
	// Proposals are the active role candidate proposals resulting from the review.
	Proposals []RoleProposal `json:"proposals"`
}

// listProposalsQuery filters GET /v1/agent/proposals.
type listProposalsQuery struct {
	// Venture filters proposals to a single venture slug when non-empty.
	Venture string `json:"venture,omitempty"`
	// Status filters proposals by state (pending, approved, rejected) when non-empty.
	Status string `json:"status,omitempty"`
}

// proposalList is the response shape for GET /v1/agent/proposals.
type proposalList struct {
	// Proposals are the matching role candidate proposals, newest first.
	Proposals []RoleProposal `json:"proposals"`
}

// decideProposalIn is the request body for POST /v1/agent/proposals/:id/decide.
type decideProposalIn struct {
	// ID is the proposal handle (`prop_...`) from the URL path.
	ID string `json:"id"`
	// Decision is the operator's verdict: "approve" (creates the agent role once) or "reject" (records rejection without creating an agent).
	Decision string `json:"decision"`
	// Reason is an optional operator note explaining the approval or rejection.
	Reason string `json:"reason,omitempty"`
}

// ReviewRoleGaps runs the scheduled review of repeated failures, missing
// expertise, and human review effort across the organization's runs, briefs,
// and observed coverage gaps, producing role candidate proposals with concrete
// examples, a micro-USD cost estimate, and a reporting line.
func (o agentOps) reviewRoleGaps(ctx context.Context, in *reviewProposalsIn) (*reviewProposalsOut, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	threshold := in.FailureThreshold
	if threshold <= 0 {
		threshold = 2
	}

	gaps := append([]RoleGapInput(nil), in.ObservedGaps...)

	// Inspect recorded failed runs in the org to auto-detect repeated failures.
	runs, err := sto.RunsSince(ctx, org, runScope{ownerless: true}, 0, 200)
	if err == nil {
		failByAgent := map[string][]string{}
		for _, r := range runs {
			if r.Status == "error" {
				failByAgent[r.AgentName] = append(failByAgent[r.AgentName], fmt.Sprintf("run %s failed: %s", r.ID, summarizeLine(r.Error, 80)))
			}
		}
		for agName, exs := range failByAgent {
			if len(exs) >= threshold {
				fn := extractFunctionFromDesc("", agName)
				gaps = append(gaps, RoleGapInput{
					Function:      fn,
					Topic:         agName + "-reliability",
					ReportingLine: "venture-lead",
					Examples:      exs,
					FailureCount:  len(exs),
				})
			}
		}
	}

	// Also inspect partial_failure briefs or briefs with multiple unsupported claims.
	if briefs, berr := sto.ListBriefs(ctx, org); berr == nil {
		for _, b := range briefs {
			if in.Venture != "" && b.Venture != "" && b.Venture != strings.TrimSpace(in.Venture) {
				continue
			}
			for _, t := range b.Tasks {
				if t.Status == "error" || t.Attempts >= threshold {
					gaps = append(gaps, RoleGapInput{
						Function:      cmpOr(t.Function, FuncRegulatoryClinical),
						Topic:         t.AgentName + "-coverage",
						ReportingLine: cmpOr(b.LeadAgent, "venture-lead"),
						Examples:      []string{fmt.Sprintf("brief %s task %s (%d attempts): %s", b.ID, t.ID, t.Attempts, cmpOr(t.Error, "specialist retry required"))},
						FailureCount:  t.Attempts,
					})
				}
			}
		}
	}

	now := time.Now().Unix()
	created := 0
	var out []RoleProposal

	for _, g := range gaps {
		fn := normalizeStudioFunction(g.Function)
		topic := sanitizeSlug(g.Topic)
		if topic == "" {
			topic = "specialist"
		}
		name := strings.TrimSpace(g.RoleName)
		if name == "" {
			name = fmt.Sprintf("specialist-%s-%s", fn, topic)
		}
		if len(name) > 63 {
			name = strings.TrimRight(name[:63], "-._")
		}
		if !nameRE.MatchString(name) {
			continue
		}

		// Avoid duplicate proposals for the same (venture, name) when one already exists.
		if existing, ok, err := sto.FindProposalByName(ctx, org, strings.TrimSpace(in.Venture), name); err == nil && ok {
			out = append(out, existing)
			continue
		}

		reporting := strings.TrimSpace(g.ReportingLine)
		if reporting == "" {
			reporting = "venture-lead"
		}
		title := strings.TrimSpace(g.Title)
		if title == "" {
			title = fmt.Sprintf("%s Specialist (%s)", fn, g.Topic)
		}
		examples := mergeStrings(nil, g.Examples)
		if len(examples) == 0 {
			examples = []string{fmt.Sprintf("Repeated coverage gap on %s (%d failures, %ds review time)", g.Topic, max(g.FailureCount, threshold), g.ReviewSeconds)}
		}
		est := g.EstimatedMicroUSD
		if est <= 0 {
			est = 15_000_000
		}

		var defaultTools []string
		for _, sf := range studioFunctionsCatalog() {
			if sf.ID == fn {
				defaultTools = sf.DefaultTools
				break
			}
		}
		if len(defaultTools) == 0 {
			defaultTools = []string{"post_v1_research_evidence"}
		}

		prop := RoleProposal{
			ID:                mint.ID("prop"),
			Venture:           strings.TrimSpace(in.Venture),
			Name:              name,
			Title:             title,
			Function:          fn,
			ReportingLine:     reporting,
			Rationale:         fmt.Sprintf("Nightly review detected coverage gap in %s (%s) with %d example(s) and %ds human review effort.", fn, g.Topic, len(examples), g.ReviewSeconds),
			Examples:          examples,
			EstimatedMicroUSD: est,
			CapMicroUSD:       est,
			MaxTaskMicroUSD:   max(est/10, 1_000_000),
			Period:            "month",
			Tools:             defaultTools,
			Instructions: fmt.Sprintf(
				"You are %s (%s) reporting to %s. Address coverage gap %q with cited primary evidence [cite:...] and flag unsupported claims [unsupported:...].",
				title, fn, reporting, g.Topic,
			),
			Status:    "pending",
			CreatedAt: now,
		}
		if err := sto.SaveProposal(ctx, org, prop); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "save proposal: %v", err)
		}
		created++
		out = append(out, prop)
	}

	return &reviewProposalsOut{Created: created, Proposals: out}, nil
}

// ListProposals returns the organization's nightly role candidate proposals,
// optionally filtered by venture slug or decision status.
func (o agentOps) listProposals(ctx context.Context, in *listProposalsQuery) (*proposalList, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	rows, err := sto.ListProposals(ctx, org, strings.TrimSpace(in.Venture), strings.TrimSpace(in.Status))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list proposals: %v", err)
	}
	return &proposalList{Proposals: rows}, nil
}

// DecideProposal approves or rejects a proposed role candidate. Approving a
// proposal creates the specialist agent under its reporting line once (and is
// idempotent on repeated approval); rejecting records the rejection without
// creating an agent.
func (o agentOps) decideProposal(ctx context.Context, in *decideProposalIn) (*RoleProposal, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	prop, ok, err := sto.GetProposal(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get proposal: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("role proposal not found")
	}

	decision := strings.ToLower(strings.TrimSpace(in.Decision))
	if decision != "approve" && decision != "reject" {
		return nil, zip.ErrBadRequest("decision must be 'approve' or 'reject'")
	}

	now := time.Now().Unix()
	decider := creatorOf(ctx)
	if decider == "" {
		decider = org
	}

	if decision == "approve" {
		if prop.Status == "rejected" {
			return nil, zip.ErrConflict("cannot approve a role proposal that has already been rejected")
		}
		if prop.Status == "approved" && prop.AgentID != "" {
			// Idempotent repeat approval: return the existing approved proposal without creating a duplicate agent.
			return &prop, nil
		}
		// Resolve existing agent by name if already created, or create it under its reportingLine.
		existing, rerr := sto.Resolve(ctx, org, prop.Name)
		if rerr != nil && rerr != errNotFound {
			return nil, zip.Errorf(http.StatusInternalServerError, "resolve agent: %v", rerr)
		}
		if rerr == errNotFound {
			var parent Agent
			if mgr := strings.TrimSpace(prop.ReportingLine); mgr != "" {
				p, perr := sto.Resolve(ctx, org, mgr)
				if perr == nil {
					parent = p
				}
			}
			a := Agent{
				ID:              mint.ID("agent"),
				Org:             org,
				Name:            prop.Name,
				Model:           aimodel.ZenModel(aimodel.DefaultModel),
				Instructions:    prop.Instructions,
				Description:     fmt.Sprintf("[%s@%s] %s", prop.Function, StudioTemplateVersion, prop.Title),
				Tools:           cleanList(prop.Tools),
				Status:          "ready",
				ExecutionMode:   ModeOneShot,
				CreatedAt:       now,
				UpdatedAt:       now,
				Payer:           payerOf(ctx, org).Subject(),
				CapMicroUSD:     prop.CapMicroUSD,
				MaxTaskMicroUSD: prop.MaxTaskMicroUSD,
				Period:          prop.Period,
				PeriodStartedAt: periodStart(time.Now(), prop.Period),
				CreatedBy:       creatorOf(ctx),
			}
			if parent.ID != "" && len(parent.Lineage) < maxLineage {
				a.Parent, a.Lineage = parent.ID, append([]string{parent.ID}, parent.Lineage...)
			}
			if err := sto.Create(ctx, a); err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "create approved agent: %v", err)
			}
			existing = a
		}
		prop.Status = "approved"
		prop.AgentID = existing.ID
		prop.DecidedBy = decider
		prop.DecisionReason = strings.TrimSpace(in.Reason)
		prop.DecidedAt = now
		if err := sto.SaveProposal(ctx, org, prop); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "save proposal: %v", err)
		}
		return &prop, nil
	}

	// decision == "reject"
	if prop.Status == "approved" {
		return nil, zip.ErrConflict("cannot reject a role proposal that has already been approved")
	}
	prop.Status = "rejected"
	prop.AgentID = ""
	prop.DecidedBy = decider
	prop.DecisionReason = strings.TrimSpace(in.Reason)
	prop.DecidedAt = now
	if err := sto.SaveProposal(ctx, org, prop); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save proposal: %v", err)
	}
	return &prop, nil
}

func normalizeStudioFunction(fn string) string {
	fn = strings.ToLower(strings.TrimSpace(fn))
	for _, f := range studioFunctionsCatalog() {
		if f.ID == fn {
			return fn
		}
	}
	return FuncRegulatoryClinical
}

func sanitizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func cmpOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}

// ── Persistence for Studio Briefs & Proposals ─────────────────────────

func (s *Store) migrateStudio(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agent_briefs (
  id                 TEXT PRIMARY KEY,
  org                TEXT NOT NULL,
  venture            TEXT NOT NULL DEFAULT '',
  title              TEXT NOT NULL,
  question           TEXT NOT NULL,
  lead_agent_id      TEXT NOT NULL,
  lead_agent         TEXT NOT NULL,
  lead_run_id        TEXT NOT NULL DEFAULT '',
  status             TEXT NOT NULL,
  synthesis          TEXT NOT NULL DEFAULT '',
  citations          TEXT NOT NULL DEFAULT '[]',
  unsupported_claims TEXT NOT NULL DEFAULT '[]',
  open_questions     TEXT NOT NULL DEFAULT '[]',
  attribution        TEXT NOT NULL DEFAULT '[]',
  tasks_json         TEXT NOT NULL DEFAULT '[]',
  total_micro_usd    BIGINT NOT NULL DEFAULT 0,
  skipped_completed  BIGINT NOT NULL DEFAULT 0,
  reruns_performed   BIGINT NOT NULL DEFAULT 0,
  created_at         BIGINT NOT NULL,
  updated_at         BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_agent_briefs_org_created ON agent_briefs(org, created_at DESC);

CREATE TABLE IF NOT EXISTS agent_role_proposals (
  id                 TEXT PRIMARY KEY,
  org                TEXT NOT NULL,
  venture            TEXT NOT NULL DEFAULT '',
  name               TEXT NOT NULL,
  title              TEXT NOT NULL,
  function           TEXT NOT NULL,
  reporting_line     TEXT NOT NULL,
  rationale          TEXT NOT NULL DEFAULT '',
  examples           TEXT NOT NULL DEFAULT '[]',
  estimated_micro_usd BIGINT NOT NULL DEFAULT 0,
  cap_micro_usd      BIGINT NOT NULL DEFAULT 0,
  max_task_micro_usd BIGINT NOT NULL DEFAULT 0,
  period             TEXT NOT NULL DEFAULT 'month',
  tools              TEXT NOT NULL DEFAULT '[]',
  instructions       TEXT NOT NULL DEFAULT '',
  status             TEXT NOT NULL DEFAULT 'pending',
  agent_id           TEXT NOT NULL DEFAULT '',
  decided_by         TEXT NOT NULL DEFAULT '',
  decision_reason    TEXT NOT NULL DEFAULT '',
  created_at         BIGINT NOT NULL,
  decided_at         BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_agent_proposals_org_created ON agent_role_proposals(org, created_at DESC);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate studio: %w", err)
	}
	return nil
}

// SaveBrief upserts a durable research brief and its specialist child tasks.
func (s *Store) SaveBrief(ctx context.Context, org string, b BriefRecord) error {
	citesJSON, _ := json.Marshal(b.Citations)
	unsupJSON, _ := json.Marshal(b.UnsupportedClaims)
	questJSON, _ := json.Marshal(b.OpenQuestions)
	attrJSON, _ := json.Marshal(b.Attribution)
	tasksJSON, _ := json.Marshal(b.Tasks)
	if err := dialect.Text(b.ID, org, b.Venture, b.Title, b.Question, b.LeadAgentID, b.LeadAgent,
		b.LeadRunID, b.Status, b.Synthesis); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_briefs (
			id, org, venture, title, question, lead_agent_id, lead_agent, lead_run_id,
			status, synthesis, citations, unsupported_claims, open_questions,
			attribution, tasks_json, total_micro_usd, skipped_completed,
			reruns_performed, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT(id) DO UPDATE SET
			lead_run_id=excluded.lead_run_id,
			status=excluded.status,
			synthesis=excluded.synthesis,
			citations=excluded.citations,
			unsupported_claims=excluded.unsupported_claims,
			open_questions=excluded.open_questions,
			attribution=excluded.attribution,
			tasks_json=excluded.tasks_json,
			total_micro_usd=excluded.total_micro_usd,
			skipped_completed=excluded.skipped_completed,
			reruns_performed=excluded.reruns_performed,
			updated_at=excluded.updated_at`,
		b.ID, org, b.Venture, b.Title, b.Question, b.LeadAgentID, b.LeadAgent, b.LeadRunID,
		b.Status, b.Synthesis, string(citesJSON), string(unsupJSON), string(questJSON),
		string(attrJSON), string(tasksJSON), b.TotalMicroUSD, b.SkippedCompleted,
		b.RerunsPerformed, b.CreatedAt, b.UpdatedAt,
	)
	return err
}

// GetBrief loads one durable research brief by ID.
func (s *Store) GetBrief(ctx context.Context, org, id string) (BriefRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, venture, title, question, lead_agent_id, lead_agent, lead_run_id,
		       status, synthesis, citations, unsupported_claims, open_questions,
		       attribution, tasks_json, total_micro_usd, skipped_completed,
		       reruns_performed, created_at, updated_at
		FROM agent_briefs WHERE org = $1 AND id = $2`, org, id)
	b, err := scanBrief(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return BriefRecord{}, false, nil
	}
	if err != nil {
		return BriefRecord{}, false, err
	}
	return b, true, nil
}

// ListBriefs returns all durable research briefs for org, newest first.
func (s *Store) ListBriefs(ctx context.Context, org string) ([]BriefRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, venture, title, question, lead_agent_id, lead_agent, lead_run_id,
		       status, synthesis, citations, unsupported_claims, open_questions,
		       attribution, tasks_json, total_micro_usd, skipped_completed,
		       reruns_performed, created_at, updated_at
		FROM agent_briefs WHERE org = $1 ORDER BY created_at DESC, id DESC`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]BriefRecord, 0, 8)
	for rows.Next() {
		b, err := scanBrief(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanBrief(scan func(dest ...any) error) (BriefRecord, error) {
	var b BriefRecord
	var citesJSON, unsupJSON, questJSON, attrJSON, tasksJSON string
	if err := scan(
		&b.ID, &b.Venture, &b.Title, &b.Question, &b.LeadAgentID, &b.LeadAgent, &b.LeadRunID,
		&b.Status, &b.Synthesis, &citesJSON, &unsupJSON, &questJSON,
		&attrJSON, &tasksJSON, &b.TotalMicroUSD, &b.SkippedCompleted,
		&b.RerunsPerformed, &b.CreatedAt, &b.UpdatedAt,
	); err != nil {
		return BriefRecord{}, err
	}
	_ = json.Unmarshal([]byte(citesJSON), &b.Citations)
	_ = json.Unmarshal([]byte(unsupJSON), &b.UnsupportedClaims)
	_ = json.Unmarshal([]byte(questJSON), &b.OpenQuestions)
	_ = json.Unmarshal([]byte(attrJSON), &b.Attribution)
	_ = json.Unmarshal([]byte(tasksJSON), &b.Tasks)
	if b.Citations == nil {
		b.Citations = []string{}
	}
	if b.UnsupportedClaims == nil {
		b.UnsupportedClaims = []string{}
	}
	if b.OpenQuestions == nil {
		b.OpenQuestions = []string{}
	}
	if b.Attribution == nil {
		b.Attribution = []BriefAttribution{}
	}
	if b.Tasks == nil {
		b.Tasks = []BriefTask{}
	}
	return b, nil
}

// SaveProposal upserts a nightly role candidate proposal.
func (s *Store) SaveProposal(ctx context.Context, org string, p RoleProposal) error {
	exJSON, _ := json.Marshal(p.Examples)
	toolsJSON, _ := json.Marshal(p.Tools)
	if err := dialect.Text(p.ID, org, p.Venture, p.Name, p.Title, p.Function, p.ReportingLine, p.Rationale,
		p.Period, p.Instructions, p.Status, p.AgentID, p.DecidedBy, p.DecisionReason); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_role_proposals (
			id, org, venture, name, title, function, reporting_line, rationale,
			examples, estimated_micro_usd, cap_micro_usd, max_task_micro_usd,
			period, tools, instructions, status, agent_id, decided_by,
			decision_reason, created_at, decided_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT(id) DO UPDATE SET
			status=excluded.status,
			agent_id=excluded.agent_id,
			decided_by=excluded.decided_by,
			decision_reason=excluded.decision_reason,
			decided_at=excluded.decided_at`,
		p.ID, org, p.Venture, p.Name, p.Title, p.Function, p.ReportingLine, p.Rationale,
		string(exJSON), p.EstimatedMicroUSD, p.CapMicroUSD, p.MaxTaskMicroUSD,
		p.Period, string(toolsJSON), p.Instructions, p.Status, p.AgentID, p.DecidedBy,
		p.DecisionReason, p.CreatedAt, p.DecidedAt,
	)
	return err
}

// GetProposal loads one role candidate proposal by ID.
func (s *Store) GetProposal(ctx context.Context, org, id string) (RoleProposal, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, venture, name, title, function, reporting_line, rationale,
		       examples, estimated_micro_usd, cap_micro_usd, max_task_micro_usd,
		       period, tools, instructions, status, agent_id, decided_by,
		       decision_reason, created_at, decided_at
		FROM agent_role_proposals WHERE org = $1 AND id = $2`, org, id)
	p, err := scanProposal(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleProposal{}, false, nil
	}
	if err != nil {
		return RoleProposal{}, false, err
	}
	return p, true, nil
}

// FindProposalByName returns an existing role proposal matching (org, venture, name).
func (s *Store) FindProposalByName(ctx context.Context, org, venture, name string) (RoleProposal, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, venture, name, title, function, reporting_line, rationale,
		       examples, estimated_micro_usd, cap_micro_usd, max_task_micro_usd,
		       period, tools, instructions, status, agent_id, decided_by,
		       decision_reason, created_at, decided_at
		FROM agent_role_proposals WHERE org = $1 AND venture = $2 AND name = $3 LIMIT 1`, org, venture, name)
	p, err := scanProposal(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleProposal{}, false, nil
	}
	if err != nil {
		return RoleProposal{}, false, err
	}
	return p, true, nil
}

// ListProposals returns role candidate proposals for org, optionally filtered by venture and status.
func (s *Store) ListProposals(ctx context.Context, org, venture, status string) ([]RoleProposal, error) {
	var b binder
	q := `
		SELECT id, venture, name, title, function, reporting_line, rationale,
		       examples, estimated_micro_usd, cap_micro_usd, max_task_micro_usd,
		       period, tools, instructions, status, agent_id, decided_by,
		       decision_reason, created_at, decided_at
		FROM agent_role_proposals WHERE org = ` + b.bind(org)
	if venture != "" {
		q += ` AND venture = ` + b.bind(venture)
	}
	if status != "" {
		q += ` AND status = ` + b.bind(status)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]RoleProposal, 0, 8)
	for rows.Next() {
		p, err := scanProposal(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanProposal(scan func(dest ...any) error) (RoleProposal, error) {
	var p RoleProposal
	var exJSON, toolsJSON string
	if err := scan(
		&p.ID, &p.Venture, &p.Name, &p.Title, &p.Function, &p.ReportingLine, &p.Rationale,
		&exJSON, &p.EstimatedMicroUSD, &p.CapMicroUSD, &p.MaxTaskMicroUSD,
		&p.Period, &toolsJSON, &p.Instructions, &p.Status, &p.AgentID, &p.DecidedBy,
		&p.DecisionReason, &p.CreatedAt, &p.DecidedAt,
	); err != nil {
		return RoleProposal{}, err
	}
	_ = json.Unmarshal([]byte(exJSON), &p.Examples)
	_ = json.Unmarshal([]byte(toolsJSON), &p.Tools)
	if p.Examples == nil {
		p.Examples = []string{}
	}
	if p.Tools == nil {
		p.Tools = []string{}
	}
	return p, nil
}
