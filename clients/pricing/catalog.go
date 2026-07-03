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
package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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
//
// Tri-state (the #30/#31 enablement model), encoded by (Enabled, Beta):
//   - ga   = Enabled            → visible to EVERYONE.
//   - beta = !Enabled && Beta    → hidden from the public; visible to opted-in/
//     granted orgs (BetaOrgs). Users may SELF-OPT-IN to a beta item.
//   - off  = !Enabled && !Beta   → hidden from EVERYONE, absolutely. BetaOrgs are
//     IGNORED — a self-opt-in can never bypass an `off` kill switch.
type Overlay struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Enabled   bool            `json:"enabled"`
	Beta      bool            `json:"beta,omitempty"`
	BetaOrgs  []string        `json:"betaOrgs,omitempty"`
	Overrides json.RawMessage `json:"overrides,omitempty"`
	UpdatedAt int64           `json:"updatedAt,omitempty"`
}

// State is the tri-state label (off|beta|ga) an operator sets and the console
// renders. Derived from (Enabled, Beta) so there is ONE source of truth.
func (o Overlay) State() string {
	switch {
	case o.Enabled:
		return "ga"
	case o.Beta:
		return "beta"
	default:
		return "off"
	}
}

// visibleTo reports whether org may see the entry this overlay governs:
//   - ga (Enabled): visible to everyone.
//   - off (!Enabled && !Beta): visible to NO ONE — the BetaOrgs list is ignored,
//     so a stray/self-added org can never see an off item (the kill switch is
//     absolute; this is the security invariant a self-opt-in must not bypass).
//   - beta (!Enabled && Beta): visible only to an org on the BetaOrgs list.
//
// The absent-row case (default-visible) is handled by the caller.
func (o Overlay) visibleTo(org string) bool {
	if o.Enabled {
		return true
	}
	if !o.Beta {
		return false // off — absolute; BetaOrgs cannot re-open it.
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

// optedIn reports whether org is on this overlay's beta list.
func (o Overlay) optedIn(org string) bool {
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

// GateRootData gates the kitchen-sink root payload (GET /v1/pricing returns the
// whole pricing blob) IN PLACE, so the root shows the exact same gated catalog
// as the leaf routes — never an un-gated second source. It filters every field
// that carries a model or provider identity:
//   - hanzoModels + thirdPartyModels via VisibleCatalog,
//   - providers via VisibleProviders,
//   - the id-reference lists freeModels and families[].models, kept only if the
//     referenced model survived the gate (customers); admins keep every ref.
//
// Aggregate summary counts and non-catalog sections (tools/infrastructure/cloud)
// carry no catalog identity and are left untouched.
func GateRootData(data map[string]any, snap map[string]Overlay, org string, isAdmin bool) {
	visible := map[string]bool{}

	if arr, ok := modelArray(data["thirdPartyModels"]); ok {
		g := VisibleCatalog(arr, snap, org, isAdmin)
		data["thirdPartyModels"] = g
		for _, m := range g {
			visible[modelID(m)] = true
		}
	}
	// hanzoModels carry no provider field in raw data; tag "Hanzo" for the gate
	// exactly as the bundle's models route does, so disabling the "Hanzo"
	// provider cascades to hide them here too.
	if arr, ok := modelArray(data["hanzoModels"]); ok {
		tagged := make([]Model, len(arr))
		for i, m := range arr {
			tagged[i] = withProvider(m, "Hanzo")
		}
		g := VisibleCatalog(tagged, snap, org, isAdmin)
		data["hanzoModels"] = g
		for _, m := range g {
			visible[modelID(m)] = true
		}
	}
	if pm, ok := data["providers"].(map[string]any); ok {
		data["providers"] = VisibleProviders(pm, snap, org, isAdmin)
	}

	// Admins see every id reference; customers see only references to models that
	// survived the gate above.
	if isAdmin {
		return
	}
	if fm, ok := data["freeModels"].([]any); ok {
		data["freeModels"] = filterIDList(fm, visible)
	}
	if fams, ok := data["families"].([]any); ok {
		for _, f := range fams {
			fmap, ok := f.(map[string]any)
			if !ok {
				continue
			}
			if ms, ok := fmap["models"].([]any); ok {
				fmap["models"] = filterIDList(ms, visible)
			}
		}
	}
}

// modelArray coerces a decoded JSON array of objects into []Model.
func modelArray(v any) ([]Model, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]Model, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, Model(m))
		}
	}
	return out, true
}

// withProvider returns a copy of m with provider set when absent/empty.
func withProvider(m Model, provider string) Model {
	t := make(Model, len(m)+1)
	for k, v := range m {
		t[k] = v
	}
	if p, _ := t["provider"].(string); p == "" {
		t["provider"] = provider
	}
	return t
}

// filterIDList keeps only the string entries present in visible; non-string
// entries pass through untouched.
func filterIDList(list []any, visible map[string]bool) []any {
	out := make([]any, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			if visible[s] {
				out = append(out, e)
			}
			continue
		}
		out = append(out, e)
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
// {enabled, beta, betaOrgs, overrides}. ONE file holds every entry; reads load a
// full snapshot once per request and the gate then runs purely in memory. mu makes
// the read-modify-write of a single row (admin set / user opt-in) atomic across
// concurrent requests — MaxOpenConns(1) serializes the statements, but a Get-then-
// Upsert from two requests could otherwise interleave and lose an opt-in.
type catalog struct {
	db *sql.DB
	mu sync.Mutex
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
	// Additive migration: the `beta` eligibility flag (the off-vs-beta
	// disambiguation the tri-state + self-opt-in need). Guard on the column's
	// presence so it runs exactly once. Backfill beta=1 for any row that ALREADY
	// carries a beta-org list, so a pre-existing private beta (set via the old
	// enabled+betaOrgs PATCH) KEEPS working after the upgrade — no regression.
	if !c.hasColumn("catalog_overlay", "beta") {
		if _, err := c.db.Exec(`ALTER TABLE catalog_overlay ADD COLUMN beta INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate beta col: %w", err)
		}
		if _, err := c.db.Exec(`UPDATE catalog_overlay SET beta=1 WHERE enabled=0 AND beta_orgs != '[]' AND beta_orgs != ''`); err != nil {
			return fmt.Errorf("migrate beta backfill: %w", err)
		}
	}
	return nil
}

// hasColumn reports whether `table` already has column `col` (SQLite
// PRAGMA table_info), so the additive migration is idempotent.
func (c *catalog) hasColumn(table, col string) bool {
	rows, err := c.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err == nil && name == col {
			return true
		}
	}
	return false
}

func (c *catalog) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

const overlayCols = `kind,id,enabled,beta_orgs,overrides,updated_at,beta`

func scanOverlay(sc interface{ Scan(...any) error }) (Overlay, error) {
	var (
		o        Overlay
		enabled  int
		betaJSON string
		ovr      string
		beta     int
	)
	if err := sc.Scan(&o.Kind, &o.ID, &enabled, &betaJSON, &ovr, &o.UpdatedAt, &beta); err != nil {
		return Overlay{}, err
	}
	o.Enabled = enabled != 0
	o.Beta = beta != 0
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
	betaFlag := 0
	if o.Beta {
		betaFlag = 1
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO catalog_overlay (`+overlayCols+`) VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(kind,id) DO UPDATE SET
		   enabled=excluded.enabled,
		   beta_orgs=excluded.beta_orgs,
		   overrides=excluded.overrides,
		   updated_at=excluded.updated_at,
		   beta=excluded.beta`,
		o.Kind, o.ID, enabled, beta, string(o.Overrides), o.UpdatedAt, betaFlag)
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	return nil
}

// errNotBeta is returned when a self-service opt-in targets an item that is not in
// beta — a `ga` item is already visible (no opt-in needed) and an `off` item is a
// kill switch a user must NOT be able to re-open. This is the security invariant of
// the self-opt-in path: it can never bypass `off`, and it never touches global state.
var errNotBeta = errors.New("item is not in beta")

// mutate is the atomic read-modify-write of ONE overlay row (admin set / user
// opt-in), serialized by c.mu so two concurrent writers cannot lose each other's
// change. A missing row defaults to the catalog default (ga) before fn runs.
func (c *catalog) mutate(ctx context.Context, kind, id string, fn func(*Overlay) error) (Overlay, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok, err := c.Get(ctx, kind, id)
	if err != nil {
		return Overlay{}, err
	}
	if !ok {
		cur = Overlay{Kind: kind, ID: id, Enabled: true} // catalog default (ga)
	}
	cur.Kind, cur.ID = kind, id
	if err := fn(&cur); err != nil {
		return Overlay{}, err
	}
	cur.UpdatedAt = time.Now().Unix()
	if err := c.Upsert(ctx, cur); err != nil {
		return Overlay{}, err
	}
	return cur, nil
}

// OptIn adds org to a BETA item's allow-list (self-service). It refuses anything
// that is not in beta (errNotBeta), so a caller can never opt their org into an
// `off` item (bypassing the kill switch) or a `ga` item (already visible). The org
// is the SANITIZED caller org supplied by the handler — never client-controllable —
// so a user can only ever opt in THEIR OWN org.
func (c *catalog) OptIn(ctx context.Context, kind, id, org string) (Overlay, error) {
	if strings.TrimSpace(org) == "" {
		return Overlay{}, fmt.Errorf("org required")
	}
	return c.mutate(ctx, kind, id, func(o *Overlay) error {
		if o.State() != "beta" {
			return errNotBeta
		}
		if !o.optedIn(org) {
			o.BetaOrgs = append(o.BetaOrgs, org)
		}
		return nil
	})
}

// OptOut removes org from an item's beta allow-list (idempotent). Only ever affects
// the caller's own org (the handler passes the sanitized org).
func (c *catalog) OptOut(ctx context.Context, kind, id, org string) (Overlay, error) {
	if strings.TrimSpace(org) == "" {
		return Overlay{}, fmt.Errorf("org required")
	}
	return c.mutate(ctx, kind, id, func(o *Overlay) error {
		kept := make([]string, 0, len(o.BetaOrgs))
		for _, b := range o.BetaOrgs {
			if b != org {
				kept = append(kept, b)
			}
		}
		o.BetaOrgs = kept
		return nil
	})
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
