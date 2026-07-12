package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// core.go is the transport-agnostic business logic for git's control plane —
// the ONE implementation each of create / list / get / delete / usage. The REST
// handlers (git.go) and the ZAP procedures (zap.go) are both THIN ADAPTERS over
// these funcs: a transport resolves the tenant + decodes its own wire shape,
// calls the core, and maps the returned error to its own status vocabulary.
//
// This is the "one and only one way" rule made concrete: one core func, two
// transports. The business rules (name validation, project sub-scope, conflict
// vs not-found, storage provisioning, usage measurement) live here once and can
// never drift between the HTTP and ZAP paths.
//
// Errors are typed sentinels so each transport maps them independently:
//   - errBadInput  → 400 (HTTP) / dispatch error (ZAP)
//   - errConflict  → 409
//   - errNotFound  → 404
// A wrapped errBadInput carries a human message for the client.

// errBadInput marks a caller-supplied validation failure (bad name, oversized
// description, malformed project). Transports answer 400. errConflict /
// errNotFound are defined in store.go and reused verbatim.
var errBadInput = errors.New("git: invalid input")

// badInput wraps a message as an errBadInput so a transport can surface the
// reason while still matching errors.Is(err, errBadInput).
func badInput(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errBadInput, fmt.Sprintf(format, a...))
}

// coreCreate provisions a repo for (org, project) and returns its view. org is
// the validated tenant; project defaults to the caller's sub-scope. It is the
// ONE create path — REST POST /v1/git/repos and the ZAP createRepo procedure
// both call it. Maps to errBadInput / errConflict for the transport.
func (s *svc) coreCreate(ctx context.Context, org, headerProject string, in createReq) (repoView, error) {
	store, err := s.storeFor(org)
	if err != nil {
		return repoView{}, fmt.Errorf("open store: %w", err)
	}
	name := normalizeName(in.Name)
	if name == "" {
		return repoView{}, badInput("name is required")
	}
	if !nameRE.MatchString(name) {
		return repoView{}, badInput("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	// Project sub-scope: an explicit body value wins, else the transport's
	// header/context sub-scope.
	project := strings.TrimSpace(in.Project)
	if project == "" {
		project = headerProject
	} else if !projectRE.MatchString(project) {
		return repoView{}, badInput("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	if len(in.Description) > 4096 {
		return repoView{}, badInput("description too large (max 4KiB)")
	}

	id, err := genID("repo")
	if err != nil {
		return repoView{}, fmt.Errorf("rng: %w", err)
	}
	now := time.Now().Unix()
	r := Repo{
		ID: id, Org: org, Project: project, Name: name,
		Description: strings.TrimSpace(in.Description), DefaultBranch: defaultBranchName,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.provision(ctx, store, r); err != nil {
		if errors.Is(err, errConflict) {
			return repoView{}, errConflict
		}
		return repoView{}, fmt.Errorf("provision: %w", err)
	}
	r.SizeBytes = s.recordUsage(ctx, org, project, name)
	return s.toView(r, nil, ""), nil
}

// coreList returns the repos for (org, project), most-recently-updated first.
func (s *svc) coreList(ctx context.Context, org, project string) ([]repoView, error) {
	store, err := s.storeFor(org)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	rows, err := store.List(ctx, org, project)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	out := make([]repoView, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toView(r, nil, ""))
	}
	return out, nil
}

// coreGet returns one repo's detail view (branches + resolved HEAD), or
// errNotFound.
func (s *svc) coreGet(ctx context.Context, org, project, name string) (repoView, error) {
	store, err := s.storeFor(org)
	if err != nil {
		return repoView{}, fmt.Errorf("open store: %w", err)
	}
	name = normalizeName(name)
	r, err := store.Get(ctx, org, project, name)
	if errors.Is(err, errNotFound) {
		return repoView{}, errNotFound
	}
	if err != nil {
		return repoView{}, fmt.Errorf("get: %w", err)
	}
	branches, head := s.refState(org, project, name)
	return s.toView(r, branches, head), nil
}

// coreDelete removes a repo + purges its storage. Returns errNotFound when no
// row went. Storage purge failure is logged, never fatal (metadata is the
// source of truth for existence).
func (s *svc) coreDelete(ctx context.Context, org, project, name string) error {
	store, err := s.storeFor(org)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	name = normalizeName(name)
	deleted, err := store.Delete(ctx, org, project, name)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if !deleted {
		return errNotFound
	}
	if err := s.storage.remove(org, project, name); err != nil {
		s.log.Warn("purge repo storage failed (continuing)", "org", org, "project", project, "repo", name, "err", err)
	}
	return nil
}

// coreUsage returns the org-wide per-repo + total storage rollup.
func (s *svc) coreUsage(ctx context.Context, org string) (usageView, error) {
	store, err := s.storeFor(org)
	if err != nil {
		return usageView{}, fmt.Errorf("open store: %w", err)
	}
	rows, err := store.ListOrg(ctx, org)
	if err != nil {
		return usageView{}, fmt.Errorf("usage: %w", err)
	}
	out := usageView{Org: org, Repos: make([]usageRepo, 0, len(rows))}
	for _, r := range rows {
		out.Repos = append(out.Repos, usageRepo{Name: r.Name, Project: r.Project, SizeBytes: r.SizeBytes})
		out.TotalBytes += r.SizeBytes
	}
	return out, nil
}
