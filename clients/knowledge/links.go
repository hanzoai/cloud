package knowledge

import (
	"context"
	"path"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/clients/framework"
)

// links.go extends the kb-page after_save/on_trash path (hooks.go) with wikilink
// EDGE maintenance, alongside the vector index. When a page is saved, it parses
// "[[Page Title]]" references out of the page body (the SAME flattened text the
// indexer embeds, via lexicalText) and reconciles them into kb-link edge documents;
// when a page is trashed, it removes that page's outgoing edges. There is no
// parallel link store — an edge IS a framework document (a Link + Data reference),
// written through the SAME framework in-process API a connector sync uses.
//
// Resolution of an edge's target to a concrete page happens by VALUE at graph-read
// time (graph.go), not here, so this path never has to rewrite edges when a target
// is renamed or trashed: a dangling link is simply an edge whose target_title
// currently matches no page. The only cascade is removing a trashed page's OWN
// outgoing edges, which delinkOnTrash does.

// maxLinksPerPage bounds the wikilinks extracted from one page, and maxEdgesPerPage
// bounds the existing edges reconciled, so a pathological body cannot amplify into
// unbounded edge writes.
const (
	maxLinksPerPage = 500
	maxEdgesPerPage = 1000
)

// wikilinkRe matches a "[[target]]" reference. The target may carry an Obsidian
// alias ("[[target|shown]]"), a heading anchor ("[[target#section]]"), or a block
// ref ("[[target#^id]]"); extractWikilinks trims those to the bare target. Embeds
// ("![[target]]") match too — the leading "!" is outside the capture — so a
// transcluded note is still recorded as a link.
var wikilinkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// mediaExt is the set of attachment extensions an Obsidian embed may target; such a
// reference is not a page link, so extraction skips it to keep the graph clean.
var mediaExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true,
	".bmp": true, ".ico": true, ".pdf": true, ".mp4": true, ".mov": true, ".webm": true,
	".mp3": true, ".wav": true, ".ogg": true, ".zip": true, ".csv": true,
}

// extractWikilinks returns the ordered, de-duplicated set of page targets a body
// references via "[[…]]". Each target is stripped of its alias/heading/block-ref
// suffix and trimmed; empty targets and attachment embeds are dropped; the result
// is capped at maxLinksPerPage. It is pure (no I/O), so it is table-testable.
func extractWikilinks(text string) []string {
	matches := wikilinkRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		target := cleanTarget(m[1])
		if target == "" || seen[strings.ToLower(target)] {
			continue
		}
		if mediaExt[strings.ToLower(path.Ext(target))] {
			continue
		}
		seen[strings.ToLower(target)] = true
		out = append(out, target)
		if len(out) >= maxLinksPerPage {
			break
		}
	}
	return out
}

// cleanTarget reduces a raw wikilink inner ("target|alias", "target#heading",
// "target#^block") to the bare target title.
func cleanTarget(raw string) string {
	t := raw
	if i := strings.IndexByte(t, '|'); i >= 0 {
		t = t[:i]
	}
	if i := strings.IndexByte(t, '#'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// linkOnSave is the kb-page after_save hook that reconciles the page's outgoing
// wikilink edges. It is best-effort like the indexing hook: a returned error is
// logged by the framework after() wrapper, and the page write already landed, so
// edge maintenance never blocks a save.
func linkOnSave(ctx context.Context, ev *framework.Event) error {
	text := lexicalText(str(ev.Doc.Data["body"]))
	titles := extractWikilinks(text)
	// Drop a self-reference (a page linking to its own title/slug): it is noise in
	// the graph and never an edge worth storing.
	self := map[string]bool{
		strings.ToLower(str(ev.Doc.Data["title"])): true,
		strings.ToLower(ev.Doc.Name):               true,
	}
	desired := make([]string, 0, len(titles))
	for _, t := range titles {
		if !self[strings.ToLower(t)] {
			desired = append(desired, t)
		}
	}
	return reconcileLinks(ctx, ev.Org, ev.Doc.Name, desired)
}

// reconcileLinks makes the kb-link edges of `source` exactly match `desired`
// (target titles), creating the missing edges and deleting the stale ones through
// the framework in-process API. It writes only within `org`.
func reconcileLinks(ctx context.Context, org, source string, desired []string) error {
	existing, err := framework.Search(ctx, org, DTLink, map[string]string{"source": source}, maxEdgesPerPage)
	if err != nil {
		return err
	}
	have := make(map[string]string, len(existing)) // target_title → edge name
	for _, e := range existing {
		have[str(e.Data["target_title"])] = e.Name
	}
	want := make(map[string]bool, len(desired))
	for _, t := range desired {
		want[t] = true
	}

	var firstErr error
	remember := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	// Delete edges no longer present in the body.
	for title, name := range have {
		if !want[title] {
			remember(framework.Delete(ctx, org, DTLink, name))
		}
	}
	// Create edges newly present in the body.
	for _, title := range desired {
		if _, ok := have[title]; ok {
			continue
		}
		_, e := framework.Ingest(ctx, org, DTLink, map[string]any{
			"source":       source,
			"target_title": title,
		}, "")
		remember(e)
	}
	return firstErr
}

// delinkOnTrash is the kb-page on_trash hook that removes the trashed page's
// outgoing edges. on_trash is a GATE (a returned error aborts the delete), so an
// edge-cleanup failure is logged and nil is returned — a leftover edge is harmless
// (graph-read drops an edge whose source page no longer exists), whereas blocking
// the page delete would be a real availability bug. Edges pointing TO the trashed
// page need no cleanup: they resolve by value, so they simply become dangling.
func delinkOnTrash(ctx context.Context, ev *framework.Event) error {
	edges, err := framework.Search(ctx, ev.Org, DTLink, map[string]string{"source": ev.Doc.Name}, maxEdgesPerPage)
	if err != nil {
		ev.Logger.Warn("kb delink on trash: list edges failed (delete proceeds)", "page", ev.Doc.Name, "err", err)
		return nil
	}
	for _, e := range edges {
		if err := framework.Delete(ctx, ev.Org, DTLink, e.Name); err != nil {
			ev.Logger.Warn("kb delink on trash: delete edge failed (delete proceeds)", "edge", e.Name, "err", err)
		}
	}
	return nil
}
