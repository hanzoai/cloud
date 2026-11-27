package provider

import (
	"context"
	"fmt"
	"sort"
)

// action.go is the provider's CAPABILITY half: what a provider can be asked to do,
// beside the credential half that says how it is reached.
//
// One registry holds both. Before this file a provider was registered twice under
// two words — here for its consent flow, and again in apps/auto as a "Connector"
// for its actions — so "x" was two things and the path that listed them said
// /v1/provider/connectors. A provider HAS auth and HAS actions; splitting those
// across two packages made the same noun mean two things and left an agent asking
// the MCP surface what it could do reading one half of the answer.
//
// WHERE AN ACTION'S BODY LIVES. Not necessarily here. A provider whose action calls
// another app — content's generate, destination's fanout — cannot have its body in
// this package, because those apps already import it and the import would close a
// cycle. So the registry is here and the implementations register into it from
// wherever they belong, through RegisterActions. The registry is a value; who fills
// it is a separate question.

// RunContext is what an action's Run receives. It is the action's ENTIRE view of the
// world: the org (for logging and attribution only — never a place to widen scope),
// the resolved input, the prior steps' outputs (threaded), and Token — the ONLY path
// to a credential. Token is bound at dispatch time to the VALIDATED org and the
// provider's own id, so an action can reach no other tenant's and no other
// provider's secret.
type RunContext struct {
	Org         string
	Input       map[string]any
	PrevOutputs map[string]any
	Token       func(secretName string) ([]byte, error)
}

// PropSpec declares one input of an action or trigger. It drives the catalogue card
// and the MCP tool's input schema, so it is the only place an argument is described.
type PropSpec struct {
	// Name is the input's key in the action's input object.
	Name string `json:"name"`
	// DisplayName is what a form calls it.
	DisplayName string `json:"displayName,omitempty"`
	// Type is the JSON type the value takes: string|number|boolean|object|array.
	Type string `json:"type"`
	// Required is whether the action refuses to run without it.
	Required bool `json:"required,omitempty"`
	// Description says what the value is.
	Description string `json:"description,omitempty"`
}

// Action is one invocable capability of a provider. Props declares its inputs; Run
// is the side-effecting body, which MUST handle every error explicitly and MUST NOT
// leak another org's data.
type Action struct {
	Name        string
	DisplayName string
	Description string
	Props       []PropSpec
	Run         func(ctx context.Context, rc RunContext) (any, error)
}

// TriggerStrategy selects HOW a flow starts: POLLING → a cron schedule on the tasks
// engine; WEBHOOK/APP_WEBHOOK → an inbound HTTP event; MANUAL → an explicit run.
type TriggerStrategy string

const (
	StrategyPolling    TriggerStrategy = "POLLING"
	StrategyWebhook    TriggerStrategy = "WEBHOOK"
	StrategyAppWebhook TriggerStrategy = "APP_WEBHOOK"
	StrategyManual     TriggerStrategy = "MANUAL"
)

// Trigger is one entry point of a provider. Triggers are catalogue metadata: the
// entry wiring lives in the flow's root trigger and its enable/disable, so a Trigger
// carries no Run.
type Trigger struct {
	Name        string
	DisplayName string
	Description string
	Strategy    TriggerStrategy
	Props       []PropSpec
}

// RegisterActions attaches actions and triggers to a provider id. It is the ONE way
// actions reach the registry, including for registry declared in this package, so
// there is never a second place to look for what a provider can do.
//
// An id with no registered provider gets an action-only one: `core` and `waitlist`
// spend no customer credential, so they have no consent flow to declare and their
// actions never call Token.
//
// Called from init(). A duplicate action, or one whose MCP tool name collides with
// another provider's, is a programming error and panics at init rather than
// resolving ambiguously at call time.
func RegisterActions(id, displayName string, actions map[string]*Action, triggers map[string]*Trigger) {
	if id == "" {
		panic("provider: RegisterActions with no provider id")
	}
	withPieces()
	p, ok := registry[id]
	if !ok {
		p = &Provider{ID: id, Name: displayName}
		registry[id] = p
	}
	if p.Actions == nil {
		p.Actions = map[string]*Action{}
	}
	if p.Triggers == nil {
		p.Triggers = map[string]*Trigger{}
	}
	for name, a := range actions {
		if _, dup := p.Actions[name]; dup {
			panic("provider: duplicate action " + id + "." + name)
		}
		// MCP tool names are "<provider>_<action>" and both halves may contain
		// underscores, so two different pairs can collide into one tool name and
		// make tool resolution ambiguous. Refuse at init — a provider added later
		// can never silently shadow another's tool.
		tool := id + "_" + name
		for exID, ex := range registry {
			for exName := range ex.Actions {
				if exID+"_"+exName == tool {
					panic("provider: tool-name collision " + tool + " (" + id + "." + name + " vs " + exID + "." + exName + ")")
				}
			}
		}
		p.Actions[name] = a
	}
	for name, t := range triggers {
		if _, dup := p.Triggers[name]; dup {
			panic("provider: duplicate trigger " + id + "." + name)
		}
		p.Triggers[name] = t
	}
}

// Registered reports whether an id names a provider at all, which is the fact
// that distinguishes an unknown provider from an unknown action on a known one.
// It exists so a caller can ask that behaviourally instead of matching on the
// wording of ActionFor's error.
func Registered(id string) bool {
	withPieces()
	_, ok := registry[id]
	return ok
}

// ActionFor resolves (provider, action) or an honest error naming exactly what was
// unknown. The durable activity and the MCP surface both call it, so the two can
// never diverge on what a tool means.
func ActionFor(id, action string) (*Provider, *Action, error) {
	withPieces()
	p, ok := registry[id]
	if !ok {
		return nil, nil, fmt.Errorf("unknown provider %q", id)
	}
	a, ok := p.Actions[action]
	if !ok {
		return nil, nil, fmt.Errorf("unknown action %q on provider %q", action, id)
	}
	return p, a, nil
}

// WithActions returns the registry that carry at least one action, by id, for
// stable catalogue and tool output. A provider with none is reachable for consent
// and offers nothing to invoke, so it is not a tool.
func WithActions() []*Provider {
	withPieces()
	out := make([]*Provider, 0, len(registry))
	for _, p := range registry {
		if len(p.Actions) > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SortedActions returns a provider's actions by name for stable output.
func SortedActions(p *Provider) []*Action {
	out := make([]*Action, 0, len(p.Actions))
	for _, a := range p.Actions {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
