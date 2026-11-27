package projects

// workspace.go — where an org keeps its code on the forge, and the record of it.
//
// An org the estate's closed table does not name gets a namespace the platform
// makes for it on first need (forge.Provision): a coding run's first start, or
// the first template it takes. This store is where the platform writes down
// which namespace it made for which org, because it is the store every org's
// projects already live in, org-scoped the same way: a row is keyed by the
// VALIDATED org, and no request field reaches the key.
//
// A coding run needs the answer too, and runs live in the agent app, so the
// record is also published on the internal plane (projects_workspace). The org
// is the caller's plane identity there, as for every op here.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/remote"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/zap-proto/zip"
)

// workspacesDDL is the record. org is the key, so an org holds one namespace;
// owner is unique, so a namespace belongs to one org. A row is written only after
// the forge made the namespace for this org and is never rewritten.
const workspacesDDL = `
CREATE TABLE IF NOT EXISTS workspaces (
  org        TEXT PRIMARY KEY,
  owner      TEXT NOT NULL UNIQUE,
  created_at BIGINT NOT NULL
);`

// book is the record as forge.Provision and forge.Find read and write it.
type book struct{ db *sql.DB }

func (s *Store) book() book { return book{db: s.db} }

// Read answers the namespace recorded for org, "" when there is none.
func (b book) Read(ctx context.Context, org string) (string, error) {
	var owner string
	err := b.db.QueryRowContext(ctx, `SELECT owner FROM workspaces WHERE org=$1`, org).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read workspace: %w", err)
	}
	return owner, nil
}

// Write records owner as org's namespace, in one transaction: an org that
// already holds one keeps it and is answered with it, and an owner another org
// holds is forge.ErrExists.
func (b book) Write(ctx context.Context, org, owner string) (string, error) {
	if err := dialect.Text(org, owner); err != nil {
		return "", fmt.Errorf("record workspace: %w", err)
	}
	tx, err := dialect.Of(b.db).BeginWrite(ctx, b.db)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var have string
	switch err := tx.QueryRowContext(ctx, `SELECT owner FROM workspaces WHERE org=$1`, org).Scan(&have); {
	case err == nil:
		return have, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("read workspace: %w", err)
	}
	var other string
	switch err := tx.QueryRowContext(ctx, `SELECT org FROM workspaces WHERE owner=$1`, owner).Scan(&other); {
	case err == nil:
		return "", fmt.Errorf("%w: %s is recorded for another org", forge.ErrExists, owner)
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("read workspace: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces (org, owner, created_at) VALUES ($1,$2,$3)`,
		org, owner, time.Now().Unix()); err != nil {
		return "", fmt.Errorf("record workspace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return owner, nil
}

// orgs is every org the record holds a workspace for, in a stable order.
func (b book) orgs(ctx context.Context) ([]string, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT org FROM workspaces ORDER BY org`)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return nil, fmt.Errorf("list workspaces: %w", err)
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

// reclaim withdraws the run keys a process that went away left in the made
// workspaces this store records, once, when it mounts.
//
// A run gives its key back when it ends, and a process that is shut down gives
// back every key it holds (apps/coding Drain); what is left is a process that
// was killed, and the next run on the same repository sweeps that repository.
// This is the bound for a repository nobody runs on again. It is the made
// workspaces' half of apps/coding Sweep, which walks the estate's namespace:
// the record of which namespaces were made lives in this store, and every org's
// runs are served beside this process, so this is where the whole set is known.
//
// Each namespace is re-read through forge.Find, so a record naming anything the
// estate holds or keeps is skipped, and forge.Client.SweepOrg removes only keys
// the run path minted and only those older than any run can be. Best-effort,
// off-thread and one pass: a forge that is down delays nothing and removes
// nothing, and the next run on each repository is the backstop.
func reclaim(s *cloud.Service[state], ctx context.Context) {
	if s.KMS == nil {
		return
	}
	b := s.State.store.book()
	orgs, err := b.orgs(ctx)
	if err != nil {
		s.Log.Warn("run keys in made workspaces were not cleared", "err", err)
		return
	}
	if len(orgs) == 0 {
		return
	}
	m, err := machine(s, ctx)
	if err != nil {
		s.Log.Warn("run keys in made workspaces were not cleared", "err", err)
		return
	}
	cleared := 0
	for _, org := range orgs {
		if ctx.Err() != nil {
			return
		}
		ws, err := forge.Find(ctx, b, org)
		if err != nil || !ws.Made {
			s.Log.Warn("a workspace record was not cleared", "org", org, "err", err)
			continue
		}
		n, err := m.SweepOrg(ctx, ws.Owner)
		if err != nil {
			s.Log.Warn("run keys were not cleared", "org", org, "workspace", ws.Owner, "err", err)
		}
		cleared += n
	}
	if cleared > 0 {
		s.Log.Info("withdrew abandoned run keys in made workspaces", "count", cleared)
	}
}

// exposeWorkspace publishes the workspace on the internal plane. Called from Mount.
func exposeWorkspace() {
	cloud.Plane().Post("/projects/workspace", planeWorkspace,
		zip.WithOperationID(client.ProjectsWorkspace),
		zip.WithSummary("Where the caller's org keeps its code on the forge, made on first need"))
}

// planeWorkspace answers the CALLER's org's workspace, making it when the call
// asks and the org has none.
//
// The org is the caller's plane identity and never an argument —
// client.WorkspaceIn has no org field, because the org is what decides which
// namespace is answered, and a caller able to name one could take another's.
// It passes the same path check planeCreate applies, because it becomes a forge
// name.
func planeWorkspace(ctx context.Context, in *client.WorkspaceIn) (*client.Workspace, error) {
	org := cloud.Who(ctx).Org
	if org == "" || !orgPathSafe(org) {
		return nil, zip.ErrForbidden("projects workspace: org required")
	}
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, zip.ErrInternal("projects: this process does not own the project store")
	}
	ws, err := workspace(s, ctx, org, in.Make)
	if err != nil {
		return nil, err
	}
	return &client.Workspace{Owner: ws.Owner, Made: ws.Made}, nil
}

// workspace is org's workspace, made when create is set and the org has none.
// The estate's orgs answer from forge's closed table and never reach the forge
// here.
func workspace(s *cloud.Service[state], ctx context.Context, org string, create bool) (forge.Workspace, error) {
	b := s.State.store.book()
	ws, err := forge.Find(ctx, b, org)
	if err != nil {
		return forge.Workspace{}, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if ws.Owner != "" || !create {
		return ws, nil
	}
	m, err := machine(s, ctx)
	if err != nil {
		return forge.Workspace{}, zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	if ws, err = forge.Provision(ctx, m, b, org); err != nil {
		return forge.Workspace{}, zip.Errorf(http.StatusBadGateway, "could not make this organization's code workspace: %v", err)
	}
	return ws, nil
}

// copyTries bounds the numbering of a copy's repository name, for the reason
// forge.Provision bounds a namespace's.
const copyTries = 20

// An org copies at most copyRate templates per copyWindow. Each copy is a clone
// the forge makes from outside and a repository in a workspace that holds a
// bounded number of them, so taking templates is paced where it is cheap to
// say no — before anything is made.
const (
	copyRate   = 10
	copyWindow = time.Hour
)

// pace holds how many templates each org may copy in a [copyWindow], for the whole
// deployment. A copy is a clone the forge makes and a repository in a workspace that
// holds a bounded number of them, and an org's requests are served by every replica,
// so the count is the KV's (coord.Spend: a window that opens at an org's first copy
// and lasts the window) and not a replica's own, which would let an org take
// [copyRate] from each.
type pace struct {
	pods   *coord.Coord // nil is this process's default
	rate   int
	window time.Duration
}

// take reports whether org may copy another template now, and counts it when it
// may. A copy that then fails still counted: a caller retrying a failing copy
// is exactly what pacing is for, and so is a refused one.
func (p *pace) take(ctx context.Context, org string) bool {
	c := p.pods
	if c == nil {
		c = coord.Default()
	}
	left, _ := c.Spend(ctx, "projects:copy", []string{org}, []int{1}, p.rate, p.window)
	return left >= 0
}

// copied makes a project from a GALLERY template for an org whose workspace the
// platform makes: the template's source is copied into the org's own namespace
// as a new private repository, and the project builds from that copy.
//
// A fork that pointed at the template's repository would point every org at
// ONE shared repository — the first edit an agent pushed would go to the
// template itself. The copy is the org's alone to change.
//
// The copy is made by the machine and read anonymously (forge.Client.Create),
// so it can only ever hold what the world can already read — which is what a
// gallery template is. An org's own private template is not copied here, and
// neither is a live project: copying another org's repository with the
// machine's authority would step over that org's own access rules.
//
// Everything that can refuse without the forge is asked first — the name, the
// slug being free, a private project being paid for — and a copy whose project
// could not then be written is deleted, so a failed fork leaves nothing behind
// but the org's workspace, which is the org's either way.
//
// A slug the caller NAMED is taken as named: one the org holds is the 409 it
// always was. A slug derived from the template is a default, so it is numbered
// past any the org holds and any public address another project answers at —
// the copy is published at once (below), and a project whose address is
// somebody else's has nothing to show.
//
// THE COPY IS PUBLISHED AT ONCE: built from its own repository in a sandbox by
// the agent app and served at the project's own address, so what the org sees
// is its copy rather than the template's demo. The build runs after the answer;
// the project reads building until it is live, and a build that could not start
// leaves the project as it was made, to publish later.
func copied(s *cloud.Service[state], c *zip.Ctx, org string, body projectsCreate, source string, named bool) (*projectsProject, error) {
	ctx := c.Context()
	p, err := draft(s, org, body)
	if err != nil {
		return nil, err
	}
	if p.Visibility, err = resolve(s, c, body.Visibility); err != nil {
		return nil, err
	}
	if named {
		switch _, err := s.State.store.GetProject(ctx, org, p.Slug); {
		case err == nil:
			return nil, zip.ErrConflict("project slug already exists in this org")
		case !errors.Is(err, errNotFound):
			return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
		}
	} else if p.Slug, err = addressable(s, ctx, org, p.Slug); err != nil {
		return nil, err
	}
	if !s.State.copies.take(ctx, org) {
		return nil, zip.Errorf(http.StatusTooManyRequests,
			"this organization has taken %d templates in the last hour, the most it may; try again later", copyRate)
	}
	ws, err := workspace(s, ctx, org, true)
	if err != nil {
		return nil, err
	}
	if !ws.Made {
		// The machine writes only where the platform made the namespace. The
		// estate's is the forge ACL's, and a copy is never made there.
		return nil, zip.ErrForbidden("a template is copied only into a workspace the platform made")
	}
	m, err := machine(s, ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	var r forge.Repo
	for n := 1; ; n++ {
		if n > copyTries {
			return nil, zip.Errorf(http.StatusConflict, "every name for %q is taken in this organization's workspace", p.Slug)
		}
		name := p.Slug
		if n > 1 {
			name += "-" + strconv.Itoa(n)
		}
		r, err = m.Create(ctx, ws.Owner, name, p.Description, source)
		if errors.Is(err, forge.ErrExists) {
			continue
		}
		if errors.Is(err, forge.ErrFull) {
			return nil, zip.Errorf(http.StatusConflict,
				"this organization's code workspace holds as many repositories as it may, so the template was not copied")
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "could not copy the template %s into this organization's workspace: %v", source, err)
		}
		break
	}
	p.RepoURL = remote.Clean("https://" + m.Host() + "/" + ws.Owner + "/" + r.Name + ".git")
	p.RepoProvider = remote.Provider(p.RepoURL)
	p.RepoBranch = strings.TrimSpace(r.Branch)
	if p.RepoBranch == "" {
		p.RepoBranch = "main"
	}
	out, err := persist(s, ctx, p, body.Analytics)
	if err != nil {
		if derr := m.Delete(context.WithoutCancel(ctx), ws.Owner, r.Name); derr != nil {
			s.Log.Warn("a fork's copy was not taken back after its project failed", "org", org, "repo", ws.Owner+"/"+r.Name, "err", derr)
		}
		return nil, err
	}
	if err := build(c, org, out.Slug); err != nil {
		s.Log.Warn("a fork's copy was not published", "org", org, "slug", out.Slug, "err", err)
		return out, nil
	}
	if now, err := s.State.store.GetProject(ctx, org, out.Slug); err == nil {
		v := toProject(now)
		out = &v
	}
	return out, nil
}

// addressable is the first of slug, slug-2, … that org does not hold and whose
// public address no other project answers at.
func addressable(s *cloud.Service[state], ctx context.Context, org, slug string) (string, error) {
	for n := 1; ; n++ {
		name := Slug(slug, n)
		if name == "" {
			continue
		}
		switch _, err := s.State.store.GetProject(ctx, org, name); {
		case err == nil:
			continue
		case !errors.Is(err, errNotFound):
			return "", zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
		}
		free, err := s.State.store.HostFree(ctx, siteHost(org, name), org, name)
		if err != nil {
			return "", zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if free {
			return name, nil
		}
	}
}
