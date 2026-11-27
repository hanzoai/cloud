package task

// home.go is a board whose issues live on GitHub, and the work an issue hands
// the agent.
//
// A board is a repository on the forge, and most of the estate's repositories
// there are pull mirrors of GitHub (forge/github.go). An issue filed on a mirror
// is filed where nobody looks, and no sync carries it home — so a board whose
// repository lives on GitHub files its issues THERE, through the App
// (apps/provider github_issue.go), and they come onto the board the way every
// GitHub issue does: mirrored into the index, bound to the thread they were
// asked for in. A key naming a repository GitHub alone holds reaches it the same
// way, admitted by the caller's own view of GitHub.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

const (
	// agent is the assignee that hands an issue to the coding agent. A GitHub App
	// cannot be an assignee, so the handing is the `agent` label, and it is the
	// same label whoever puts it on: this surface, or a person on GitHub.
	agent = "agent"
	// me is the assignee that means the caller.
	me = "me"
	// handBudget bounds admitting a run, which resolves a credential and opens
	// a session before it answers.
	handBudget = 90 * time.Second
)

// place is where a board key's issues live.
type place struct {
	// owner and name are the repository on GitHub, when its issues live there.
	owner, name string
	// repo is its name on the forge, when the forge holds it — its own, or a
	// mirror of owner/name.
	repo string
}

// hub reports whether the issues live on GitHub.
func (p place) hub() bool { return p.owner != "" }

// ref is how a coding run names the repository: by the forge's name when the
// forge holds it, so the forge's ACL admits the run, and by GitHub's address
// when only GitHub does.
func (p place) ref() string {
	if p.repo != "" {
		return p.repo
	}
	return "github.com/" + p.owner + "/" + p.name
}

// where resolves the repository a board key names, as the caller sees it: a
// repository on the forge, read as the actor, and its home when it mirrors
// GitHub; or a repository GitHub alone holds, which the caller's own GitHub view
// has to show them. Neither is a place the caller could not already reach.
func (o ops) where(ctx context.Context, cl *forge.Client, ns, key string) (place, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return place{}, err
	}
	if owner, name, ok := forge.GitHub(ns, org, key); ok {
		if _, err := providerpeer.IntegrationsGitHubRepo(ctx, &client.GitHubRepoIn{Owner: owner, Repo: name}); err != nil {
			var he *zip.HTTPError
			if errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusForbidden) {
				return place{}, zip.ErrNotFound(fmt.Sprintf("no repository %s/%s you can see on GitHub", owner, name))
			}
			return place{}, zip.Errorf(http.StatusServiceUnavailable, "github unavailable: %v", err)
		}
		return place{owner: owner, name: name}, nil
	}
	name, r, err := resolveForgeRepo(ctx, cl, ns, org, key)
	if err != nil {
		if errors.Is(err, forge.ErrUnknownActor) {
			return place{}, zip.ErrNotFound("no such board")
		}
		return place{}, o.answer(err)
	}
	p := place{repo: name}
	p.owner, p.name, _ = r.Home()
	return p, nil
}

// resolveForgeRepo looks up a board key on the forge as the caller sees it:
// exact repository name first, then an org-prefixed slug (`<org>-<repo>` ->
// `<repo>`), then imported GitHub mirror slugs whose `-` separator corresponds
// to the forge's `<owner>_<repo>` mirror name (e.g. `cssprod-sroute` ->
// `cssprod_sroute` or `cssprod-my-app` -> `cssprod_my-app`). Every probe runs
// through cl.Repo under the caller's own Sudo actor, so ACL enforcement is
// identical to a direct read.
func resolveForgeRepo(ctx context.Context, cl *forge.Client, ns, org, key string) (string, forge.Repo, error) {
	name := strings.TrimSuffix(key[strings.LastIndex(key, "/")+1:], ".git")
	r, err := cl.Repo(ctx, ns, name)
	if err == nil {
		return name, r, nil
	}
	if !errors.Is(err, forge.ErrUnknownActor) && !errors.Is(err, forge.ErrNotFound) {
		return "", forge.Repo{}, err
	}
	if org != "" && len(name) > len(org)+1 && strings.EqualFold(name[:len(org)+1], org+"-") {
		trimmed := name[len(org)+1:]
		if r2, err2 := cl.Repo(ctx, ns, trimmed); err2 == nil {
			return trimmed, r2, nil
		}
	}
	for _, cand := range mirrorSlugCandidates(name) {
		if r2, err2 := cl.Repo(ctx, ns, cand); err2 == nil {
			return cand, r2, nil
		}
	}
	return "", forge.Repo{}, err
}

// mirrorSlugCandidates produces bounded `<owner>_<repo>` forge name candidates
// from a hyphenated project slug (e.g. `cssprod-sroute` -> `cssprod_sroute`,
// `my-org-my-app` -> `my_org-my-app`, `my-org_my-app`, `my-org-my_app`, `my_org_my_app`).
func mirrorSlugCandidates(name string) []string {
	if !strings.Contains(name, "-") {
		return nil
	}
	var out []string
	seen := map[string]bool{name: true}
	const maxSplits = 4
	splits := 0
	for i := 0; i < len(name) && splits < maxSplits; i++ {
		if name[i] != '-' {
			continue
		}
		splits++
		cand := name[:i] + "_" + name[i+1:]
		if !seen[cand] {
			seen[cand] = true
			out = append(out, cand)
		}
	}
	if all := strings.ReplaceAll(name, "-", "_"); !seen[all] {
		out = append(out, all)
	}
	return out
}

// sameBoardKey reports whether a card's board key matches the requested filter,
// treating hyphenated project slugs (`cssprod-sroute`) and `<org>-<repo>`
// prefixes equivalently to their underlying forge repository (`cssprod_sroute`
// / `<repo>`).
func sameBoardKey(have, want, org string) bool {
	if strings.EqualFold(have, want) {
		return true
	}
	if strings.EqualFold(strings.ReplaceAll(have, "_", "-"), strings.ReplaceAll(want, "_", "-")) {
		return true
	}
	if org != "" && len(want) > len(org)+1 && strings.EqualFold(want[:len(org)+1], org+"-") {
		trimmed := want[len(org)+1:]
		if strings.EqualFold(have, trimmed) || strings.EqualFold(strings.ReplaceAll(have, "_", "-"), strings.ReplaceAll(trimmed, "_", "-")) {
			return true
		}
	}
	return false
}

// github makes one write to an issue GitHub holds and renders the answer as a
// card. The caller rides ctx, so the write is audited as them. A card on a
// mirror keeps the board's key, so the next edit reaches it the way this one
// did — through the forge's ACL.
func github(ctx context.Context, p place, in client.GitHubIssueIn) (*client.GitHubIssue, *issueView, error) {
	got, err := providerpeer.IntegrationsGitHubIssue(ctx, &in)
	if err != nil {
		return nil, nil, err
	}
	v := hubView(got)
	if p.repo != "" {
		v.ProjectKey, v.Identifier = p.repo, ident(p.repo, got.Number)
	}
	return got, &v, nil
}

// hubView renders an issue GitHub holds as a card, in the one shape every row on
// this surface has. Its board is the repository's GitHub name.
func hubView(g *client.GitHubIssue) issueView {
	labels := make([]forge.Label, 0, len(g.Labels))
	for _, l := range g.Labels {
		labels = append(labels, forge.Label{Name: l})
	}
	status, priority, rest := classify(labels)
	if strings.EqualFold(g.State, "closed") {
		status = "done"
	}
	full := g.Owner + "/" + g.Repo
	ref := "github:" + full + "#" + strconv.Itoa(g.Number)
	return issueView{
		ID: ref, Identifier: ident(full, g.Number), ProjectKey: full, Number: g.Number,
		Kind: "issue", Source: "git", Repo: g.Repo, ExtRef: ref,
		Title: g.Title, Description: g.Body, Status: status, Priority: priority,
		Assignee: g.Assignee, Labels: rest, URL: g.URL,
	}
}

// handOff starts the coding run an issue asks for, as the caller. It narrates
// where the hand-off was asked for, or the thread the issue is bound to, and the
// coding app takes the org's issue channel for a run with neither.
//
// Its own budget, detached from the request's: the write that labelled the
// issue has happened, and a run whose admission is cut short by the request
// ending is a label with no run behind it.
func handOff(ctx context.Context, p place, number int, title, body string, to client.Reply) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handBudget)
	defer cancel()
	in := client.Work(p.ref(), number, title, body, to)
	_, err := plane.Call[client.CodingStartIn, client.CodingStarted](ctx, agentpeer.App, client.AgentsCodingStart, &in)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "labelled %q, but the run did not start: %v", agent, err)
	}
	return nil
}

// thread is the conversation a write names, or the one the issue is bound to.
func thread(to client.Reply, channel string) client.Reply {
	if to.Channel != "" {
		return to
	}
	bound, _ := client.ReplyTo(channel)
	return bound
}
