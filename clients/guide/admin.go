package guide

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// admin.go is the AUTHORING concern: the SuperAdmin cockpit backend for the brand
// blueprint (the middle resolution tier). It is the ONE plane that WRITES the shared
// platform blueprint, and it is gated on SuperAdmin (owner=="admin", the reserved admin
// org) — the ONE cross-tenant predicate. A normal org member OR a per-org admin
// (IsOrgAdmin) gets 403: the brand blueprint is SHARED platform content, not a
// per-customer surface. Trusting per-org isAdmin here would be a privilege escalation.
//
// Every write is fail-closed (a bad blueprint is rejected and never becomes active) and
// versioned (each edit is a new immutable version — point-in-time recovery, the
// legal-template discipline). Edits take effect immediately: the next resolve reads the
// DB's latest version.

// superAdmin wraps a handler so it runs ONLY for a platform SuperAdmin. The gate is
// structural — every blueprint route is registered through it — so it can never be
// forgotten on a new route.
func superAdmin(s *cloud.Service[state], fn func(*cloud.Service[state], *zip.Ctx) error) zip.Handler {
	return cloud.Handle(s, func(s *cloud.Service[state], c *zip.Ctx) error {
		if !principal.IsSuperAdmin(c) {
			return zip.ErrForbidden("SuperAdmin required: the brand blueprint is platform content")
		}
		return fn(s, c)
	})
}

// authoringBlueprint loads the blueprint the admin plane edits — the deployment's
// resolved brand blueprint from the DB — and the brand KEY writes target. Reads and
// writes key off the SAME resolution (LatestResolved), so a deployment authors exactly
// the row it serves. Post-seed the DB always carries a row; the fixture fallback covers
// only a pre-seed / unreachable DB, where a write creates version 1 under the base key.
func (st state) authoringBlueprint(ctx context.Context) (Blueprint, string, int, error) {
	// The fixture fallbacks return a CLONE, never the shared st.defBlueprint by value: a
	// caller (patchBlueprintItem → patchIn) edits the returned blueprint IN PLACE, and a
	// by-value struct copy would share the embedded fixture's slice backing — a pre-seed
	// PATCH would corrupt the process-wide fixture. clone() gives independent slices.
	if st.blueprints == nil {
		return st.defBlueprint.clone(), "", 0, nil
	}
	doc, version, key, ok, err := st.blueprints.LatestResolved(ctx, st.brand)
	if err != nil {
		return Blueprint{}, "", 0, err
	}
	if !ok {
		return st.defBlueprint.clone(), key, 0, nil // pre-seed / unreachable: fixture as the base
	}
	bp, err := Parse(doc)
	if err != nil {
		return Blueprint{}, "", 0, err // a stored doc that no longer parses — refuse, don't guess
	}
	return bp, key, version, nil
}

// writeKey resolves the brand KEY an author write targets WITHOUT parsing the stored doc.
// putBlueprint / listBlueprintVersions need only the key, so they must not depend on the
// current stored doc parsing: a corrupt or schema-drifted row must still be replaceable by
// a valid PUT (and its versions listable). LatestResolved returns the resolution key
// regardless of whether a row exists (brand → base "").
func (st state) writeKey(ctx context.Context) (string, error) {
	if st.blueprints == nil {
		return "", nil // pre-seed / no store: writes create version 1 under the base key
	}
	_, _, key, _, err := st.blueprints.LatestResolved(ctx, st.brand)
	return key, err
}

// saveBlueprint persists a new version of the brand blueprint under key. The blueprint's
// own brand field is pinned to the key so the stored doc stays coherent with its slot.
func (st state) saveBlueprint(ctx context.Context, key string, bp Blueprint) (int, error) {
	bp.Brand = key
	doc, err := json.Marshal(bp)
	if err != nil {
		return 0, err
	}
	return st.blueprints.SaveVersion(ctx, key, doc, time.Now().Unix())
}

// getBlueprint returns the FULL authored brand blueprint — every section/step/strategy/
// template WITH its enabled flag (including disabled items, which the org-facing reads
// never see) — plus the active version and the brand key. SuperAdmin only.
func getBlueprint(s *cloud.Service[state], c *zip.Ctx) error {
	bp, key, version, err := s.State.authoringBlueprint(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	return c.JSON(http.StatusOK, blueprintResponse(bp, key, version))
}

// putBlueprint replaces the whole brand blueprint (a new version). The body must parse
// AND validate as a blueprint; a bad body is 422 and never becomes active (fail-closed,
// so a redeploy's re-seed or the previous version stays authoritative).
func putBlueprint(s *cloud.Service[state], c *zip.Ctx) error {
	body := c.Body()
	if len(body) == 0 {
		return zip.ErrBadRequest("empty blueprint body")
	}
	if len(body) > maxBlueprint {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "blueprint exceeds the %d-byte limit", maxBlueprint)
	}
	bp, err := Parse(body)
	if err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	if s.State.blueprints == nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: blueprint store unavailable")
	}
	// Fetch the write key WITHOUT parsing the current stored doc: a corrupt / schema-drifted
	// row must not block a valid PUT from overwriting it (fail-closed on write path).
	key, err := s.State.writeKey(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	version, err := s.State.saveBlueprint(c.Context(), key, bp)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	auditBlueprint(s, c, "guide.blueprint.put", key, version, map[string]any{
		"principles": len(bp.Principles), "sections": len(bp.Sections), "steps": len(bp.Steps),
		"strategies": len(bp.Strategies), "templates": len(bp.Templates),
	})
	return c.JSON(http.StatusOK, blueprintResponse(bp, key, version))
}

// listBlueprintVersions returns the brand blueprint's version history (metadata only) —
// the point-in-time-recovery / audit trail. SuperAdmin only.
func listBlueprintVersions(s *cloud.Service[state], c *zip.Ctx) error {
	if s.State.blueprints == nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: blueprint store unavailable")
	}
	// Key without parsing: a corrupt stored doc must not block listing the version history.
	key, err := s.State.writeKey(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	versions, err := s.State.blueprints.ListVersions(c.Context(), key)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"brand": key, "versions": versions})
}

// patchBlueprintItem edits ONE item in a collection (sections|steps|strategies|
// templates) by id — the "edit an item" AND the headline "enable/disable an item"
// lever (disable is PATCH {"enabled": false}). It applies a JSON merge-patch onto the
// item, re-validates the WHOLE blueprint (fail-closed — a patch that would dangle a dep
// or break the DAG is rejected), and saves a new version. SuperAdmin only.
func patchBlueprintItem(s *cloud.Service[state], c *zip.Ctx) error {
	collection := strings.ToLower(strings.TrimSpace(c.Param("collection")))
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("item id required")
	}
	patch := c.Body()
	if len(patch) == 0 {
		return zip.ErrBadRequest("empty patch body")
	}
	if len(patch) > maxBlueprint {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "patch exceeds the %d-byte limit", maxBlueprint)
	}
	bp, key, _, err := s.State.authoringBlueprint(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	var found bool
	switch collection {
	case "sections":
		found, err = patchIn(bp.Sections, id, func(x Section) string { return x.ID }, patch)
	case "steps":
		found, err = patchIn(bp.Steps, id, func(x Step) string { return x.ID }, patch)
	case "strategies":
		found, err = patchIn(bp.Strategies, id, func(x Strategy) string { return x.ID }, patch)
	case "templates":
		found, err = patchIn(bp.Templates, id, func(x Template) string { return x.ID }, patch)
	default:
		return zip.ErrBadRequest("unknown collection " + collection + " (want sections|steps|strategies|templates)")
	}
	if err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	if !found {
		return zip.ErrNotFound(collection + " item not found: " + id)
	}
	// Fail-closed: the mutated blueprint must still be a valid blueprint (the patch can
	// never dangle a dependency, break the DAG, or empty the journey).
	if err := bp.Validate(); err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	version, err := s.State.saveBlueprint(c.Context(), key, bp)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	auditBlueprint(s, c, "guide.blueprint.item.patch", key, version, map[string]any{"collection": collection, "id": id})
	return c.JSON(http.StatusOK, blueprintResponse(bp, key, version))
}

// patchIn finds the item with id in items and applies patch to it IN PLACE (the slice
// shares its backing array with the blueprint, so the blueprint reflects the edit).
func patchIn[T any](items []T, id string, idOf func(T) string, patch []byte) (bool, error) {
	for i := range items {
		if idOf(items[i]) != id {
			continue
		}
		merged, err := mergeItemPatch(items[i], patch)
		if err != nil {
			return false, err
		}
		items[i] = merged
		return true, nil
	}
	return false, nil
}

// mergeItemPatch applies a JSON merge-patch (an RFC 7386-style key overlay) onto item,
// round-tripping through its JSON tags so the patch respects the schema, and FORBIDS a
// change to the immutable id. Only the keys present in the patch are changed; every
// other field is preserved.
func mergeItemPatch[T any](item T, patch []byte) (T, error) {
	var zero T
	base, err := json.Marshal(item)
	if err != nil {
		return zero, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return zero, err
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(patch, &p); err != nil {
		return zero, err
	}
	delete(p, "id") // id is the immutable key; a patch can never rekey an item
	for k, v := range p {
		m[k] = v
	}
	merged, err := json.Marshal(m)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(merged, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// blueprintResponse is the admin plane's envelope: the full blueprint (with every
// enabled flag made explicit so the FE has no ambiguity), the active version, the brand
// key, and item counts.
func blueprintResponse(bp Blueprint, key string, version int) map[string]any {
	return map[string]any{
		"brand":     key,
		"version":   version,
		"blueprint": explicitEnabled(bp),
		"counts": map[string]int{
			"principles": len(bp.Principles),
			"sections":   len(bp.Sections),
			"steps":      len(bp.Steps),
			"strategies": len(bp.Strategies),
			"templates":  len(bp.Templates),
		},
	}
}

// explicitEnabled returns a copy of bp with every enable pointer made explicit (nil →
// the default-on true), so the admin FE sees an unambiguous on/off on every item. The
// slices are copied so the in-memory blueprint is untouched. Principles carry no enable
// lever (the spine is the fixed backbone) but are copied too, so the returned Blueprint is
// fully independent of the source.
func explicitEnabled(bp Blueprint) Blueprint {
	bp.Enabled = boolPtr(on(bp.Enabled))
	bp.Principles = append([]Principle(nil), bp.Principles...)
	bp.Sections = append([]Section(nil), bp.Sections...)
	for i := range bp.Sections {
		bp.Sections[i].Enabled = boolPtr(on(bp.Sections[i].Enabled))
	}
	bp.Steps = append([]Step(nil), bp.Steps...)
	for i := range bp.Steps {
		bp.Steps[i].Enabled = boolPtr(on(bp.Steps[i].Enabled))
	}
	bp.Strategies = append([]Strategy(nil), bp.Strategies...)
	for i := range bp.Strategies {
		bp.Strategies[i].Enabled = boolPtr(on(bp.Strategies[i].Enabled))
	}
	bp.Templates = append([]Template(nil), bp.Templates...)
	for i := range bp.Templates {
		bp.Templates[i].Enabled = boolPtr(on(bp.Templates[i].Enabled))
	}
	return bp
}

// auditBlueprint records a SuperAdmin blueprint edit on the shared tamper-evident trail.
// The `after` map carries opaque ids + counts ONLY (never a rendered body), redacted as
// a second layer. Best-effort: a trail append failure logs but never fails the edit.
func auditBlueprint(s *cloud.Service[state], c *zip.Ctx, action, brand string, version int, after map[string]any) {
	if s.State.audit == nil {
		return
	}
	after["brand"], after["version"] = brand, version
	blob, _ := json.Marshal(after)
	rec := audit.Record{
		Actor:     audit.Actor{Org: principal.Owner(c), Sub: c.User(), Email: c.UserEmail()},
		Action:    action,
		Resource:  audit.Resource{Type: "guide.blueprint", ID: brand},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: c.IsAdmin()},
		Outcome:   audit.Outcome{Result: "success", Status: 200},
		Method:    c.Method(),
		Path:      c.Path(),
		RequestID: c.RequestID(),
		After:     audit.Redact(blob),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("guide blueprint audit append failed", "err", err, "action", action)
	}
}
