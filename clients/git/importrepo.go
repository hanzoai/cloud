package git

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// importrepo.go adds POST /v1/git/repos/:name/import — the "adopt as primary" verb of
// the git federation, distinct from /mirror (keep a replica). It composes the pieces
// that already exist into ONE end-to-end flow:
//
//	mirror-in (mirror.go)     → pull every ref from the upstream into native storage
//	native CI  (ci.go)        → commit .hanzo/workflows/ so it builds on Hanzo runners
//	index      (index_on_push)→ the CI commit's push-landed event folds it into /v1/code
//	mirror-out (mirror_out.go)→ register the upstream as an outbound target (optional)
//
// Model: git.hanzo.ai becomes the PRIMARY (native is canonical); the upstream becomes
// a downstream mirror a later native push force-safe-mirrors back to. /mirror stays the
// continuous-replica verb (upstream canonical, force-sync, no CI). Two directions of
// the same federation, one fetch implementation (mirrorInto) composed two ways.
//
// Org-scoped identically to every repo route (principal.Org → X-Org-Id); the source
// URL is SSRF-guarded (mirrorSource) and the outbound target is allowlisted
// (validateMirrorTarget) exactly as /mirror and /mirrors are.

type importReq struct {
	Source  string `json:"source"`  // upstream https git URL to import from (required)
	Project string `json:"project"` // project sub-scope (defaults to the header scope)
	Mirror  string `json:"mirror"`  // outbound target to register (optional); "" ⇒ none
	CI      *bool  `json:"ci"`      // inject native CI on import (default true)
}

// importView is the import result: the repo view plus what the import did — the
// native workflow files written and the outbound mirror host registered (if any).
type importView struct {
	repoView
	Imported  bool     `json:"imported"`
	Workflows []string `json:"workflows"`        // .hanzo/workflows files committed (nil ⇒ already present)
	Mirror    string   `json:"mirror,omitempty"` // outbound mirror host registered (empty ⇒ none)
}

// handleImport handles POST /v1/git/repos/:name/import.
func handleImport(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := normalizeName(c.Param("name"))
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	var body importReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	// Project sub-scope: explicit body value wins, else the header scope — identical
	// to create/mirror so an import lands in the same scope a create would.
	project := strings.TrimSpace(body.Project)
	if project == "" {
		project = projectScope(c)
	} else if !projectRE.MatchString(project) {
		return zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	view, err := coreImport(s, c.Context(), org, project, name, body)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, view)
}

// coreImport is the transport-agnostic import: ensure the repo, mirror-in the
// upstream, inject native CI (which indexes the repo via its push event), register
// the optional outbound mirror, and return the honest summary. Reuses mirrorInto,
// ensureNativeCI, ensureMirrorTarget, and recordUsage — no new fetch/commit/mirror
// path. Errors are zip-typed so the transport maps them; a CI/mirror side-effect
// failure is logged, never fatal (the code has already landed).
func coreImport(s *cloud.Service[state], ctx context.Context, org, project, name string, in importReq) (importView, error) {
	store, err := storeFor(s, org)
	if err != nil {
		return importView{}, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	// Harden the upstream URL (https, no userinfo, SSRF-guarded) — the same gate /mirror uses.
	src, err := mirrorSource(in.Source)
	if err != nil {
		return importView{}, err
	}
	// Validate the outbound target up front (fail fast, before any fetch), if given.
	var mirrorHost string
	if strings.TrimSpace(in.Mirror) != "" {
		_, host, verr := validateMirrorTarget(in.Mirror)
		if verr != nil {
			return importView{}, verr
		}
		mirrorHost = host
	}

	if _, err := ensureRepo(s, ctx, store, org, project, name); err != nil {
		return importView{}, zip.Errorf(http.StatusInternalServerError, "ensure repo: %v", err)
	}

	// Fetch every branch from the upstream FAST-FORWARD-ONLY (importFetch, the same
	// native-canonical primitive the GitHub-App import uses) — NOT a force mirror. On
	// a first import (empty native) every branch lands; on a re-import of a repo
	// already adopted as primary a diverged native branch is a Conflict, native
	// PRESERVED, so the injected CI commit is never clobbered (idempotent re-import).
	// This is what makes "adopt as primary" different from /mirror's force replica.
	outcomes, err := s.State.storage.importFetch(ctx, org, project, name, src, gitCred{})
	if err != nil {
		return importView{}, zip.Errorf(http.StatusBadGateway, "import fetch: %v", err)
	}
	now := time.Now().Unix()
	for branch, oc := range outcomes {
		if oc.Conflict {
			_ = store.RecordConflict(ctx, org, project, name, branch, oc.Detail, now)
		} else {
			_ = store.ClearConflict(ctx, org, project, name, branch)
		}
	}

	// Native CI on import: sync .github/workflows → .hanzo/workflows (or a default),
	// committed via corePush — whose push-landed event also folds the repo into the
	// code index. Best-effort: the import is not failed by a CI-injection error.
	ci := in.CI == nil || *in.CI
	var written []string
	if ci {
		w, cerr := ensureNativeCI(s, ctx, org, project, name)
		if cerr != nil {
			s.Log.Warn("git import: native CI injection failed (continuing)", "org", org, "repo", name, "err", cerr)
		}
		written = w
	}
	// No CI commit fired (ci disabled, or every workflow already present): drive the
	// index directly so an import is ALWAYS searchable, never dependent on the CI push.
	if len(written) == 0 {
		indexImported(s, context.WithoutCancel(ctx), org, project, name)
	}

	// Register the outbound mirror so a later native push force-safe-mirrors back out.
	if mirrorHost != "" {
		if merr := ensureMirrorTarget(ctx, store, org, project, name, in.Mirror); merr != nil {
			s.Log.Warn("git import: register outbound mirror (continuing)", "org", org, "repo", name, "err", merr)
			mirrorHost = "" // report honestly: not registered
		}
	}

	// Meter storage like every other repo op, then read a fresh row + ref state.
	recordUsage(s, context.WithoutCancel(ctx), org, project, name)
	r, gerr := store.Get(ctx, org, project, name)
	if gerr != nil {
		return importView{}, zip.Errorf(http.StatusInternalServerError, "read repo: %v", gerr)
	}
	branches, head := refState(s, org, project, name)
	return importView{
		repoView:  toView(s, r, branches, head),
		Imported:  true,
		Workflows: written,
		Mirror:    mirrorHost,
	}, nil
}
