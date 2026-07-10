package git

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/zap-proto/zip"
)

// Mirror-in: import an EXTERNAL git repository into the embedded server so a repo
// that lives on GitHub (e.g. github.com/hanzoai/cloud) becomes a first-class
// Hanzo-hosted repo at <domain>/v1/git/<org>/<name>.git. Once mirrored, a push
// there fires git-push-to-deploy exactly like any native repo (smart_http.go),
// so cloud can host — and deploy — its OWN source without depending on GitHub.
//
// DRY: the fetch writes into the SAME billy/S3 storer the smart-HTTP server
// clones/pushes from (storage.storer), never a second git stack. Idempotent by
// mirror semantics (+refs/*:refs/*, forced): re-mirroring force-updates refs.
// Org-scoped exactly like create — the org (X-Org-Id) owns the mirror; no
// other org can read or push to it. Storage is bounded the ONE way this
// subsystem bounds storage: recordUsage meters the mirrored bytes against the
// org's commerce quota, the same number a push is bounded by.

// mirrorRefSpec is git's mirror refspec: force every source ref onto the same
// destination ref. HEAD (not under refs/) is handled separately by mirrorInto.
const mirrorRefSpec config.RefSpec = "+refs/*:refs/*"

// mirrorEnvToken names the env var — KMS-injected via a KMSSecret sync, never
// hardcoded, never logged — holding the credential for mirroring a PRIVATE
// https source. Empty means an anonymous fetch (public sources need none).
const mirrorEnvToken = "GIT_MIRROR_TOKEN"

type mirrorReq struct {
	Source  string `json:"source"`
	Project string `json:"project"`
}

// mirror imports body.Source into the org's repo at :name. It provisions the
// repo on first use (idempotent, race-safe) then force-fetches every ref, so a
// first call clones the source and a repeat call syncs it.
func (s *svc) mirror(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := s.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := normalizeName(c.Param("name"))
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	var body mirrorReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	src, err := mirrorSource(body.Source)
	if err != nil {
		return err
	}
	// Project sub-scope: explicit body value wins, else the header sub-scope —
	// identical to create so a mirror lands in the same scope a create would.
	project := strings.TrimSpace(body.Project)
	if project == "" {
		project = projectScope(c)
	} else if !projectRE.MatchString(project) {
		return zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}

	r, err := s.ensureRepo(c.Context(), store, org, project, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "ensure repo: %v", err)
	}
	if err := s.storage.mirrorInto(c.Context(), org, project, name, src); err != nil {
		return zip.Errorf(http.StatusBadGateway, "mirror fetch: %v", err)
	}
	// Meter the mirrored bytes the same way a push is metered (the ONE storage
	// bound). Best-effort — a metering miss must not fail a landed mirror.
	r.SizeBytes = s.recordUsage(context.WithoutCancel(c.Context()), org, project, name)
	branches, head := s.refState(org, project, name)
	return c.JSON(http.StatusOK, s.toView(r, branches, head))
}

// mirrorInto fetches every ref/object from srcURL into the repo's storer with
// mirror semantics and points HEAD at the source's default branch so a
// clone-back resolves a default. Idempotent: an up-to-date source is a no-op.
func (s *storage) mirrorInto(ctx context.Context, org, project, name, srcURL string) error {
	st, err := s.storer(org, project, name)
	if err != nil {
		return err
	}
	remote := gogit.NewRemote(st, &config.RemoteConfig{
		Name:  "mirror",
		URLs:  []string{srcURL},
		Fetch: []config.RefSpec{mirrorRefSpec},
	})
	auth := mirrorAuth(srcURL)

	// Discover the source default branch (HEAD symref) BEFORE fetching — the
	// refs/* refspec never carries HEAD.
	def, err := defaultBranch(remote.ListContext(ctx, &gogit.ListOptions{Auth: auth}))
	if err != nil {
		return fmt.Errorf("list source: %w", err)
	}

	switch err := remote.FetchContext(ctx, &gogit.FetchOptions{
		RefSpecs: []config.RefSpec{mirrorRefSpec},
		Auth:     auth,
		Force:    true,
		Tags:     gogit.AllTags,
	}); {
	case err == nil, errors.Is(err, gogit.NoErrAlreadyUpToDate):
		// fetched, or nothing new — both are a successful (idempotent) mirror.
	default:
		return err
	}

	if def != "" {
		if err := st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, def)); err != nil {
			return fmt.Errorf("set HEAD: %w", err)
		}
	}
	return nil
}

// defaultBranch resolves the source's default branch from an advertised ref
// list: the HEAD symref if the server sends one, else the branch whose tip
// equals HEAD's hash, else "main", else the first branch. Returns "" for a
// source with no branches (an empty repo — HEAD is then left untouched).
func defaultBranch(refs []*plumbing.Reference, listErr error) (plumbing.ReferenceName, error) {
	if listErr != nil {
		return "", listErr
	}
	byHash := map[plumbing.Hash]plumbing.ReferenceName{}
	var headHash plumbing.Hash
	var haveHead bool
	var main, first plumbing.ReferenceName
	for _, ref := range refs {
		switch {
		case ref.Name() == plumbing.HEAD:
			if ref.Type() == plumbing.SymbolicReference {
				return ref.Target(), nil // authoritative
			}
			headHash, haveHead = ref.Hash(), true
		case ref.Name().IsBranch():
			byHash[ref.Hash()] = ref.Name()
			if ref.Name() == plumbing.NewBranchReferenceName(defaultBranchName) {
				main = ref.Name()
			}
			if first == "" {
				first = ref.Name()
			}
		}
	}
	if haveHead {
		if b, ok := byHash[headHash]; ok {
			return b, nil
		}
	}
	if main != "" {
		return main, nil
	}
	return first, nil
}

// ensureRepo returns the org's repo, provisioning it (metadata row + empty
// bare storage) on first use. Idempotent and race-safe: a concurrent create is
// reconciled by reloading the canonical row, never surfaced as a conflict — a
// mirror must be repeatable.
func (s *svc) ensureRepo(ctx context.Context, store *Store, org, project, name string) (Repo, error) {
	r, err := store.Get(ctx, org, project, name)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, errNotFound) {
		return Repo{}, err
	}
	id, err := genID("repo")
	if err != nil {
		return Repo{}, err
	}
	now := time.Now().Unix()
	fresh := Repo{
		ID: id, Org: org, Project: project, Name: name,
		DefaultBranch: defaultBranchName, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.provision(ctx, store, fresh); err != nil && !errors.Is(err, errConflict) {
		return Repo{}, err
	}
	return store.Get(ctx, org, project, name)
}

// mirrorSource validates the org-supplied source URL: a well-formed http/https
// git URL with a host. This is the boundary check for the one place a
// org-supplied address enters the server's outbound network path.
func mirrorSource(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", zip.ErrBadRequest("source is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", zip.ErrBadRequest("source must be an http(s) git URL")
	}
	return u.String(), nil
}

// mirrorAuth builds the fetch credential for an https source from the
// KMS-injected env token, in GitHub's token-as-password basic-auth form. Returns
// nil for http (loopback) and when no token is set, so public mirrors stay
// anonymous and a token is never sent in cleartext.
func mirrorAuth(srcURL string) transport.AuthMethod {
	tok := strings.TrimSpace(os.Getenv(mirrorEnvToken))
	if tok == "" {
		return nil
	}
	if u, err := url.Parse(srcURL); err != nil || u.Scheme != "https" {
		return nil
	}
	return &githttp.BasicAuth{Username: "x-access-token", Password: tok}
}
