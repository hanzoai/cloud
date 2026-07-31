// detail.go — the three per-app DETAIL projections the ArgoCD SPA's application
// view calls (and 404-toasts when absent): sync windows, revision metadata, and
// the LIVE resource-tree stream. All THREE are TENANT-SCOPED exactly like
// dashApp/dashResourceTree — resolveScope + findNamespace decide visibility, so a
// normal org reads only its OWN apps' detail (a cross-tenant name is a clean 404,
// no existence oracle), a SuperAdmin reads the whole fleet, and an unvalidated
// caller fails closed. There is ONE scoping path (scope.go); nothing here forks it.
package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── sync windows ─────────────────────────────────────────────────────────────

// argoSyncWindows is v1alpha1 ApplicationSyncWindowState (models.ts
// ApplicationSyncWindowState). This platform runs no sync windows, so the
// projection is the permissive empty — nothing blocks a sync (canSync true, no
// active/assigned windows). Nil slices marshal to `null`, the exact shape the SPA
// reads.
type argoSyncWindows struct {
	ActiveWindows   []any `json:"activeWindows"`
	AssignedWindows []any `json:"assignedWindows"`
	CanSync         bool  `json:"canSync"`
}

// syncWindows reports whether one application may be synced right now.
// This plane declares no blackout windows, so the answer is always yes — but the
// app must exist AND belong to the caller, or the read is a 404.
//
// Example: {"name": "cloud"}
func (o ops) syncWindows(ctx context.Context, in *appRef) (*argoSyncWindows, error) {
	s := o.s
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	// Existence + ownership check (discard the namespace): 404 a name that is not the
	// caller's, so a tenant cannot probe whether another org runs an app of a given name.
	if _, err := sc.findNamespace(s, ctx, name); err != nil {
		return nil, err
	}
	return &argoSyncWindows{CanSync: true}, nil
}

// ── revision metadata ────────────────────────────────────────────────────────

// argoRevisionMetadata is v1alpha1 RevisionMetadata (models.ts RevisionMetadata) —
// date is required (models.Time); author/tags/message/signatureInfo are optional.
type argoRevisionMetadata struct {
	Author        string   `json:"author,omitempty"`
	Date          string   `json:"date"`
	Tags          []string `json:"tags,omitempty"`
	Message       string   `json:"message,omitempty"`
	SignatureInfo string   `json:"signatureInfo,omitempty"`
}

// maxRevisionLen bounds the reflected revision so an over-long path segment cannot
// bloat the response (the value is otherwise inert — JSON-escaped, never a shell/path arg).
const maxRevisionLen = 256

// revisionMetadata describes one revision of an application as far as the CR knows.
//
// The App CR is IMAGE-based: the deploy is pinned to an image tag, not a git commit, and
// the projection's git source (git.hanzo.ai/hanzoai/universe) is the display-only manifest
// repo, NOT the app's own source — so there is no in-process commit to resolve a revision
// author/date/message from (clients/git exposes CloneURL + VerifyRef only, neither of which
// yields commit metadata for an arbitrary revision). Rather than 404 (the toast) or
// fabricate a git author, this returns an HONEST minimal RevisionMetadata: message = the
// revision (HEAD resolves to the CR's declared image tag), date = when the app was declared
// (the CR creation time), author = "" (none). Real git enrichment is a follow-on gated on
// the CR carrying a real git source + a clients/git CommitMetadata export.
//
// Example: {"name": "cloud", "revision": "HEAD"}
func (o ops) revisionMetadata(ctx context.Context, in *revisionRef) (*argoRevisionMetadata, error) {
	s := o.s
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	ns, err := sc.findNamespace(s, ctx, name)
	if err != nil {
		return nil, err
	}
	cr, _, err := getAppCR(s, ctx, ns, name)
	if err != nil {
		return nil, k8sErr(s, "get", err)
	}
	meta := revisionMetadataOf(cr, in.Revision)
	return &meta, nil
}

// revisionMetadataOf builds the honest minimal RevisionMetadata for an image-based App CR.
func revisionMetadataOf(cr *unstructured.Unstructured, revision string) argoRevisionMetadata {
	if len(revision) > maxRevisionLen {
		revision = revision[:maxRevisionLen]
	}
	message := revision
	// HEAD (the SPA's default when no revision is pinned) resolves to the CR's declared tag.
	if message == "" || message == "HEAD" {
		if tag, _, _ := unstructured.NestedString(cr.Object, "spec", "image", "tag"); tag != "" {
			message = tag
		}
	}
	return argoRevisionMetadata{
		Date:    cr.GetCreationTimestamp().UTC().Format(time.RFC3339),
		Message: message,
	}
}

// ── live resource-tree stream ────────────────────────────────────────────────

// treeResult is the `{"result": …}` envelope the SPA unwraps for the resource-tree
// stream (watchResourceTree: JSON.parse(data).result) — the SAME envelope shape the
// applications stream uses.
type treeResult struct {
	Result argoTree `json:"result"`
}

// dashStreamResourceTree is GET /v1/deploy/stream/applications/:name/resource-tree — the
// LIVE ApplicationTree as SSE. TENANT-SCOPED: resolveScope + findNamespace run BEFORE any
// emission (a cross-tenant name 404s with no SSE opened, an unvalidated caller 403s), then
// the tree is emitted once and refreshed on the keep-alive interval, honoring ctx cancel.
func dashStreamResourceTree(s *cloud.Service[state], c *zip.Ctx) error {
	// Scope gate FIRST — nothing is emitted (SendStreamWriter is never reached) unless the
	// caller is authorized for this specific app.
	sc, ok := resolveScope(c)
	if !ok {
		return refuse(c)
	}
	if err := ready(s); err != nil {
		return err
	}
	name := reqName(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("name must be a DNS-1123 label")
	}
	ns, err := sc.findNamespace(s, c.Context(), name)
	if err != nil {
		return err // cross-tenant / unknown name → 404 BEFORE any stream is opened
	}
	// Capture the context BEFORE SendStreamWriter (its callback runs after this handler
	// returns and must not touch c). c.Context() does NOT cancel on client disconnect; a
	// failing flush inside the loop is the disconnect signal — mirrors dashStreamApps.
	ctx := c.Context()
	setStreamHeaders(c)
	return c.SendStreamWriter(func(w *bufio.Writer) {
		streamResourceTree(s, ctx, ns, name, w)
	})
}

// streamResourceTree emits the app's ApplicationTree once, then re-emits it on the keep-alive
// interval (a cheap poll that doubles as the keep-alive — no multi-resource watch to leak),
// until the client disconnects (a failing write) or the context is canceled. Separated from
// the handler so it is unit-testable over a bytes.Buffer + a cancelable context.
func streamResourceTree(s *cloud.Service[state], ctx context.Context, ns, name string, w *bufio.Writer) {
	if !emitResourceTree(s, ctx, ns, name, w) {
		return // client gone during the initial emit
	}
	ticker := time.NewTicker(streamKeepalive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !emitResourceTree(s, ctx, ns, name, w) {
				return
			}
		}
	}
}

// emitResourceTree writes one ApplicationTree SSE frame (rebuilt from the live CR). If the
// CR read fails (app deleted / transient), it holds the connection open with a keep-alive
// comment rather than killing the stream. Returns false only when a write fails (client gone).
func emitResourceTree(s *cloud.Service[state], ctx context.Context, ns, name string, w *bufio.Writer) bool {
	cr, _, err := getAppCR(s, ctx, ns, name)
	if err != nil {
		return writeKeepalive(w)
	}
	return writeTreeEvent(w, projectTree(buildTree(s, ctx, ns, name, cr)))
}

// writeTreeEvent writes one SSE frame — `data: {"result": <ApplicationTree>}` — and flushes.
// Returns false when the write/flush fails (client disconnected).
func writeTreeEvent(w *bufio.Writer, tree argoTree) bool {
	payload, err := json.Marshal(treeResult{Result: tree})
	if err != nil {
		return true // unreachable for this shape; skip the frame rather than kill the stream
	}
	if _, err := w.WriteString("data: "); err != nil {
		return false
	}
	if _, err := w.Write(payload); err != nil {
		return false
	}
	if _, err := w.WriteString("\n\n"); err != nil {
		return false
	}
	return w.Flush() == nil
}

// writeKeepalive writes one SSE keep-alive comment and flushes; false when the client is gone.
func writeKeepalive(w *bufio.Writer) bool {
	if _, err := w.WriteString(": keep-alive\n\n"); err != nil {
		return false
	}
	return w.Flush() == nil
}
