package sync

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk/mint"
)

// account.go is the account sync: one link that covers every repository a
// GitHub account holds, including the ones created after it was declared.
//
// It is a Sync like any other — same row, same engine, same direction and
// trigger — whose SOURCE is an account (https://github.com/<account>) rather
// than a repository. Reconciling it does not move a ref. It makes sure every
// repository the org's installation grants on that account has a repository
// sync of its own, with the account sync's direction, trigger and actor, and
// queues the import of each one it had to declare. From there each repository
// is an ordinary sync: the webhook advances it, the scheduler polls it, the
// console lists it.
//
// What starts a reconcile is what starts any other: a manual run, the poll
// sweep, or an event. A manual run and a sweep list the account's whole grant;
// an event — a push, or GitHub announcing a repository — names the one
// repository it is about, which is enough to declare that one without asking
// GitHub for the rest.
//
// The org decides once, by declaring the account sync. Which repositories it
// covers is decided by the installation's grant on GitHub, which the account's
// owner controls: "All repositories" covers every one, a selection covers that
// selection.

// accountTarget is the target locator an account sync carries: every repository
// lands under its own name, so there is no one name to give.
const accountTarget = "*"

// accountLocator returns the account an account-sync source names, or "" for a
// repository source. An account source is an https address whose path is one
// segment: https://github.com/<account>.
func accountLocator(locator string) string {
	u, err := url.Parse(strings.TrimSpace(locator))
	if err != nil {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	if path == "" || strings.Contains(path, "/") {
		return ""
	}
	return fold(path)
}

// account is the account this sync covers, or "" for a repository sync.
func (sy Sync) account() string {
	if sy.Kind != "git" {
		return ""
	}
	return accountLocator(sy.Source.Locator)
}

// expand reconciles an account sync: it declares the repository syncs the
// account is missing and queues each one's import. It reports changed when it
// read the whole grant — so the account sync's last-synced time is the last time
// the account was read — or declared a repository an event named.
func expand(ctx context.Context, sy Sync, ev Event) (bool, error) {
	s := mounted.Load()
	if s == nil {
		return false, errors.New("sync: not mounted")
	}
	st, err := storeFor(s, sy.Org)
	if err != nil {
		return false, err
	}
	account := sy.account()
	var sources []string
	if ev.Manual {
		// The installation lives in the integrations process; this one asks it,
		// stating the sync's own org, so only an account that org connected answers.
		call, done := asOrg(ctx, sy.Org)
		out, err := providerpeer.IntegrationsGitHubAccount(call, &client.GitHubAccountIn{Account: account})
		done()
		if err != nil {
			return false, fmt.Errorf("read %s's repositories: %w", account, err)
		}
		for _, r := range out.Repos {
			sources = append(sources, r.CloneURL)
		}
	} else {
		name := fold(ev.Repo)
		if name == "" {
			name = repoNameFromLocator(ev.Locator)
		}
		if name == "" {
			return false, nil
		}
		sources = []string{"https://github.com/" + account + "/" + name + ".git"}
	}
	made, err := declare(ctx, st, sy, sources)
	// Each repository declared is one of the org's projects (project.go).
	if perr := project(ctx, st, made); perr != nil {
		s.Log.Warn("sync account: repositories not yet projects", "sync", sy.ID, "err", perr)
	}
	for _, child := range made {
		spawnReconcile(st, child)
	}
	if len(made) > 0 {
		s.Log.Info("sync account: declared repositories", "sync", sy.ID, "account", account, "declared", len(made))
	}
	if err != nil {
		return len(made) > 0, err
	}
	return ev.Manual || len(made) > 0, nil
}

// declare writes a repository sync for each source the account sync does not
// yet have one for, inheriting its direction, trigger and actor, and returns the
// ones it wrote. A source the forge could not hold is skipped, not fatal: one
// unspellable repository must not stop the rest of an account syncing.
func declare(ctx context.Context, st *store, sy Sync, sources []string) ([]Sync, error) {
	var made []Sync
	for _, raw := range sources {
		src, err := validateGitSource(endpointReq{Provider: provGitHub, Locator: raw})
		if err != nil || accountLocator(src.Locator) != "" {
			continue
		}
		tgt := deriveGitTarget(endpointReq{}, src)
		if _, err := st.GetByEndpoints(ctx, sy.Org, "git", src.Locator, tgt.Locator); err == nil {
			continue
		} else if !errors.Is(err, errNotFound) {
			return made, err
		}
		now := time.Now().Unix()
		if err := st.Upsert(ctx, Sync{
			ID: mint.ID("sync"), Org: sy.Org, Kind: "git", Source: src, Target: tgt,
			Direction: sy.Direction, Trigger: sy.Trigger, Actor: sy.Actor,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return made, fmt.Errorf("declare %s: %w", src.Locator, err)
		}
		child, err := st.GetByEndpoints(ctx, sy.Org, "git", src.Locator, tgt.Locator)
		if err != nil {
			return made, fmt.Errorf("read %s: %w", src.Locator, err)
		}
		made = append(made, child)
	}
	return made, nil
}
