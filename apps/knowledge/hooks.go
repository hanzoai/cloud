package knowledge

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/client"
	knowledgepeer "github.com/hanzoai/cloud/client/knowledge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
)

// hooks.go is the client between the framework DocType lifecycle and the ONE vector
// index (index.go). For every KNOWLEDGE doctype (kb.page, kb.memory, kb.source) it
// registers:
//
//   - after_save → sync: on create AND update, read the document back and make
//     its lexical row and passages match it. after_save runs POST-write and is
//     non-fatal by the framework's contract (a returned error is logged, the write
//     already landed), so an ai plane outage NEVER blocks a knowledge write.
//
//   - on_trash → sync, gone: before delete, remove the document's row and
//     passages. on_trash is a GATE (a returned error would abort the delete), so
//     this hook SWALLOWS the index error and returns nil — an index failure must
//     never wedge a delete.
//
// Both run where the framework runs, and sync runs where the org's passage store
// is (syncDoc): in this process when it mounted knowledge, else in the knowledge
// peer's. plugin/framework is the process that mounts no knowledge, and every
// save, trash, connector sync and import runs its hooks there.
//
// kb.connector is intentionally NOT indexed: it is connection metadata, never
// knowledge text. This is the whole extension surface — kb adds no second hook path.
//
// kb.page ALSO maintains wikilink EDGES (links.go): its save reconciles "[[…]]"
// references into kb.link edges, and its trash removes its outgoing edges. Because
// runHooks stops at the first erroring hook, indexing and edge maintenance are
// combined into ONE page hook that runs both INDEPENDENTLY — an embedding outage
// that fails indexing must never skip edge extraction, and vice-versa.
func registerHooks() {
	for _, dt := range indexedDocTypes {
		if dt == DTPage {
			continue // kb.page uses the combined index+link hooks below
		}
		framework.RegisterHook(dt, framework.ActionAfterSave, indexOnSave)
		framework.RegisterHook(dt, framework.ActionOnTrash, deindexOnTrash)
	}
	framework.RegisterHook(DTPage, framework.ActionAfterSave, pageAfterSave)
	framework.RegisterHook(DTPage, framework.ActionOnTrash, pageOnTrash)
	for _, dt := range []framework.ID{DTMemory, DTSource} {
		framework.RegisterHook(dt, framework.ActionBeforeSave, ownerOnSave)
	}
}

// ownerOnSave is the gate on a document's owner: a caller may claim a document
// for themself or leave it the org's, never hand it to another member, and an
// owner once set does not move. It is a before_save hook, so a refusal is the
// write not happening.
func ownerOnSave(ctx context.Context, ev *framework.Event) error {
	want := str(ev.Doc.Data["owner"])
	if ev.Prev != nil {
		if had := str(ev.Prev.Data["owner"]); had != want {
			return errOwnerFixed
		}
		return nil
	}
	if want != "" && want != subject(ctx) {
		return errOwnerNotYou
	}
	return nil
}

var (
	errOwnerNotYou = errors.New("kb: owner may only be yourself")
	errOwnerFixed  = errors.New("kb: owner does not change once set")
)

// subject is the caller as a document one principal owns is keyed on — the
// boundary's attested principal, through principal.SubjectOf — or "" for a call
// with no person behind it, which owns nothing and searches only what the org
// shares. A caller whose credential names no subject no two people share is ""
// too: it claims nothing and reaches only the org's.
//
// Not the token's `sub` (Principal.Subject). A member's API key and an
// application named after that member present one Subject, `<owner>/<name>`, so
// each would own and find the other's documents; their Users differ. A person's
// User is their `sub`, so what a person already owns is still theirs.
func subject(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	p, ok := principal.Minted(c)
	if !ok {
		return ""
	}
	id, _ := principal.SubjectOf(p)
	return id
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

// pageOnTrash removes BOTH the page's passages and its outgoing edges. Both
// halves swallow their own errors (a trash hook must never block the delete), so
// this always returns nil.
func pageOnTrash(ctx context.Context, ev *framework.Event) error {
	_ = deindexOnTrash(ctx, ev) // swallows internally
	_ = delinkOnTrash(ctx, ev)  // swallows internally
	return nil
}

// indexOnSave syncs the just-saved document. ev.Org is the VALIDATED tenant
// (never a client header) and it names the org whose legs are written, so
// knowledge is indexed strictly in-tenant. A non-nil return is logged by the
// framework's after() wrapper — the document is already persisted, so indexing
// is best-effort by design.
func indexOnSave(ctx context.Context, ev *framework.Event) error {
	return syncDoc(ctx, ev.Org, ev.DocType, ev.Doc.Name, false)
}

// deindexOnTrash removes the document's row and passages before it is deleted.
// It is a gate hook, so it must not abort the delete on an index failure: the
// error is logged via the event logger and nil is returned. An orphaned row or
// passage names a document that no longer opens, which no search answers
// (present), and the next reindex removes it; blocking the delete would be a
// real availability bug.
func deindexOnTrash(ctx context.Context, ev *framework.Event) error {
	if err := syncDoc(ctx, ev.Org, ev.DocType, ev.Doc.Name, true); err != nil {
		ev.Logger.Warn("kb deindex on trash failed (delete proceeds)",
			"doctype", ev.DocType, "name", ev.Doc.Name, "err", err)
	}
	return nil
}

// syncDoc runs sync where the org's passage store is: in this process when it
// mounted knowledge, else in the knowledge peer's, which reads the document back
// from the framework itself. A deployment with no knowledge app has no legs to
// keep, and that is not an error.
func syncDoc(ctx context.Context, org string, dt framework.ID, name string, gone bool) error {
	if x := idx; x != nil {
		return x.sync(ctx, org, dt, name, gone)
	}
	_, err := knowledgepeer.KnowledgeIndex(cloud.For(ctx, org),
		&client.KnowledgeIndexIn{DocType: dt.String(), Name: name, Gone: gone})
	if errors.Is(err, plane.ErrNoPeer) {
		return nil
	}
	return err
}
