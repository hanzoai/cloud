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
func registerHooks() {
	for _, dt := range indexedDocTypes {
		framework.RegisterHook(dt, framework.ActionAfterSave, indexOnSave)
		framework.RegisterHook(dt, framework.ActionOnTrash, deindexOnTrash)
	}
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
