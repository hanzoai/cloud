package git

// workspace.go — an org's codebases when its code lives in the workspace the
// platform made for it on the forge (forge workspace.go): every org the estate's
// closed table does not name.
//
// That workspace is where a coding run clones, pushes its branch and opens its
// pull request, where a template copy lands and where a GitHub import lands. So
// for such an org the codebase list is the workspace's repositories, read as the
// machine in the namespace the org's own record names, and New makes the
// repository there. A repository this app holds answers beside them.
//
// POST /v1/git/workspace is the one act that sets the workspace up for the
// person opening it: the namespace, made on the org's first need, and the
// person's forge account and seat (forge member.go), from their IAM subject and
// the verified address IAM holds. It is a POST, so the audit trail records who
// set up what and when.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// madeRepo makes a new repository in a made workspace; a test stands in for it.
var madeRepo = func(ctx context.Context, m *forge.Client, owner, name, description string) (forge.Repo, error) {
	return m.Create(ctx, owner, name, description, "")
}

// madeEnrol seats a person in a made workspace; a test stands in for it.
var madeEnrol = func(ctx context.Context, m *forge.Client, owner string, p forge.Person) (forge.Enrolled, error) {
	return m.Enroll(ctx, owner, p)
}

// madeEmail is the caller's address as IAM holds it; a test stands in for it.
var madeEmail = func(ctx context.Context) (*client.Email, error) { return iam.IAMEmail(ctx) }

// workspaceView is where the org's code lives on the forge, and the caller's
// account there.
type workspaceView struct {
	// Owner is the forge namespace.
	Owner string `json:"owner"`
	// Made says the platform made it for the org; false is the estate's.
	Made bool `json:"made"`
	// Login is the caller's forge account in a made workspace.
	Login string `json:"login,omitempty"`
}

// seatFailed is what opening the workspace answers when something it asks — IAM,
// the project app, the forge — failed it. Not 502: the edge in front of the API
// replaces an origin's 502 with a page of its own that carries no CORS header and
// none of the reason, so the browser that asked — Dev's Issues, seating its person
// before it reads the board — read only "Failed to fetch", and nobody could tell
// which step had failed. A 503 reaches the browser whole, reason and all.
const seatFailed = http.StatusServiceUnavailable

// openWorkspace sets the org's code workspace up for the caller and answers it:
// the namespace, made when the org has none, and the caller's forge account and
// seat there. Idempotent. The estate's namespace is answered from the closed
// table and nobody is enrolled in it. A seat is a person's: an API key or an
// application is refused, and a SuperAdmin acting in another org reads the
// workspace as it is and makes nothing.
//
// Example: {}
// Response: {"owner": "acme", "made": true, "login": "seat-ann"}
func (o ops) openWorkspace(ctx context.Context, _ *cloud.Unit) (*workspaceView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if owner, err := forge.Owner(t.org); err == nil {
		return &workspaceView{Owner: owner}, nil
	}
	if supporting(ctx, t.org) {
		ws, err := madeWorkspace(t.org, false)
		if err != nil && !errors.Is(err, plane.ErrNoPeer) {
			return nil, zip.Errorf(seatFailed, "this organization's code workspace could not be read: %v", err)
		}
		return &workspaceView{Owner: ws.Owner, Made: ws.Made}, nil
	}
	sub, err := seated(ctx)
	if err != nil {
		return nil, err
	}
	who, err := madeEmail(ctx)
	if err != nil {
		return nil, zip.Errorf(seatFailed, "your account could not be read: %v", err)
	}
	if who == nil || !who.Verified || strings.TrimSpace(who.Address) == "" {
		return nil, zip.ErrForbidden("verify your email address first: a seat in the code workspace is tied to a verified address")
	}
	ws, err := madeWorkspace(t.org, true)
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return nil, zip.Errorf(http.StatusServiceUnavailable, "this deployment keeps no code workspaces")
	case err != nil:
		return nil, zip.Errorf(seatFailed, "this organization's code workspace could not be set up: %v", err)
	case !ws.Made:
		return nil, zip.Errorf(seatFailed, "this organization's code workspace was not made")
	}
	m, err := madeSource(ctx)
	if err != nil {
		return nil, zip.Errorf(seatFailed, "the code forge is unreachable: %v", err)
	}
	got, err := madeEnrol(ctx, m.Machine(), ws.Owner, forge.Person{Subject: sub, Address: who.Address, Name: who.Name})
	switch {
	case errors.Is(err, forge.ErrAddressHeld):
		return nil, zip.ErrConflict("your seat's address belongs to another account on the code forge; contact support to free it")
	case err != nil:
		return nil, zip.Errorf(seatFailed, "your seat in this organization's code workspace could not be set up: %v", err)
	}
	if got.Made || got.Seated {
		o.s.Log.Info("code workspace seat", "org", t.org, "workspace", ws.Owner, "login", got.Login,
			"account", got.Made, "seated", got.Seated, "request", zip.CallerOf(ctx).RequestID)
	}
	return &workspaceView{Owner: ws.Owner, Made: true, Login: got.Login}, nil
}

// seated is the IAM subject a seat is bound to: the `sub` of an access token a
// person holds themselves (principal.Signed).
func seated(ctx context.Context) (string, error) {
	c, live := cloud.Request(ctx)
	if !live {
		return "", principal.RefusedFrom(ctx)
	}
	p, ok := principal.Signed(c)
	if !ok {
		return "", zip.ErrForbidden("a seat in this organization's code workspace is a person's: sign in with your own account")
	}
	return p.Subject, nil
}

// supporting reports whether the caller acts in org on another's behalf: a
// SuperAdmin in an org that is not its home, or a call made for someone else.
func supporting(ctx context.Context, org string) bool {
	who := zip.CallerOf(ctx)
	return who.ActedBy != "" || who.Admin && strings.TrimSpace(who.Owner) != org
}

// madeOf is the machine and the namespace of the caller's made workspace, made
// first when create is set; ok is false for the estate, for an org with none,
// and on a deployment that runs no project app.
func madeOf(ctx context.Context, t tenant, create bool) (m *forge.Client, owner string, ok bool, err error) {
	if _, err := forge.Owner(t.org); err == nil {
		return nil, "", false, nil
	}
	ws, err := madeWorkspace(t.org, create)
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return nil, "", false, nil
	case err != nil:
		return nil, "", false, zip.Errorf(http.StatusBadGateway, "this organization's code workspace could not be read: %v", err)
	case !ws.Made:
		return nil, "", false, nil
	}
	c, err := madeSource(ctx)
	if err != nil {
		return nil, "", false, zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	return c.Machine(), ws.Owner, true, nil
}

// madeList is the repositories of the caller's made workspace, in the list
// shape. A project sub-scope is this app's, so a scoped list reads none.
func madeList(ctx context.Context, t tenant) ([]repoView, error) {
	if t.project != "" {
		return nil, nil
	}
	m, owner, ok, err := madeOf(ctx, t, false)
	if err != nil || !ok {
		return nil, err
	}
	repos, _, err := m.Inventory(ctx, owner)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the code forge could not be read: %v", err)
	}
	out := make([]repoView, 0, len(repos))
	for _, r := range repos {
		out = append(out, forgeView(t.org, r, nil))
	}
	return out, nil
}

// madeCreate makes a repository in the caller's made workspace; ok is false when
// the org keeps its code here.
func madeCreate(s *cloud.Service[state], ctx context.Context, t tenant, in createReq) (*repoView, bool, error) {
	if _, err := forge.Owner(t.org); err == nil {
		return nil, false, nil
	}
	name := normalizeName(in.Name)
	switch {
	case name == "":
		return nil, true, zip.ErrBadRequest("name is required")
	case !nameRE.MatchString(name):
		return nil, true, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	case len(in.Description) > 4096:
		return nil, true, zip.ErrBadRequest("description too large (max 4KiB)")
	}
	ws, err := madeWorkspace(t.org, false)
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return nil, false, nil
	case err != nil:
		return nil, true, zip.Errorf(http.StatusBadGateway, "this organization's code workspace could not be read: %v", err)
	}
	if in.Public {
		return nil, true, zip.ErrBadRequest("a repository in this organization's code workspace is private")
	}
	if strings.TrimSpace(in.Project) != "" || t.project != "" {
		return nil, true, zip.ErrBadRequest("a repository in this organization's code workspace has no project scope")
	}
	if _, held := findRepo(s, ctx, t.org, name); held {
		return nil, true, zip.ErrConflict("repo name already exists in this scope")
	}
	if supporting(ctx, t.org) {
		return nil, true, zip.ErrForbidden("a repository is made in a customer's workspace by its own people, not in support mode")
	}
	if !ws.Made {
		if ws, err = madeWorkspace(t.org, true); err != nil {
			return nil, true, zip.Errorf(http.StatusBadGateway, "this organization's code workspace could not be set up: %v", err)
		}
		if !ws.Made {
			return nil, true, zip.Errorf(http.StatusBadGateway, "this organization's code workspace was not made")
		}
	}
	c, err := madeSource(ctx)
	if err != nil {
		return nil, true, zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	r, err := madeRepo(ctx, c.Machine(), ws.Owner, name, strings.TrimSpace(in.Description))
	switch {
	case errors.Is(err, forge.ErrExists):
		return nil, true, zip.ErrConflict("repo name already exists in this scope")
	case errors.Is(err, forge.ErrFull):
		return nil, true, zip.ErrConflict("this organization's code workspace holds as many repositories as it may")
	case errors.Is(err, forge.ErrBadName):
		return nil, true, zip.ErrBadRequest(strings.TrimPrefix(err.Error(), "forge: "))
	case err != nil:
		return nil, true, zip.Errorf(http.StatusBadGateway, "the repository could not be made: %v", err)
	}
	v := forgeView(t.org, r, nil)
	return &v, true, nil
}

// madeGet is one repository of the caller's made workspace with its branches;
// ok is false when the org keeps no workspace, and for a project-scoped caller,
// whose list reads none either.
func madeGet(s *cloud.Service[state], ctx context.Context, t tenant, name string) (*repoView, bool, error) {
	if t.project != "" {
		return nil, false, nil
	}
	m, owner, repo, ok, err := made(s, ctx, t, normalizeName(name))
	if err != nil || !ok {
		return nil, ok, err
	}
	r, err := m.Repo(ctx, owner, repo)
	if err != nil {
		return nil, true, madeRefusal(err, "repo not found")
	}
	branches, err := m.Branches(ctx, owner, r.Name)
	if err != nil {
		return nil, true, madeRefusal(err, "repo not found")
	}
	v := forgeView(t.org, r, branches)
	if _, _, at := place(name); at {
		v = homeView(t.org, r, branches)
	}
	return &v, true, nil
}

// madeDelete deletes one repository of the caller's made workspace, as the
// machine, since a customer's code lives there and not in this app's store; ok is
// false when the org keeps no workspace, and for a project-scoped caller, whose
// list reads none either. A name the workspace does not hold is the same 404 the
// read answers, so a second delete is not a second success. Support mode deletes
// nothing of a customer's, as it makes nothing there (madeCreate).
func madeDelete(s *cloud.Service[state], ctx context.Context, t tenant, name string) (bool, error) {
	if t.project != "" {
		return false, nil
	}
	name = normalizeName(name)
	if _, _, at := place(name); at {
		// A repository whose home is the forge is deleted there, by whoever the
		// forge lets delete it; this surface reads it and removes nothing of it.
		return false, nil
	}
	m, owner, _, ok, err := made(s, ctx, t, name)
	if err != nil || !ok {
		return ok, err
	}
	if _, err := m.Repo(ctx, owner, name); err != nil {
		return true, madeRefusal(err, "repo not found")
	}
	if supporting(ctx, t.org) {
		return true, zip.ErrForbidden("a repository in a customer's workspace is deleted by its own people, not in support mode")
	}
	if err := m.Delete(ctx, owner, name); err != nil {
		return true, zip.Errorf(http.StatusBadGateway, "the repository could not be deleted: %v", err)
	}
	return true, nil
}

// forgeView is a workspace repository in this surface's shape.
func forgeView(org string, r forge.Repo, branches []string) repoView {
	branch := strings.TrimSpace(r.Branch)
	if branch == "" {
		branch = defaultBranchName
	}
	updated := ""
	if !r.UpdatedAt.IsZero() {
		updated = r.UpdatedAt.UTC().Format(time.RFC3339)
	}
	v := repoView{
		ID: "forge:" + r.FullName, Org: org, Name: r.Name, Description: r.Description,
		DefaultBranch: branch, Public: !r.Private, Branches: branches,
		CloneURL: r.Clone, SSHURL: r.SSH, SizeBytes: r.Size * 1024, UpdatedAt: updated,
	}
	if up, ok := normUpstream(r.Origin); ok && r.Mirror {
		v.Upstream = up
	}
	return v
}

// merged is the repositories held here and the workspace's, one row per name
// (the held one first, as a browse read answers it), most recently updated first.
func merged(held, more []repoView) []repoView {
	out := append(make([]repoView, 0, len(held)+len(more)), held...)
	seen := make(map[string]bool, len(held))
	for _, r := range held {
		seen[strings.ToLower(r.Name)] = true
	}
	for _, r := range more {
		if !seen[strings.ToLower(r.Name)] {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}
