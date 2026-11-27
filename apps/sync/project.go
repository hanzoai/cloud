package sync

// project.go — every repository an org links is one of its projects.
//
// A repository link is made a project the moment it is declared: by POST
// /v1/sync, or by an account link declaring one per repository its account
// holds. The project store is the projects app's, so the projects app makes it
// (client.ProjectsLink), answering the project that already builds from that
// repository when there is one. GET /v1/project then lists it beside every other
// project, which is what hanzo.ai and the platform both read.
//
// Each link is made a project ONCE, and the projected table says which were. A
// project its org deleted stays deleted while the link lives; unlinking leaves
// the project, as it leaves the repository's copy on the forge.
//
// Links declared before this, and any whose making failed (the projects app was
// away), are made at boot ([backfill]).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/namespace"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/sdk"
)

// project makes each repository link in links a project of its org, skipping
// the ones already made. Every failure is returned; one does not stop the rest.
func project(ctx context.Context, st *store, links []Sync) error {
	done, err := st.Projected(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, sy := range links {
		if sy.Kind != "git" || sy.account() != "" || done[sy.ID] {
			continue
		}
		call, stop := asOrg(ctx, sy.Org)
		_, err := projectpeer.ProjectsLink(call, &client.LinkIn{Repo: sy.Source.Locator})
		stop()
		if err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", sy.Source.Locator, err))
			continue
		}
		if err := st.Project(ctx, sy.ID, time.Now().Unix()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// backfillAfter is how long after boot the first backfill pass waits, so the
// projects app this asks is up.
const backfillAfter = time.Minute

// backfill makes a project of every repository link not yet made one, across
// every org this replica acts for, and retries with backoff until a pass leaves
// none behind or ctx ends.
func backfill(s *cloud.Service[state], ctx context.Context) {
	wait := backfillAfter
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if pass(s, ctx) {
			return
		}
		wait = min(wait*2, time.Hour)
	}
}

// pass is one backfill over every org's store this replica acts for. It reports
// whether every link is now a project.
func pass(s *cloud.Service[state], ctx context.Context) bool {
	clean := true
	err := s.State.stores.Each(func(ns namespace.Namespace, st *store, openErr error) {
		if ctx.Err() != nil {
			clean = false
			return
		}
		if openErr != nil {
			s.Log.Warn("sync project: open store", "namespace", ns, "err", openErr)
			clean = false
			return
		}
		if !s.State.stores.Owned(ns) {
			return
		}
		links, err := st.ListAll(ctx)
		if err == nil {
			err = project(ctx, st, links)
		}
		if err != nil {
			s.Log.Warn("sync project: links not yet projects", "namespace", ns, "err", err)
			clean = false
		}
	})
	if err != nil {
		s.Log.Warn("sync project: enumerate orgs", "err", err)
		return false
	}
	return clean
}
