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

	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── sync windows ─────────────────────────────────────────────────────────────

// argoSyncWindows is v1alpha1 ApplicationSyncWindowState (models.ts
// ApplicationSyncWindowState). This platform runs no sync windows, so the
// projection is the permissive empty — nothing blocks a sync (canSync true, no
// active/assigned windows). Nil slices marshal to `null`, the exact shape the SPA
// reads.
type argoSyncWindows struct {
	// ActiveWindows are the sync windows in force right now. Always null: this
	// platform declares none, so nothing is ever in force.
	ActiveWindows []any `json:"activeWindows"`
	// AssignedWindows are the windows configured for this application at all,
	// whether or not currently in force. Always null, for the same reason.
	AssignedWindows []any `json:"assignedWindows"`
	// CanSync is whether a sync would be permitted at this moment. Always true —
	// with no windows there is nothing to deny it. A caller must not read this as
	// "a sync will succeed"; it only means no window is blocking one.
	CanSync bool `json:"canSync"`
}

// GetDeploySyncWindows returns one application's argocd
// ApplicationSyncWindowState — the answer to "is anything blocking a sync of this
// application right now?".
//
// This platform runs NO sync windows, so the answer is always the permissive
// empty one: canSync true, with no active and no assigned windows. The
// application is still resolved first, so a name that is not the caller's is not
// found rather than handed the static body — the endpoint discloses nothing about
// another tenant's fleet.
func (o ops) syncWindows(ctx context.Context, in *appRef) (*argoSyncWindows, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	// Existence + ownership check: 404 a name that is not the caller's, so a tenant
	// cannot probe whether another org runs an app of a given name.
	if _, err := o.resolve(ctx, sc, name); err != nil {
		return nil, err
	}
	return &argoSyncWindows{CanSync: true}, nil
}

// ── revision metadata ────────────────────────────────────────────────────────

// argoRevisionMetadata is v1alpha1 RevisionMetadata (models.ts RevisionMetadata) —
// date is required (models.Time); author/tags/message/signatureInfo are optional.
type argoRevisionMetadata struct {
	// Author is the commit author. Always absent: an App CR pins an IMAGE, so this
	// process has no commit to read one from and will not invent one.
	Author string `json:"author,omitempty"`
	// Date is when the App CR was created, RFC 3339 UTC — the only real timestamp
	// there is here. It is NOT the date of the revision asked for.
	Date string `json:"date"`
	// Tags are the git tags pointing at the revision. Always absent, for the same
	// reason as Author.
	Tags []string `json:"tags,omitempty"`
	// Message is the revision asked for, echoed back — not a commit message. The
	// empty revision and "HEAD" resolve to the image tag the CR declares
	// (spec.image.tag), and anything longer than 256 characters is truncated to it.
	Message string `json:"message,omitempty"`
	// SignatureInfo is the GPG verification result for the revision. Always absent:
	// nothing here verifies a signature, and an empty field says so rather than
	// implying an unsigned commit.
	SignatureInfo string `json:"signatureInfo,omitempty"`
}

// maxRevisionLen bounds the reflected revision so an over-long path segment cannot
// bloat the response (the value is otherwise inert — JSON-escaped, never a shell/path arg).
const maxRevisionLen = 256

// GetDeployRevisionMetadata returns the argocd RevisionMetadata for one revision
// of one application — what the detail view shows beside a revision.
//
// An App CR is IMAGE-pinned rather than commit-pinned: the deploy names an image
// tag, and the git source this projection reports is the display-only manifest
// repo, not the application's own source. Nothing in this process can read a
// commit's author or message for an arbitrary revision. So rather than 404 (which
// the SPA turns into an error toast) or invent a git author, it answers the
// HONEST minimum: date is when the App CR was created, message is the revision
// asked for — with the empty revision and "HEAD" resolving to the image tag the
// CR declares — and author is empty. An over-long revision is truncated before it
// is echoed back.
//
// Tenant-scoped exactly like the application read.
func (o ops) revisionMetadata(ctx context.Context, in *revisionRef) (*argoRevisionMetadata, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	t, err := o.resolve(ctx, sc, name)
	if err != nil {
		return nil, err
	}
	if t.site != nil {
		meta := siteRevisionMetadata(t.site, in.Revision)
		return &meta, nil
	}
	if t.cd != nil {
		meta := revisionMetadataOfCD(t.cd, in.Revision)
		return &meta, nil
	}
	meta := revisionMetadataOf(t.cr, in.Revision)
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

// revisionMetadataOfCD is the same honest minimum for a CD Application, whose
// revision is a git commit: the empty revision and "HEAD" resolve to the commit CD
// last compared, and the date is when the Application was created.
func revisionMetadataOfCD(cd *unstructured.Unstructured, revision string) argoRevisionMetadata {
	if len(revision) > maxRevisionLen {
		revision = revision[:maxRevisionLen]
	}
	message := revision
	if message == "" || message == "HEAD" {
		if rev, _, _ := unstructured.NestedString(cd.Object, "status", "sync", "revision"); rev != "" {
			message = rev
		}
	}
	return argoRevisionMetadata{
		Date:    cd.GetCreationTimestamp().UTC().Format(time.RFC3339),
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
		return forbidden()
	}
	if err := ready(s); err != nil {
		return err
	}
	name, err := appName(c.Param("name"))
	if err != nil {
		return err
	}
	t, err := ops{s}.resolve(c.Context(), sc, name)
	if err != nil {
		return err // cross-tenant / unknown name → 404 BEFORE any stream is opened
	}
	// Capture the context BEFORE SendStreamWriter (its callback runs after this handler
	// returns and must not touch c). c.Context() does NOT cancel on client disconnect; a
	// failing flush inside the loop is the disconnect signal — mirrors dashStreamApps.
	ctx := c.Context()
	setStreamHeaders(c)
	if t.site != nil {
		return c.SendStreamWriter(func(w *bufio.Writer) {
			streamTree(ctx, w, func() (argoTree, bool) { return siteTree(), true })
		})
	}
	if t.cd != nil {
		// Re-read the resolved Application by name each tick — one Get, not a list,
		// and always the same object the gate admitted.
		ns, cdName := t.cd.GetNamespace(), t.cd.GetName()
		return c.SendStreamWriter(func(w *bufio.Writer) {
			streamTree(ctx, w, func() (argoTree, bool) {
				cd, err := s.State.dyn.Resource(k8s.CDApplications).Namespace(ns).Get(ctx, cdName, metav1.GetOptions{})
				if err != nil {
					return argoTree{}, false
				}
				return cdTree(cd), true
			})
		})
	}
	ns := t.ns
	return c.SendStreamWriter(func(w *bufio.Writer) {
		streamResourceTree(s, ctx, ns, name, w)
	})
}

// streamResourceTree emits the app's ApplicationTree once, then re-emits it on the keep-alive
// interval (a cheap poll that doubles as the keep-alive — no multi-resource watch to leak),
// until the client disconnects (a failing write) or the context is canceled. Separated from
// the handler so it is unit-testable over a bytes.Buffer + a cancelable context.
func streamResourceTree(s *cloud.Service[state], ctx context.Context, ns, name string, w *bufio.Writer) {
	streamTree(ctx, w, func() (argoTree, bool) {
		cr, _, err := getAppCR(s, ctx, ns, name)
		if err != nil {
			return argoTree{}, false
		}
		return projectTree(buildTree(s, ctx, ns, name, cr)), true
	})
}

// streamTree emits read()'s tree once, then again on every keep-alive tick, until
// the client disconnects (a failing write) or ctx is canceled. When read fails
// (app deleted / transient) it holds the connection open with a keep-alive comment
// rather than killing the stream.
func streamTree(ctx context.Context, w *bufio.Writer, read func() (argoTree, bool)) {
	emit := func() bool {
		tree, ok := read()
		if !ok {
			return writeKeepalive(w)
		}
		return writeTreeEvent(w, tree)
	}
	if !emit() {
		return // client gone during the initial emit
	}
	ticker := time.NewTicker(streamKeepalive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !emit() {
				return
			}
		}
	}
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
