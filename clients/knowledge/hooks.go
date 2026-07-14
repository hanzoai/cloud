package knowledge

import (
	"context"

	"github.com/hanzoai/cloud/clients/framework"
)

// hooks.go is the seam between the framework DocType lifecycle and the ONE vector
// index (index.go). For every KNOWLEDGE doctype (kb-page, kb-memory, kb-source) it
// registers:
//
//   - after_save → indexDoc: on create AND update, (re)embed the document and
//     upsert its point into the org's collection. after_save runs POST-write and is
//     non-fatal by the framework's contract (a returned error is logged, the write
//     already landed), so a vector/embedding outage NEVER blocks a knowledge write.
//
//   - on_trash → deindexDoc: before delete, remove the document's point. on_trash
//     is a GATE (a returned error would abort the delete), so this hook SWALLOWS the
//     index error and returns nil — a vector outage must never wedge a delete.
//
// kb-connector is intentionally NOT indexed: it is connection metadata, never
// knowledge text. This is the whole extension surface — kb adds no second hook path.
//
// kb-page ALSO maintains wikilink EDGES (links.go): its save reconciles "[[…]]"
// references into kb-link edges, and its trash removes its outgoing edges. Because
// runHooks stops at the first erroring hook, indexing and edge maintenance are
// combined into ONE page hook that runs both INDEPENDENTLY — a vector outage that
// fails indexing must never skip edge extraction, and vice-versa.
func registerHooks() {
	for _, dt := range indexedDocTypes {
		if dt == DTPage {
			continue // kb-page uses the combined index+link hooks below
		}
		framework.RegisterHook(dt, framework.ActionAfterSave, indexOnSave)
		framework.RegisterHook(dt, framework.ActionOnTrash, deindexOnTrash)
	}
	framework.RegisterHook(DTPage, framework.ActionAfterSave, pageAfterSave)
	framework.RegisterHook(DTPage, framework.ActionOnTrash, pageOnTrash)
}

// pageAfterSave runs BOTH the vector index and the wikilink-edge reconciliation for
// a saved page, isolating a failure of one from the other. Indexing is fail-open
// (its error is logged here, not propagated) so link extraction always runs; the
// link error is returned for the framework's after() wrapper to log.
func pageAfterSave(ctx context.Context, ev *framework.Event) error {
	if err := indexOnSave(ctx, ev); err != nil {
		ev.Logger.Warn("kb index on save failed (page write already landed)",
			"name", ev.Doc.Name, "err", err)
	}
	return linkOnSave(ctx, ev)
}

// pageOnTrash removes BOTH the page's vector point and its outgoing edges. Both
// halves swallow their own errors (a trash hook must never block the delete), so
// this always returns nil.
func pageOnTrash(ctx context.Context, ev *framework.Event) error {
	_ = deindexOnTrash(ctx, ev) // swallows internally
	_ = delinkOnTrash(ctx, ev)  // swallows internally
	return nil
}

// indexOnSave embeds the just-saved document and upserts it to the org's vector
// namespace. ev.Org is the VALIDATED tenant (never a client header); the collection
// and the point payload are both keyed by it, so knowledge is indexed strictly
// in-tenant. A non-nil return is logged by the framework's after() wrapper — the
// document is already persisted, so indexing is best-effort by design.
func indexOnSave(ctx context.Context, ev *framework.Event) error {
	title := str(ev.Doc.Data["title"])
	return index().indexDoc(ctx, ev.Org, ev.DocType, ev.Doc.Name, title, ev.Doc.Data)
}

// deindexOnTrash removes the document's point before it is deleted. It is a gate
// hook, so it must not abort the delete on an index failure: the error is logged
// via the event logger and nil is returned. A best-effort orphaned vector is
// harmless (search re-checks payload.org and the doc no longer exists to open),
// whereas blocking the delete would be a real availability bug.
func deindexOnTrash(ctx context.Context, ev *framework.Event) error {
	if err := index().deindexDoc(ctx, ev.Org, ev.DocType, ev.Doc.Name); err != nil {
		ev.Logger.Warn("kb deindex on trash failed (delete proceeds)",
			"doctype", ev.DocType, "name", ev.Doc.Name, "err", err)
	}
	return nil
}
