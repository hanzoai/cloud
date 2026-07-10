package content

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/framework"
)

// hooks.go is the seam between the framework DocType lifecycle and the ONE marketing
// state machine (lifecycle.go). For every publishable marketing DocType it registers a
// before_save GATE that enforces status-edge legality on create AND update — so an
// illegal transition is impossible no matter who writes: the /v1/content transition
// endpoint, a raw PUT /v1/framework/:doctype, the console's generic renderer, or an
// automations flow step. The rule lives in lifecycle.go; this hook only enforces it at
// the storage boundary. It has NO side effects (no distribution, no external calls) —
// side effects are orchestration and live in content.go, never in the engine.
//
// before_save is a gate (a returned error aborts the write → HTTP 422) and runs
// OUTSIDE the store transaction, exactly as the framework contract specifies.
func registerHooks() {
	for _, dt := range publishableDocTypes {
		framework.RegisterHook(dt, framework.ActionBeforeSave, enforceLifecycle)
	}
}

// enforceLifecycle rejects an illegal status transition.
//
//   - Create (ev.Prev == nil): a new document must START in draft. The Select field's
//     Default is draft, so an omitted status is already draft; only an explicit
//     non-draft create is rejected — content is authored, then advanced, never born
//     live.
//   - Update (ev.Prev != nil): the new status must be reachable from the previous one
//     via a legal edge (CanTransition). A no-op (unchanged status) is always legal, so
//     an ordinary field edit that leaves status untouched passes.
//
// ev.Org is the VALIDATED tenant; this hook reads only the event's own document data,
// so it is inherently in-tenant.
func enforceLifecycle(_ context.Context, ev *framework.Event) error {
	to := statusOf(ev.Doc)
	if to == "" {
		// A publishable DocType always has a status (Default draft); an empty value
		// means the field is absent from this write — nothing to enforce.
		return nil
	}
	if !IsStatus(to) {
		return fmt.Errorf("unknown %s status %q", ev.DocType, to)
	}
	if ev.Prev == nil { // create
		if to != StatusDraft {
			return fmt.Errorf("a new %s must start in %q, not %q", ev.DocType, StatusDraft, to)
		}
		return nil
	}
	from := statusOf(ev.Prev)
	if from == "" {
		from = StatusDraft // a legacy row with no status is treated as a draft
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal %s transition %q → %q", ev.DocType, from, to)
	}
	return nil
}

// statusOf reads the trimmed status string from a document, or "" if unset/non-string.
func statusOf(d *framework.Document) string {
	if d == nil || d.Data == nil {
		return ""
	}
	s, _ := d.Data[StatusField].(string)
	return strings.TrimSpace(s)
}
