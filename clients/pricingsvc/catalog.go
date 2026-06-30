// Catalog enablement overlay: the ONE mutable state Hanzo Cloud lays over the
// static @hanzo/pricing catalog, plus the ONE gate that applies it on read.
//
// Decomplected: the goja bundle (data/pricing.json) stays the sole source of
// truth for catalog CONTENT and SHAPE. This file adds only per-entry STATE —
// {enabled, betaOrgs, overrides} keyed by (kind,id) — and a pure function that
// filters + merges that state onto the bundle's output. Go never reshapes a
// model; it only hides entries and merges an admin override patch on top.
//
// Default is "everything enabled": a model/provider with no overlay row is
// visible to every org, unchanged. An empty store therefore leaves the catalog
// exactly as the bundle ships it — no fabricated state, no regression for live
// customers until an admin acts.
package pricingsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	// modernc.org/sqlite is the pure-Go SQLite driver already in the cloud dep
	// graph (provisioningsvc uses it). Blank import registers the "sqlite" name.
	_ "modernc.org/sqlite"
)

// Overlay entity kinds. A model is keyed by its id (id||name); a provider by its
// name (the bundle's `provider` string).
const (
	kindModel    = "model"
	kindProvider = "provider"
)

// Model is one catalog entry exactly as the @hanzo/pricing bundle emits it (see
// goja/bundle.js 'models'): an opaque JSON object. The gate reads only the
// identifier (id, falling back to name) and provider, and passes every other
// field through untouched — keeping the bundle authoritative for shape.
type Model map[string]any

func str(v any) string { s, _ := v.(string); return s }

// modelID is the overlay key for a model: its slugged id when present
// (third-party, e.g. "anthropic/claude-opus-4.6"), else its name (Hanzo/Zen
// models, e.g. "zen4"). Mirrors the bundle's own lookup, which matches name OR
// id.
func modelID(m Model) string {
	if id := strings.TrimSpace(str(m["id"])); id != "" {
		return id
	}
	return strings.TrimSpace(str(m["name"]))
}

func providerID(m Model) string { return strings.TrimSpace(str(m["provider"])) }

func overlayKey(kind, id string) string { return kind + "\x00" + id }

// Overlay is the mutable enablement STATE for one catalog entry. Zero value (no
// row) == enabled, no beta orgs, no override; the gate treats an absent row as
// visible, so an empty store is a no-op.
type Overlay struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Enabled   bool            `json:"enabled"`
	BetaOrgs  []string        `json:"betaOrgs,omitempty"`
	Overrides json.RawMessage `json:"overrides,omitempty"`
	UpdatedAt int64           `json:"updatedAt,omitempty"`
}

// visibleTo reports whether org may see the entry this overlay governs: enabled
// for everyone, OR org is on the beta list (private beta of an otherwise-hidden
// entry). The absent-row case (default-visible) is handled by the caller.
func (o Overlay) visibleTo(org string) bool {
	if o.Enabled {
		return true
	}
	if org == "" {
		return false
	}
	for _, b := range o.BetaOrgs {
		if b == org {
			return true
		}
	}
	return false
}

// VisibleCatalog applies the enablement overlay to the bundle's full model list
// for org. A model is visible iff its OWN overlay AND its provider's overlay
// both admit org (enabled, or org on the beta list); an entry with no overlay
// row is visible by default, so a provider with no row never hides its models.
// Returned models carry any admin override merged on top (RFC 7386). isAdmin
// callers receive EVERY model — disabled ones included — each annotated under
// "_overlay" so the admin UI can render and toggle it.
//
// Pure over (full, snap, org, isAdmin): no IO, no globals. This is the unit
// under test; the wiring layer fetches `full` from goja and `snap` from the
// store, then calls it.
func VisibleCatalog(full []Model, snap map[string]Overlay, org string, isAdmin bool) []Model {
	out := make([]Model, 0, len(full))
	for _, m := range full {
		mo, mok := snap[overlayKey(kindModel, modelID(m))]
		po, pok := snap[overlayKey(kindProvider, providerID(m))]
		visible := (!mok || mo.visibleTo(org)) && (!pok || po.visibleTo(org))
		if !isAdmin && !visible {
			continue
		}
		merged := mergeModel(m, mo.Overrides)
		if isAdmin {
			merged["_overlay"] = modelAdminState(mo, mok, po, pok)
		}
		out = append(out, merged)
	}
	return out
}

// VisibleProviders filters a provider dict (name -> info) by the provider
// overlay for org, merging provider overrides (RFC 7386). isAdmin callers get
// every provider with state annotated under each provider's "_overlay".
func VisibleProviders(providers map[string]any, snap map[string]Overlay, org string, isAdmin bool) map[string]any {
	out := make(map[string]any, len(providers))
	for name, info := range providers {
		po, ok := snap[overlayKey(kindProvider, name)]
		visible := !ok || po.visibleTo(org)
		if !isAdmin && !visible {
			continue
		}
		infoMap, isMap := info.(map[string]any)
		if !isMap {
			out[name] = info // opaque value: nothing to override or annotate.
			continue
		}
		merged := applyMergePatch(infoMap, overridePatch(po.Overrides))
		if isAdmin {
			merged["_overlay"] = providerAdminState(po, ok)
		}
		out[name] = merged
	}
	return out
}

func modelAdminState(mo Overlay, mok bool, po Overlay, pok bool) map[string]any {
	st := map[string]any{
		"modelEnabled":    !mok || mo.Enabled,
		"providerEnabled": !pok || po.Enabled,
	}
	if mok {
		if len(mo.BetaOrgs) > 0 {
			st["modelBetaOrgs"] = mo.BetaOrgs
		}
		if len(mo.Overrides) > 0 {
			st["modelOverrides"] = mo.Overrides
		}
	}
	if pok && len(po.BetaOrgs) > 0 {
		st["providerBetaOrgs"] = po.BetaOrgs
	}
	return st
}

func providerAdminState(po Overlay, ok bool) map[string]any {
	st := map[string]any{"providerEnabled": !ok || po.Enabled}
	if ok {
		if len(po.BetaOrgs) > 0 {
			st["betaOrgs"] = po.BetaOrgs
		}
		if len(po.Overrides) > 0 {
			st["overrides"] = po.Overrides
		}
	}
	return st
}

// ----- RFC 7386 JSON Merge Patch -------------------------------------------
//
// One override semantics, the standard one: object values merge recursively,
// null deletes a key, anything else replaces. The same patch a JSON Merge Patch
// client (the admin UI) would expect. Input maps are never mutated.

func mergeModel(base Model, raw json.RawMessage) Model {
	return Model(applyMergePatch(base, overridePatch(raw)))
}

func overridePatch(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil // malformed override never corrupts the catalog; ignored.
	}
	return m
}

func applyMergePatch(target, patch map[string]any) map[string]any {
	out := make(map[string]any, len(target)+len(patch))
	for k, v := range target {
		out[k] = v
	}
	for k, pv := range patch {
		if pv == nil {
			delete(out, k)
			continue
		}
		if pm, ok := pv.(map[string]any); ok {
			if tm, ok2 := out[k].(map[string]any); ok2 {
				out[k] = applyMergePatch(tm, pm)
			} else {
				out[k] = applyMergePatch(map[string]any{}, pm)
			}
			continue
		}
		out[k] = pv
	}
	return out
}

// ----- store ----------------------------------------------------------------

// catalog is the enablement overlay store: one SQLite table mapping (kind,id) ->
// {enabled, betaOrgs, overrides}. ONE file holds every entry; reads load a full
// snapshot once per request and the gate then runs purely in memory.
type catalog struct {
	db *sql.DB
}

// openCatalog opens (creating if needed) the overlay DB at path and migrates it.
// path may be ":memory:" for an ephemeral, non-persistent overlay (degraded
// mode when no DataDir is configured). The "sqlite" driver is modernc's pure-Go
// build. MaxOpenConns(1) serializes writes against the file lock without retry.
func openCatalog(path string) (*catalog, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	c := &catalog{db: db}
	if err := c.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return c, nil
}

func (c *catalog) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS catalog_overlay (
  kind       TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  beta_orgs  TEXT    NOT NULL DEFAULT '[]',
  overrides  TEXT    NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, id)
);`
	if _, err := c.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (c *catalog) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

const overlayCols = `kind,id,enabled,beta_orgs,overrides,updated_at`

func scanOverlay(sc interface{ Scan(...any) error }) (Overlay, error) {
	var (
		o        Overlay
		enabled  int
		betaJSON string
		ovr      string
	)
	if err := sc.Scan(&o.Kind, &o.ID, &enabled, &betaJSON, &ovr, &o.UpdatedAt); err != nil {
		return Overlay{}, err
	}
	o.Enabled = enabled != 0
	if betaJSON != "" && betaJSON != "[]" {
		_ = json.Unmarshal([]byte(betaJSON), &o.BetaOrgs)
	}
	if ovr != "" {
		o.Overrides = json.RawMessage(ovr)
	}
	return o, nil
}

// Snapshot loads every overlay row keyed overlayKey(kind,id). One read per
// request; an empty store yields an empty map (the gate leaves the catalog
// untouched).
func (c *catalog) Snapshot(ctx context.Context) (map[string]Overlay, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+overlayCols+` FROM catalog_overlay`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Overlay{}
	for rows.Next() {
		o, err := scanOverlay(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out[overlayKey(o.Kind, o.ID)] = o
	}
	return out, rows.Err()
}

// Get returns the overlay for (kind,id) and whether a row exists.
func (c *catalog) Get(ctx context.Context, kind, id string) (Overlay, bool, error) {
	row := c.db.QueryRowContext(ctx,
		`SELECT `+overlayCols+` FROM catalog_overlay WHERE kind=? AND id=?`, kind, id)
	o, err := scanOverlay(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Overlay{}, false, nil
	}
	if err != nil {
		return Overlay{}, false, fmt.Errorf("get: %w", err)
	}
	return o, true, nil
}

// Upsert writes the full overlay row, replacing any existing (kind,id) row.
func (c *catalog) Upsert(ctx context.Context, o Overlay) error {
	beta := "[]"
	if len(o.BetaOrgs) > 0 {
		b, err := json.Marshal(o.BetaOrgs)
		if err != nil {
			return fmt.Errorf("marshal betaOrgs: %w", err)
		}
		beta = string(b)
	}
	enabled := 0
	if o.Enabled {
		enabled = 1
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO catalog_overlay (`+overlayCols+`) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(kind,id) DO UPDATE SET
		   enabled=excluded.enabled,
		   beta_orgs=excluded.beta_orgs,
		   overrides=excluded.overrides,
		   updated_at=excluded.updated_at`,
		o.Kind, o.ID, enabled, beta, string(o.Overrides), o.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	return nil
}

// Models fetches the live overlay snapshot and gates `full` for org — the ONE
// call the read path makes.
func (c *catalog) Models(ctx context.Context, full []Model, org string, isAdmin bool) ([]Model, error) {
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return VisibleCatalog(full, snap, org, isAdmin), nil
}
