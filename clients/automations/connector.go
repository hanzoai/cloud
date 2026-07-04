package automations

import (
	"context"
	"fmt"
	"sort"
)

// Connector framework — Tier-A, first-class Go connectors. ONE registry, N
// connectors; a connector self-registers from its file's init() (mirroring
// clients/integrations' register()). Adding a connector is a new file, never a
// change to the dispatch code.
//
// A Connector's Name equals the clients/integrations provider id wherever its
// actions need custodied credentials ("slack","github",…) — so RunContext.Token
// resolves against the SAME per-org KMS custody the OAuth plane sealed. Connectors
// that need no external creds (core) name a scope that no provider owns; their
// actions never call Token.

// RunContext is what an action's Run receives. It is the connector's ENTIRE view
// of the world: the org (for logging/attribution only — never a place to widen
// scope), the resolved input, the prior steps' outputs (threaded), and Token —
// the ONLY door to a credential. Token is bound at dispatch time to the VALIDATED
// org (StepInput.Owner) and the connector's own provider id, so a connector can
// reach no other tenant's and no other provider's secret.
type RunContext struct {
	Org         string
	Input       map[string]any
	PrevOutputs map[string]any
	Token       func(secretName string) ([]byte, error)
}

// Action is one invocable capability of a connector. Props declares its inputs
// (drives the catalogue + the MCP tool input schema). Run is the side-effecting
// body; it MUST handle every error explicitly and MUST NOT leak another org's data.
type Action struct {
	Name        string
	DisplayName string
	Description string
	Props       []PropSpec
	Run         func(ctx context.Context, rc RunContext) (any, error)
}

// Trigger is one entry point of a connector. Strategy selects POLLING (cron) vs
// WEBHOOK vs MANUAL. Phase-1 triggers are catalogue/metadata only (the entry wiring
// lives in the flow's root trigger + enable/disable); a Trigger carries no Run.
type Trigger struct {
	Name        string
	DisplayName string
	Description string
	Strategy    TriggerStrategy
	Props       []PropSpec
}

// Connector is one connectable capability provider. Name == the integrations
// provider id where credentials apply.
type Connector struct {
	Name        string
	DisplayName string
	AuthType    string // "none" | "bot_token" | "oauth2" — for the catalogue card
	AuthReq     bool
	Actions     map[string]*Action
	Triggers    map[string]*Trigger
}

// registry is populated by each connector file's register() from its init(). Go
// initializes the map before any init() runs, so every connector is present by the
// time Mount snapshots it.
var registry = map[string]*Connector{}

// register adds a connector to the package registry. A nil/empty connector or a
// duplicate name is a programming error and panics at init — two connectors cannot
// own the same name (which would make credential + tool routing ambiguous).
func register(c *Connector) {
	if c == nil || c.Name == "" {
		panic("automations: register nil/empty connector")
	}
	if _, dup := registry[c.Name]; dup {
		panic("automations: duplicate connector " + c.Name)
	}
	if c.Actions == nil {
		c.Actions = map[string]*Action{}
	}
	if c.Triggers == nil {
		c.Triggers = map[string]*Trigger{}
	}
	// INF-1: MCP tool names are "<connector>_<action>", and both halves may contain
	// underscores (e.g. "google_sheets"+"append_row"), so two DIFFERENT pairs could
	// collide into ONE tool name and make resolveTool ambiguous. Refuse at init — a
	// future connector can never silently shadow another's tool.
	for an := range c.Actions {
		tool := c.Name + "_" + an
		for exName, ex := range registry {
			for exAn := range ex.Actions {
				if exName+"_"+exAn == tool {
					panic("automations: tool-name collision " + tool + " (" + c.Name + "." + an + " vs " + exName + "." + exAn + ")")
				}
			}
		}
	}
	registry[c.Name] = c
}

// lookupAction resolves (connector, action) from the registry, or an honest error
// naming exactly what was unknown. Used by the durable activity AND the MCP surface
// — ONE resolution path, so the two can never diverge on what a tool means.
func lookupAction(connector, action string) (*Connector, *Action, error) {
	c, ok := registry[connector]
	if !ok {
		return nil, nil, fmt.Errorf("unknown connector %q", connector)
	}
	a, ok := c.Actions[action]
	if !ok {
		return nil, nil, fmt.Errorf("unknown action %q on connector %q", action, connector)
	}
	return c, a, nil
}

// sortedConnectors returns the connectors by name for stable catalogue/tool output.
func sortedConnectors() []*Connector {
	out := make([]*Connector, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sortedActions returns a connector's actions by name for stable output.
func sortedActions(c *Connector) []*Action {
	out := make([]*Action, 0, len(c.Actions))
	for _, a := range c.Actions {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
