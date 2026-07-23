// Package automations mounts the Hanzo Cloud /v1/automations/* surface: a
// native-Go Connectors+Automations engine (HIP-0106, task #51) that runs an
// org's flows durably on the ONE shared in-process hanzoai/tasks engine and
// invokes third-party connectors whose credentials are custodied by
// clients/integrations (KMS-sealed, per-org).
//
// This file ports the ActivePieces shared contract
// (auto/packages/shared/src/lib/automation/) to plain Go structs + string-const
// enums. The JSON tags match the TypeScript field names verbatim because the
// reused web/ flow builder is the contract consumer — it authors the same
// trigger→action tree this engine walks.
//
// TENANT ISOLATION is a physical property, not a policy: every stored row leads
// its indexes with `org`, and the durable engine's ONLY credential scope is
// FlowRunInput.Owner — the VALIDATED org resolved from principal.Org at
// flow-start, never a client-supplied field. See engine.go (ExecuteStepActivity).
package automations

import "encoding/json"

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

// TriggerStrategy mirrors trigger/index.ts TriggerStrategy. It selects HOW a
// flow starts: POLLING → a cron schedule on the tasks engine; WEBHOOK/APP_WEBHOOK
// → an inbound HTTP event; MANUAL → an explicit /run.
type TriggerStrategy string

const (
	StrategyPolling    TriggerStrategy = "POLLING"
	StrategyWebhook    TriggerStrategy = "WEBHOOK"
	StrategyAppWebhook TriggerStrategy = "APP_WEBHOOK"
	StrategyManual     TriggerStrategy = "MANUAL"
)

// FlowTriggerType / FlowActionType mirror the discriminants of the step nodes.
const (
	TriggerTypePiece = "PIECE_TRIGGER"
	TriggerTypeEmpty = "EMPTY"

	ActionTypePiece  = "PIECE"
	ActionTypeCode   = "CODE"
	ActionTypeBranch = "BRANCH"
	ActionTypeLoop   = "LOOP_ON_ITEMS"
	ActionTypeRouter = "ROUTER"
)

// StepSettings is the flattened union of PieceTriggerSettings / PieceActionSettings
// / CodeActionSettings — the fields the engine actually reads to dispatch a step:
// which piece, which action/trigger, and the input map. (The builder-only settings
// — propertySettings, sampleData, errorHandlingOptions — round-trip opaquely via
// the raw version JSON in the store; they are not modeled here.)
type StepSettings struct {
	PieceName    string         `json:"pieceName,omitempty"`
	PieceVersion string         `json:"pieceVersion,omitempty"`
	ActionName   string         `json:"actionName,omitempty"`
	TriggerName  string         `json:"triggerName,omitempty"`
	Input        map[string]any `json:"input,omitempty"`
}

// FlowTrigger is the root of the step tree (triggers/trigger.ts). Strategy drives
// enable/disable (POLLING → CreateSchedule); NextAction chains into the action list.
type FlowTrigger struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"` // PIECE_TRIGGER | EMPTY
	DisplayName string          `json:"displayName"`
	Valid       bool            `json:"valid"`
	Strategy    TriggerStrategy `json:"strategy,omitempty"`
	Settings    StepSettings    `json:"settings"`
	NextAction  *FlowAction     `json:"nextAction,omitempty"`
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
}

// ── Connector catalog (catalog wire schema) ──────────────────
//
// "Connector" is the ONE Hanzo term for an external-service integration a flow
// step invokes (Slack/GitHub/Stripe/…). The pre-rename name "piece" (ActivePieces
// jargon) is retired on this surface; the /v1/automations/pieces path survives only
// as a back-compat alias (see automations.go). See HIP-0126.

// Catalog is the browse catalogue served at GET /v1/automations/connectors. It is
// the go:embed'd catalog/catalog.json, seeded with the Tier-A connectors and later
// overwritten with the full 700+ connector set at this EXACT schema.
type Catalog struct {
	ConnectorCount int                 `json:"connectorCount"`
	Connectors     []ConnectorMetadata `json:"connectors"`
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
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Description string     `json:"description"`
	Props       []PropSpec `json:"props"`
}

type ConnectorTrigger struct {
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Description string     `json:"description"`
	Strategy    string     `json:"strategy"`
	Props       []PropSpec `json:"props"`
}

// PropSpec describes one input property of an action/trigger. It is the ONE prop
// shape shared by the connector framework (connector.go), the catalogue, and the
// MCP tool input-schema derivation (mcp.go) — one definition, three consumers.
type PropSpec struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Type        string `json:"type"` // string|number|boolean|object|array
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

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
// /v1/automations/flows/:id/operations: a type + an opaque request the apply
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
}

// FlowStep is one resolved, executable step in the flattened chain.
type FlowStep struct {
	Name       string         `json:"name"`
	PieceName  string         `json:"pieceName"`
	ActionName string         `json:"actionName"`
	Input      map[string]any `json:"input"`
}

// StepInput is the activity's per-step input. Owner is copied verbatim from
// FlowRunInput.Owner by the workflow — an activity can never widen its own scope.
type StepInput struct {
	Owner       string         `json:"owner"`
	RunID       string         `json:"runId"`
	Name        string         `json:"name"`
	PieceName   string         `json:"pieceName"`
	ActionName  string         `json:"actionName"`
	Input       map[string]any `json:"input"`
	PrevOutputs map[string]any `json:"prevOutputs"`
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
		piece, action := stepPieceAction(a)
		if piece == "" {
			continue
		}
		out = append(out, FlowStep{
			Name:       a.Name,
			PieceName:  piece,
			ActionName: action,
			Input:      a.Settings.Input,
		})
	}
	return out
}

// stepPieceAction resolves the (piece, action) an action node dispatches to. A
// PIECE node names them explicitly; a CODE node is the built-in core.code transform.
func stepPieceAction(a *FlowAction) (piece, action string) {
	switch a.Type {
	case ActionTypeCode:
		return corePiece, "code"
	case ActionTypePiece:
		return a.Settings.PieceName, a.Settings.ActionName
	default:
		// ROUTER/LOOP are not linear-executable in Phase 1 — skip (piece=="").
		return "", ""
	}
}
