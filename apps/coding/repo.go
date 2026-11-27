package coding

// repo.go — which of the org's repositories a request means.
//
// A person asks for a change to "the hero on hanzo.ai", and a run needs a
// repository. The model used to pass whatever it made of that as `repo` and the
// run took it on trust: the name was ENSURED, so a guess the forge had never
// heard of was created — an empty private repository, cloned, edited, pushed and
// proposed, while the site the person meant sat untouched.
//
// So a reference is RESOLVED against the org's own repositories, as the actor
// sees them, and a repository is never invented:
//
//	a repository by that name       that one, as the forge itself matches names
//	a repository serving that site  the one whose website is that host
//	anything else                   a refusal naming the nearest names
//
// The org's namespace is the only one a run works in (its workspace,
// workspace.go), so an `owner/` prefix is accepted when it names that namespace
// and refused, before anything is spent, when it names another.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/forge"
)

// maxCandidates bounds the names a refusal offers. A refusal is read by a model
// choosing again, and a list of every repository is a second question.
const maxCandidates = 8

// reference reads a repository reference: `name`, `owner/name`, either behind a
// scheme and a host (`https://git.hanzo.ai/hanzoai/cloud`), or a site
// (`hanzo.ai`). Every segment must be a name the git subsystem admits, which is
// what keeps a path, a `..` or a flag out of every place the name reaches.
func reference(ref string) (owner, name string, err error) {
	s := strings.TrimSpace(ref)
	for _, scheme := range []string{"https://", "http://"} {
		if len(s) > len(scheme) && strings.EqualFold(s[:len(scheme)], scheme) {
			s = s[len(scheme):]
			break
		}
	}
	s = strings.Trim(s, "/")
	if len(s) > len(".git") && strings.EqualFold(s[len(s)-len(".git"):], ".git") {
		s = s[:len(s)-len(".git")]
	}
	parts := strings.Split(s, "/")
	for _, p := range parts {
		if !RepoRE.MatchString(p) {
			return "", "", fmt.Errorf("coding: %q is not a repository", ref)
		}
	}
	if len(parts) > 2 {
		parts = parts[len(parts)-2:] // the host in front is where it lives, not what it is
	}
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", parts[0], nil
}

// homes are the repositories an estate org works on whose code is NOT in its
// forge namespace, by the name a person says, with where the code is: the
// private edition on GitHub. A name here never resolves on the forge.
//
// It is a declared table, as forge's owners are, because which edition is the
// one an org works on is a decision and not something a name can show.
// Measured 2026-10-26: `cloud` in org hanzo resolved to the forge's
// hanzoai/cloud, a one-README placeholder an early run created, and a run that
// had asked for the backend worked in an empty tree and closed done with
// nothing. hanzoai/cloud is the curated open-source edition, with history
// unrelated to the backend's; hanzo-inc/cloud is what production builds from.
// universe has the same split (hanzoai/universe redirects). A reference that
// names hanzoai means that namespace (home).
var homes = map[string]map[string]string{
	"hanzo": {"cloud": "hanzo-inc/cloud", "universe": "hanzo-inc/universe"},
}

// home is the GitHub repository a reference means when homes says its code is
// there: a bare name, or one under the org's own name spelled exactly as the
// org is. Any other owner — the forge namespace `hanzoai` among them — names
// that namespace's repository and is not mapped.
func home(org, owner, name string) (string, string, bool) {
	if owner != "" && !strings.EqualFold(owner, org) {
		return "", "", false
	}
	h, ok := homes[org][strings.ToLower(name)]
	if !ok {
		return "", "", false
	}
	o, n, _ := strings.Cut(h, "/")
	return o, n, true
}

// ours reports whether owner names the org's own namespace: its workspace on the
// forge, or the org itself, which is how a person spells it. No owner is the org's.
func ours(ws forge.Workspace, org, owner string) bool {
	if owner == "" || strings.EqualFold(owner, org) {
		return true
	}
	return ws.Owner != "" && strings.EqualFold(owner, ws.Owner)
}

// resolve is the repository name a reference means in the org's workspace ws,
// as actor sees its repositories — or, in a workspace the platform made, as the
// machine sees them, which is every one of them and nothing outside it.
func resolve(ctx context.Context, ws forge.Workspace, actor, name string) (string, error) {
	got, _, err := resolveRepo(ctx, ws, actor, name, "")
	return got, err
}

// resolveRepo is resolve together with the repository's default branch as the
// forge knows it (empty when the forge did not report one). project is the
// optional project slug carried on the request (e.g. "cssprod-sroute") so an
// imported GitHub repository mirrored as "<owner>_<repo>" resolves cleanly even
// when a surface sends only its basename ("Sroute").
func resolveRepo(ctx context.Context, ws forge.Workspace, actor, name, project string) (string, string, error) {
	c, err := forgeClient(ctx)
	if err != nil {
		if isLocalDev() {
			return name, "", nil
		}
		return "", "", err
	}
	owner := ws.Owner
	if owner == "" {
		return "", "", fmt.Errorf("coding: %w", forge.ErrNoOwner)
	}
	as := acting(c, ws, actor)
	// The cheap read first, because it is the common case: a repository by that
	// name. A sudoed miss is ErrUnknownActor — the same answer for "not there" and
	// "not yours to see" — and the list below tells the two causes apart. The
	// machine's miss is plain absence.
	r, err := as.Repo(ctx, owner, name)
	if err == nil {
		return r.Name, strings.TrimSpace(r.Branch), vacant(ws, r)
	}
	if !errors.Is(err, forge.ErrUnknownActor) && !errors.Is(err, forge.ErrNotFound) {
		return "", "", fmt.Errorf("coding: read %s/%s: %w", owner, name, err)
	}
	repos, err := as.Repos(ctx, owner)
	if errors.Is(err, forge.ErrUnknownActor) {
		// AN UNKNOWN ACTOR IS A MISSING FORGE ACCOUNT, and it says so. Sudo resolves
		// a login against a user row and does not create one, and the forge
		// provisions a user only at an interactive sign-in — so a person whose
		// first touch of the forge is a coding run has no row to act as, and the
		// generic refusal would read as "you cannot work on this repository" when
		// the truth is "you have never signed in". One sign-in is the fix.
		return "", "", fmt.Errorf("coding: %s has no account on the forge yet — sign in to %s once, then start the run",
			actor, c.Host())
	}
	if err != nil {
		return "", "", fmt.Errorf("coding: list %s's repositories: %w", owner, err)
	}
	got, near := pickFor(name, project, ws.Made, repos)
	if got != "" {
		for _, r := range repos {
			if r.Name == got {
				return got, strings.TrimSpace(r.Branch), vacant(ws, r)
			}
		}
	}
	where := owner
	if ws.Made {
		// Said as the person knows it. A repository on GitHub is reached by its
		// GitHub address, which is the one thing a refusal here can point to.
		where = "this organization's workspace (" + owner + ")"
	}
	switch {
	case got != "":
		return got, "", nil
	case len(near) == 0 && ws.Made:
		return "", "", fmt.Errorf("coding: %s has no repository named or serving %q; a repository on GitHub is named github.com/<owner>/<name>",
			where, name)
	case len(near) == 0:
		return "", "", fmt.Errorf("coding: %s has no repository named or serving %q", where, name)
	}
	return "", "", fmt.Errorf("coding: %s has no repository named or serving %q; did you mean %s?",
		where, name, strings.Join(near, ", "))
}

// placeholder is the description the early coding runs gave a repository they
// created when a name matched none (forge Ensure, since removed). Such a
// repository holds one README and is never the code a request meant.
const placeholder = "created for a Hanzo agent run"

// vacant refuses a repository of the estate's that holds no code: an empty one,
// or a placeholder an early run created. A run cloned into one works in an empty
// tree on a task about code it cannot see, and ends having done nothing. A
// workspace the platform made is not asked: a project's repository there begins
// as one README by design. Any other repository with no code in it is caught
// after its run, from the checkout itself (coding.go hollow).
func vacant(ws forge.Workspace, r forge.Repo) error {
	if ws.Made || !r.Empty && strings.TrimSpace(r.Description) != placeholder {
		return nil
	}
	return fmt.Errorf("%w: %s/%s on the forge holds no code, so it is not the repository you mean; "+
		"name the repository by its GitHub address, github.com/<owner>/<name>", errVacant, ws.Owner, r.Name)
}

// errVacant is the refusal of an empty repository, and it is final: a run is never
// sent on to a GitHub repository of the same name instead, because that name can
// be the open-source edition of the private code the caller meant.
var errVacant = errors.New("coding: empty repository")

// pick is the repository name means among repos: the one by that name, else the
// ONE whose website is that site. Otherwise it answers nothing and the names
// nearest it — the ones serving the site when several do, else the ones whose
// name holds the other — sorted, so the same question gets the same list.
func pick(name string, repos []forge.Repo) (string, []string) {
	return pickFor(name, "", false, repos)
}

// pickFor extends pick for platform-made workspaces and project-tagged runs:
// imported GitHub repositories are mirrored in the org's forge workspace under
// `<owner>_<repo>` (e.g. `cssprod_sroute`) while their project slug uses `-`
// (`cssprod-sroute`) and a UI often sends only the basename (`Sroute`).
//
// Resolution priority (never guessing across ambiguous matches):
//  1. Exact case-insensitive repository name.
//  2. Exact project-slug match (normalized `-` -> `_`) whose `_`-suffix matches
//     `name` (so a mismatched project cannot silently hijack a different repo).
//  3. Unique repository serving the named website host.
//  4. In a platform-made workspace, the ONE `<owner>_<repo>` mirror whose
//     `_`-delimited suffix equals `name` case-insensitively.
func pickFor(name, project string, made bool, repos []forge.Repo) (string, []string) {
	host := site(name)
	want := strings.ToLower(strings.TrimSpace(name))
	projSlug := strings.ToLower(strings.TrimSpace(project))
	projUnder := strings.ReplaceAll(projSlug, "-", "_")
	var serving, mirrored, near []string
	var projMatch string
	for _, r := range repos {
		if strings.EqualFold(r.Name, name) {
			return r.Name, nil
		}
		have := strings.ToLower(r.Name)
		haveDash := strings.ReplaceAll(have, "_", "-")
		if projSlug != "" && (have == projSlug || have == projUnder || haveDash == projSlug) &&
			(have == want || strings.HasSuffix(have, "_"+want) || strings.HasSuffix(have, "-"+want)) {
			projMatch = r.Name
		}
		if host != "" && site(r.Website) == host {
			serving = append(serving, r.Name)
		}
		if made && want != "" && strings.HasSuffix(have, "_"+want) {
			mirrored = append(mirrored, r.Name)
		}
		if strings.Contains(have, want) || (len(have) >= 4 && strings.Contains(want, have)) {
			near = append(near, r.Name)
		}
	}
	if projMatch != "" {
		return projMatch, nil
	}
	if len(serving) == 1 {
		return serving[0], nil
	}
	if len(serving) > 1 {
		near = serving
	} else if len(mirrored) == 1 {
		return mirrored[0], nil
	} else if len(mirrored) > 1 {
		near = mirrored
	}
	sort.Strings(near)
	if len(near) > maxCandidates {
		near = near[:maxCandidates]
	}
	return "", near
}

// site is the host a name or a website address names — lowercased, without a
// scheme, a path or a leading www — or "" when it names none.
func site(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s, _, _ = strings.Cut(s, "/")
	s = strings.TrimPrefix(s, "www.")
	if !strings.Contains(s, ".") {
		return ""
	}
	return s
}
