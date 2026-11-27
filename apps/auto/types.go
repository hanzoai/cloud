// types.go ports the ActivePieces shared contract
// (auto/packages/shared/src/lib/automation/) to plain Go structs + string-const
// enums. The JSON tags match the TypeScript field names verbatim because the
// reused web/ flow builder is the contract consumer — it authors the same
// trigger→action tree this engine walks.
//
// TENANT ISOLATION is a physical property, not a policy: every stored row leads
// its indexes with `org`, and the durable engine's ONLY credential scope is
// FlowRunInput.Owner — the VALIDATED org resolved from principal.Org at
// flow-start, never a client-supplied field. See engine.go (ExecuteStepActivity).
//
// The package doc lives once, in auto.go.

package auto

import (
	"encoding/json"

	"github.com/hanzoai/cloud/apps/provider"
)

// ── Flow (flows/flow.ts) ────────────────────────────────────────────────────

// FlowStatus mirrors flows/flow.ts FlowStatus.
type FlowStatus string

const (
	FlowEnabled  FlowStatus = "ENABLED"
	FlowDisabled FlowStatus = "DISABLED"
)

// Flow is an org-scoped automation. projectId IS the org (the TS `projectId`
// field), always server-derived from principal.Org and NEVER trusted from a
// request body — a caller can never author a flow into another tenant.
type Flow struct {
	ID                 string          `json:"id"`
	Org                string          `json:"projectId"` // projectId == org (server-derived)
	ExternalID         string          `json:"externalId"`
	FolderID           string          `json:"folderId"`
	Status             FlowStatus      `json:"status"`
	PublishedVersionID string          `json:"publishedVersionId"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	Created            int64           `json:"created"`
	Updated            int64           `json:"updated"`
}

// ── FlowVersion (flows/flow-version.ts) ─────────────────────────────────────

// FlowVersionState mirrors flows/flow-version.ts FlowVersionState.
type FlowVersionState string

const (
	VersionDraft  FlowVersionState = "DRAFT"
	VersionLocked FlowVersionState = "LOCKED"
)

// LatestFlowSchemaVersion is the shared schema stamp new versions carry, matching
// flow-version.ts LATEST_FLOW_SCHEMA_VERSION.
const LatestFlowSchemaVersion = "21"

// FlowVersion is one editable revision of a flow: a display name plus the root
// trigger of the step tree. Org is the isolation key (never serialized).
type FlowVersion struct {
	ID            string           `json:"id"`
	Org           string           `json:"-"`
	FlowID        string           `json:"flowId"`
	DisplayName   string           `json:"displayName"`
	Trigger       *FlowTrigger     `json:"trigger"`
	Valid         bool             `json:"valid"`
	State         FlowVersionState `json:"state"`
	SchemaVersion string           `json:"schemaVersion"`
	Created       int64            `json:"created"`
	Updated       int64            `json:"updated"`
}

// ── Step tree (triggers/trigger.ts + actions/action.ts) ─────────────────────

// FlowTriggerType / FlowActionType mirror the discriminants of the step nodes.
const (
	TriggerTypeProvider = "PIECE_TRIGGER"
	TriggerTypeEmpty    = "EMPTY"

	ActionTypeProvider = "PIECE"
	ActionTypeCode     = "CODE"
	ActionTypeBranch   = "BRANCH"
	ActionTypeLoop     = "LOOP_ON_ITEMS"
	ActionTypeRouter   = "ROUTER"
)

// StepSettings is the flattened union of PieceTriggerSettings / PieceActionSettings
// / CodeActionSettings — the fields the engine actually reads to dispatch a step:
// which provider, which action/trigger, and the input map. (The builder-only settings
// — propertySettings, sampleData, errorHandlingOptions — round-trip opaquely via
// the raw version JSON in the store; they are not modeled here.)
type StepSettings struct {
	ProviderName    string         `json:"pieceName,omitempty"`
	ProviderVersion string         `json:"pieceVersion,omitempty"`
	ActionName      string         `json:"actionName,omitempty"`
	TriggerName     string         `json:"triggerName,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
}

// FlowTrigger is the root of the step tree (triggers/trigger.ts). Strategy drives
// enable/disable (POLLING → CreateSchedule); NextAction chains into the action list.
type FlowTrigger struct {
	Name        string                   `json:"name"`
	Type        string                   `json:"type"` // PIECE_TRIGGER | EMPTY
	DisplayName string                   `json:"displayName"`
	Valid       bool                     `json:"valid"`
	Strategy    provider.TriggerStrategy `json:"strategy,omitempty"`
	Settings    StepSettings             `json:"settings"`
	NextAction  *FlowAction              `json:"nextAction,omitempty"`
}

// FlowAction is an action node (actions/action.ts). The engine walks the linear
// NextAction chain; the tree discriminants (ROUTER/LOOP) are modeled for contract
// fidelity but branch/loop execution is out of Phase-1 scope (see operations.go).
type FlowAction struct {
	Name        string       `json:"name"`
	Type        string       `json:"type"` // PIECE | CODE | ROUTER | LOOP_ON_ITEMS
	DisplayName string       `json:"displayName"`
	Valid       bool         `json:"valid"`
	Skip        bool         `json:"skip,omitempty"`
	Settings    StepSettings `json:"settings"`
	NextAction  *FlowAction  `json:"nextAction,omitempty"`
}

// ── FlowRun (flow-run/flow-run.ts + execution/flow-execution.ts) ────────────

// FlowRunStatus mirrors execution/flow-execution.ts FlowRunStatus (the subset the
// engine emits; the full enum has memory/log/quota terminal states we map onto FAILED).
type FlowRunStatus string

const (
	RunRunning   FlowRunStatus = "RUNNING"
	RunSucceeded FlowRunStatus = "SUCCEEDED"
	RunFailed    FlowRunStatus = "FAILED"
	RunPaused    FlowRunStatus = "PAUSED"
	RunQueued    FlowRunStatus = "QUEUED"
	RunCanceled  FlowRunStatus = "CANCELED"
	RunTimeout   FlowRunStatus = "TIMEOUT"
	// RunSkipped is a start that did not run because the flow runs one at a time
	// and its previous run was still going (store.go CreateRunExclusive).
	RunSkipped FlowRunStatus = "SKIPPED"
	// RunRefused is a run of an automation whose person is no longer a member of
	// its org: no Dev run started, and the automation was turned off (agent.go).
	RunRefused FlowRunStatus = "REFUSED"
)

// FlowRun is an execution record (flow-run/flow-run.ts). WorkflowID (the tasks
// engine handle) equals ID and is internal — resume/describe address the engine
// through it, scoped to the org's namespace.
type FlowRun struct {
	ID            string        `json:"id"`
	Org           string        `json:"-"`
	FlowID        string        `json:"flowId"`
	FlowVersionID string        `json:"flowVersionId"`
	WorkflowID    string        `json:"-"`
	Status        FlowRunStatus `json:"status"`
	StartTime     int64         `json:"startTime"`
	FinishTime    int64         `json:"finishTime"`
	Created       int64         `json:"created"`
	Updated       int64         `json:"updated"`
	// Session is the Dev run an agent step started, and Summary one line on how
	// the run went. With Person and Email — who the run was made as — and Actor —
	// who pressed Run — they are an automation's, shown only by its own routes to
	// those who may manage it (automation.go manages), never on this record.
	Session string `json:"-"`
	Summary string `json:"-"`
	Person  string `json:"-"`
	Email   string `json:"-"`
	Actor   string `json:"-"`
	// Draft is what a posting automation's agent wrote, and Posts where it went,
	// as JSON (post.go). Shown, like the rest above, only by the automation routes.
	Draft string `json:"-"`
	Posts string `json:"-"`
	// Outputs are the Library items the run made, a JSON list of ids.
	Outputs string `json:"-"`
}

// ── Connector catalog (catalog wire schema) ──────────────────
//
// "Connector" is the ONE Hanzo term for an external-service integration a flow
// step invokes (Slack/GitHub/Stripe/…). The pre-rename name "provider" (ActivePieces
// jargon) is retired on this surface; the /v1/auto/providers path survives only
// as a back-compat alias (see auto.go). See HIP-0126.

// Catalog is the browse catalogue served at GET /v1/auto/provider: every
// connector the registry holds — the generated piece catalog (piece/catalog)
// and each connector with actions of its own.
type Catalog struct {
	ConnectorCount int `json:"connectorCount"`
	// RunnableCount is how many of them this deployment can actually invoke. It is
	// the number that matters and it was absent: a caller read connectorCount and
	// reasonably believed all of it worked.
	RunnableCount int                 `json:"runnableCount"`
	Connectors    []ConnectorMetadata `json:"connectors"`
}

// ConnectorMetadata is one catalogue entry. The catalog WIRE schema models a
// connector's actions/triggers as arrays, so this Go shape uses arrays to match the
// wire exactly, as the contract requires.
type ConnectorMetadata struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName"`
	Description string             `json:"description"`
	LogoURL     string             `json:"logoUrl"`
	Version     string             `json:"version"`
	Categories  []string           `json:"categories"`
	Auth        ConnectorAuth      `json:"auth"`
	Actions     []ConnectorAction  `json:"actions"`
	Triggers    []ConnectorTrigger `json:"triggers"`
	// Runnable is whether this connector's actions run HERE, so a caller can
	// invoke what the entry lists. It is the registry's answer — an action
	// registered for the connector — never a stored flag, because a stored flag
	// is a second answer to a question the registry already answers.
	Runnable bool `json:"runnable"`
}

// ConnectorAuth is the catalogue's auth descriptor: which credential a connector
// needs ("none" for core, "oauth2"/"bot_token" for a connected provider) and
// whether it is required.
type ConnectorAuth struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// ConnectorAction / ConnectorTrigger are the catalogue's action/trigger descriptors.
type ConnectorAction struct {
	Name        string              `json:"name"`
	DisplayName string              `json:"displayName"`
	Description string              `json:"description"`
	Props       []provider.PropSpec `json:"props"`
}

type ConnectorTrigger struct {
	Name        string              `json:"name"`
	DisplayName string              `json:"displayName"`
	Description string              `json:"description"`
	Strategy    string              `json:"strategy"`
	Props       []provider.PropSpec `json:"props"`
}

// provider.PropSpec describes one input property of an action/trigger. It is the ONE prop
// shape shared by the connector framework (connector.go), the catalogue, and the
// Tool input-schema derivation (tool.go) — one definition, every consumer.

// ── FlowOperation envelope (flows/operations/index.ts) ──────────────────────

// FlowOperationType mirrors operations/index.ts FlowOperationType — the builder's
// edit-op discriminants. The engine applies the linear-chain subset (see
// operations.go); tree-restructuring ops are honestly rejected, never faked.
type FlowOperationType string

const (
	OpAddAction     FlowOperationType = "ADD_ACTION"
	OpUpdateAction  FlowOperationType = "UPDATE_ACTION"
	OpDeleteAction  FlowOperationType = "DELETE_ACTION"
	OpMoveAction    FlowOperationType = "MOVE_ACTION"
	OpUpdateTrigger FlowOperationType = "UPDATE_TRIGGER"
	OpChangeName    FlowOperationType = "CHANGE_NAME"
	OpChangeStatus  FlowOperationType = "CHANGE_STATUS"
)

// FlowOperation is the discriminated-union envelope the builder POSTs to
// /v1/auto/flows/:id/operations: a type + an opaque request the apply
// switch decodes per type.
type FlowOperation struct {
	Type    FlowOperationType `json:"type"`
	Request json.RawMessage   `json:"request"`
}

// Operation request shapes (operations/index.ts). Only the linear-chain ops are
// modeled; each is decoded from FlowOperation.Request by the apply switch.
type (
	// AddActionRequest inserts Action after the step named ParentStep (empty ⇒ append
	// to the end of the chain, or set as the trigger's first action).
	AddActionRequest struct {
		ParentStep string      `json:"parentStep"`
		Action     *FlowAction `json:"action"`
	}
	// UpdateActionRequest replaces the settings of the step named Name.
	UpdateActionRequest struct {
		Name     string       `json:"name"`
		Type     string       `json:"type"`
		Settings StepSettings `json:"settings"`
	}
	// DeleteActionRequest removes the named steps and relinks the chain.
	DeleteActionRequest struct {
		Names []string `json:"names"`
	}
	// MoveActionRequest moves Name to sit immediately after NewParentStep.
	MoveActionRequest struct {
		Name          string `json:"name"`
		NewParentStep string `json:"newParentStep"`
	}
	// UpdateTriggerRequest replaces the root trigger node.
	UpdateTriggerRequest FlowTrigger
	// ChangeNameRequest renames the version.
	ChangeNameRequest struct {
		DisplayName string `json:"displayName"`
	}
	// ChangeStatusRequest flips ENABLED/DISABLED.
	ChangeStatusRequest struct {
		Status FlowStatus `json:"status"`
	}
)

// ── Durable-execution I/O (engine.go) ───────────────────────────────────────
// All JSON-serializable (no funcs/channels): they cross the tasks wire.

// FlowRunInput is the durable workflow's typed input. Owner is the VALIDATED org
// set at flow-start from principal.Org — the SOLE credential scope and the
// cross-tenant isolation boundary; it is NEVER read from a request body. Steps is
// the flattened trigger→action chain (side-effecting steps in order).
type FlowRunInput struct {
	Owner         string     `json:"owner"`
	FlowID        string     `json:"flowId"`
	FlowVersionID string     `json:"flowVersionId"`
	RunID         string     `json:"runId"`
	Steps         []FlowStep `json:"steps"`
	// Trigger is the inbound event payload that fired the flow — nil for a manual /run
	// or a bare cron tick, set by Deliver for a WEBHOOK/APP_WEBHOOK event. The workflow
	// seeds it as outputs["trigger"], so an action can reference the event that fired the
	// flow with {{trigger.field}}.
	Trigger map[string]any `json:"trigger,omitempty"`
	// Depth is the run's causation depth — how many in-platform trigger→action hops led
	// here (0 = an external origin). It rides in from TriggerEvent.Depth and lets an
	// in-platform cycle terminate at maxCausationDepth instead of amplifying.
	Depth int `json:"depth,omitempty"`
	// Exclusive runs the flow one at a time: a start while an earlier run is live
	// is recorded SKIPPED and does nothing. Automations set it (automation.go).
	Exclusive bool `json:"exclusive,omitempty"`
}

// FlowStep is one resolved, executable step in the flattened chain.
type FlowStep struct {
	Name         string         `json:"name"`
	ProviderName string         `json:"pieceName"`
	ActionName   string         `json:"actionName"`
	Input        map[string]any `json:"input"`
}

// StepInput is the activity's per-step input. Owner is copied verbatim from
// FlowRunInput.Owner by the workflow — an activity can never widen its own scope.
type StepInput struct {
	Owner        string         `json:"owner"`
	RunID        string         `json:"runId"`
	Name         string         `json:"name"`
	ProviderName string         `json:"pieceName"`
	ActionName   string         `json:"actionName"`
	Input        map[string]any `json:"input"`
	PrevOutputs  map[string]any `json:"prevOutputs"`
}

// StepOutput is the activity's result for one step.
type StepOutput struct {
	Name   string `json:"name"`
	Output any    `json:"output"`
}

// FlowRunResult is the workflow's terminal result: the final status plus every
// step's output, keyed by step name (the threaded outputs).
type FlowRunResult struct {
	RunID   string         `json:"runId"`
	Status  FlowRunStatus  `json:"status"`
	Steps   int            `json:"steps"`
	Outputs map[string]any `json:"outputs"`
}

// flattenSteps walks a version's trigger→nextAction chain into the ordered,
// executable []FlowStep the workflow runs. The trigger node itself is an entry
// point, not a side-effecting step, so it is not emitted; only PIECE/CODE actions
// (the linear chain) are. Skipped actions are dropped. A nil trigger yields none.
func flattenSteps(v *FlowVersion) []FlowStep {
	if v == nil || v.Trigger == nil {
		return nil
	}
	out := make([]FlowStep, 0, 8)
	for a := v.Trigger.NextAction; a != nil; a = a.NextAction {
		if a.Skip {
			continue
		}
		provider, action := stepProviderAction(a)
		if provider == "" {
			continue
		}
		out = append(out, FlowStep{
			Name:         a.Name,
			ProviderName: provider,
			ActionName:   action,
			Input:        a.Settings.Input,
		})
	}
	return out
}

// stepProviderAction resolves the (provider, action) an action node dispatches to. A
// PIECE node names them explicitly; a CODE node is the built-in core.code transform.
func stepProviderAction(a *FlowAction) (provider, action string) {
	switch a.Type {
	case ActionTypeCode:
		return coreProvider, "code"
	case ActionTypeProvider:
		return a.Settings.ProviderName, a.Settings.ActionName
	default:
		// ROUTER/LOOP are not linear-executable in Phase 1 — skip (provider=="").
		return "", ""
	}
}
