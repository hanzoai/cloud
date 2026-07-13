package content

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/hanzoai/cloud/clients/framework"
)

// hooks.go is the seam between the framework DocType lifecycle and the ONE marketing
// state machine (lifecycle.go). For every publishable marketing DocType it registers
// before_save GATES that enforce, at the storage boundary, two rules no matter who
// writes (the /v1/content endpoints, a raw PUT /v1/framework/:doctype, the console's
// generic renderer, an automations flow step):
//
//  1. enforceLifecycle — status-edge legality (the rule lives in lifecycle.go).
//  2. enforceServerOwned — server-managed fields (external_ids, published_at) may only
//     be written by a TRUSTED in-process op (Publish/Transition), never by a client.
//
// Both are pure gates (a returned error aborts the write → HTTP 422) with NO side
// effects — distribution and timestamp stamping are orchestration and live in
// content.go, never in the engine. They run OUTSIDE the store transaction, exactly as
// the framework contract specifies, in registration order (lifecycle first).
func registerHooks() {
	for _, dt := range publishableDocTypes {
		framework.RegisterHook(dt, framework.ActionBeforeSave, enforceLifecycle)
		framework.RegisterHook(dt, framework.ActionBeforeSave, enforceServerOwned)
	}
	// Campaign carries the ONE explicit commerce join key (`product`); the integrity gate
	// verifies it resolves to a real catalog product. It is Campaign-only on purpose:
	// Asset.`design` is a studio design slug that only BY CONVENTION equals a product slug
	// and is authored render-first (before the catalog product exists), so gating it would
	// reject the very first step of the storefront loop; the publish edge already
	// fail-closes when a published Asset's design is not a real store product.
	framework.RegisterHook(DocTypeCampaign, framework.ActionBeforeSave, enforceCatalogRefs)
}

// serverOwnedFields are the reconciliation/confirmation columns that reflect SERVER
// truth and must never be set by a client write: `external_ids` is written ONLY by the
// Publish fan-out (the distributor's returned post ids), `published_at` ONLY by a
// Transition to `published`. A DocType that lacks a field simply never carries it, so
// this ONE set is safe to apply across every publishable type. The framework declares
// them ReadOnly in the schema, but ReadOnly is a UI hint the store does not enforce —
// this hook is where the guarantee is actually enforced.
var serverOwnedFields = []string{"external_ids", "published_at"}

// trustedWriteKey marks a context whose write is a TRUSTED first-party op (Publish /
// Transition) permitted to write server-owned fields. It is an unexported, in-process
// value: an HTTP client can never inject a Go context value, so a raw PUT never carries
// it and is always guarded. This is the same shape by which framework's trusted
// in-process writes (Ingest) differ from a raw PUT — the trust is carried in-band, in Go.
type trustedWriteKey struct{}

// withTrustedWrite tags ctx as a trusted server write. Publish and Transition wrap the
// context they hand to framework.UpdateData so their server-owned-field writes pass the
// enforceServerOwned gate.
func withTrustedWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedWriteKey{}, true)
}

// isTrustedWrite reports whether ctx was tagged by withTrustedWrite.
func isTrustedWrite(ctx context.Context) bool {
	v, _ := ctx.Value(trustedWriteKey{}).(bool)
	return v
}

// enforceServerOwned rejects a client-attempted CHANGE to a server-owned field, so a
// client cannot forge external_ids to suppress a real fan-out (skipDistributed would
// then filter every channel) nor fake published_at to look live. A TRUSTED write
// (Publish/Transition) is the ONLY writer and is exempt.
//
//   - Create (ev.Prev == nil): a client may not birth a server-owned field with a value.
//   - Update: an OMITTED field is PRESERVED from the stored value (a partial edit never
//     drops reconciliation state); a field byte-equal to the stored value is a no-op
//     (a full-doc round-trip is fine); a DIFFERENT value is a client mutation → reject.
func enforceServerOwned(ctx context.Context, ev *framework.Event) error {
	if isTrustedWrite(ctx) {
		return nil // Publish/Transition are the trusted writers
	}
	for _, f := range serverOwnedFields {
		newVal, present := ev.Doc.Data[f]
		if ev.Prev == nil { // create
			if present && !isEmptyServerField(newVal) {
				return fmt.Errorf("%s.%s is server-managed and cannot be set by a client", ev.DocType, f)
			}
			continue
		}
		old := ev.Prev.Data[f]
		if !present {
			// Client omitted it: preserve the stored value rather than replacing it away
			// (framework update is full-replace on the validated field set).
			if old != nil {
				ev.Doc.Data[f] = old
			}
			continue
		}
		if !serverFieldEqual(newVal, old) {
			return fmt.Errorf("%s.%s is server-managed and cannot be changed by a client write", ev.DocType, f)
		}
	}
	return nil
}

// serverFieldEqual reports whether an incoming server-owned value matches the stored
// one, treating "no ids"/"unset" spellings (absent, empty string, empty map) as equal so
// a benign round-trip is never mistaken for a mutation.
func serverFieldEqual(a, b any) bool {
	if isEmptyServerField(a) && isEmptyServerField(b) {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// isEmptyServerField reports whether a server-owned value carries no information.
func isEmptyServerField(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case map[string]any:
		return len(x) == 0
	default:
		return false
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

// enforceCatalogRefs is the before_save integrity gate on a Campaign's `product` join
// key: a campaign that names a commerce product must name one the org's catalog actually
// has (a dangling handle would drive generation/distribution for a nonexistent product).
//
//   - It validates ONLY when the handle is newly set or CHANGED — never on an unrelated
//     re-save (e.g. a status transition) — so a later catalog edit can never wedge an
//     existing campaign's lifecycle.
//   - It resolves via the ONE commerce edge content already owns (Storefront.ProductExists).
//     When that edge is not wired (local dev / no commerce), or reachable-but-erroring, it
//     SKIPS — integrity is enforced only where it can actually be checked, and a commerce
//     outage never wedges content authoring. Only a clean "resolved, no such product"
//     answer fails the write closed.
//
// ev.Org is the VALIDATED tenant; the lookup is X-Org-Id-pinned to it (no cross-org read).
func enforceCatalogRefs(ctx context.Context, ev *framework.Event) error {
	handle := strings.TrimSpace(dataString(ev.Doc.Data, "product"))
	if handle == "" {
		return nil
	}
	if ev.Prev != nil && strings.TrimSpace(dataString(ev.Prev.Data, "product")) == handle {
		return nil // unchanged reference — already validated when it was set
	}
	s := mounted
	if s == nil || s.State.sf == nil {
		return nil // content not mounted / no edge — cannot verify, so do not block
	}
	ok, err := s.State.sf.ProductExists(ctx, ev.Org, handle)
	if err != nil {
		if !errors.Is(err, errNotConfigured) && ev.Logger != nil {
			// Commerce was reachable but errored — do not wedge the write on a transient
			// blip; the dangling-handle guard is best-effort against a live catalog.
			ev.Logger.Warn("campaign product validation skipped (commerce edge error)",
				"org", ev.Org, "product", handle, "err", err)
		}
		return nil
	}
	if !ok {
		return fmt.Errorf("%s.product %q does not resolve to a product in this org's catalog", ev.DocType, handle)
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
