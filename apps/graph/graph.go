// Package graph is one organization's entities and the relations between them,
// held as assertions: somebody, at some moment, from some evidence, asserted
// that this thing stands in that relation to that other thing.
//
// Nodes and edges are how it reads on the wire; an assertion is what is stored.
// A node is NOT a row — an entity exists because something was asserted about it
// — so there is no node table, no create and no cascade delete. An edge is an
// assertion whose value names another entity; a property is an assertion whose
// value is a scalar. One table, one bit (`names`) telling them apart.
//
// WHY THIS AND NOT A MUTABLE NODE/EDGE STORE. An upsert-by-id graph is easier to
// write and silently loses the four things this plane exists to provide: who
// said so, when it became true, what disagreed, and what it looked like last
// Tuesday. Provenance is columns, point-in-time is the derived knowable instant,
// conflict is the shared total order, and entity resolution is the `same`
// relation resolved by that same order. None of them is a subsystem.
//
// THE ALGEBRA IS NOT HERE. The derived instant, the content address, the
// observation window and the order live in `claim`, shared with the ground-truth
// plane (apps/label) so that this binary holds ONE answer to "somebody asserted
// something about a thing" rather than two that drift. This package contributes
// the vocabulary, the bounds and the store — which is all a caller of that
// algebra is meant to contribute.
//
// TENANCY is physical: one SQLite file per (organization, project) through
// cloud.OrgDB, so another organization's assertions are not in the database being
// read and no predicate can be forgotten. See HIP-1198.
//
// That pair IS the graph database, which is why this plane holds no registry of
// graphs. An org names one by carrying a project, its default project is the
// whole-org view, and the files are made on first assertion and reclaimed when
// idle. Nothing here would be gained by a second primitive: a graphs table would
// be a name for a project, kept somewhere else, able to disagree with it.
package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type state struct {
	stores *cloud.OrgStore[*store]
	log    luxlog.Logger
}

// mounted is the process handle, so Shutdown closes what Mount opened.
var mounted *state

// scope is the tenant a request writes as. `by` is stamped from the validated
// principal and never from the body: an attributable record whose attribution
// the caller chose is not attributable.
type scope struct {
	ns namespace.Namespace
	by string
}

func build(deps cloud.Deps) (*cloud.Service[*state], error) {
	if deps.DataDir == "" {
		return nil, fmt.Errorf("graph.Use:  empty deps.DataDir, so no assertion could be kept")
	}
	b := cloud.NewBase(deps, "graph")
	return &cloud.Service[*state]{Base: b, State: &state{
		stores: cloud.NewOrgStore[*store](b, "graph", openStore),
		log:    b.Log,
	}}, nil
}

func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("graph.Use:  nil app")
	}
	s, err := build(deps)
	if err != nil {
		return err
	}
	mounted = s.State
	routes(app, s)
	s.Log.Info("graph plane mounted", "env", deps.Env)
	return nil
}

// Shutdown closes every tenant file this process opened. An assertion that only
// existed in a pod is an assertion that did not exist.
func Shutdown() error {
	if mounted == nil || mounted.stores == nil {
		return nil
	}
	return mounted.stores.CloseAll()
}

// tenantOf resolves which graph database a request reads and writes, from the
// VALIDATED principal and nothing else. No caller value reaches the namespace or
// the path.
//
// The database is the (org, project) pair. An org's PROJECT is what names one
// graph among its graphs, and it is safe to select a file with because it is
// server-minted — IAM mints the project claim, and both minters bind the header
// from it after stripping any client copy, so a caller cannot name a database by
// asking for it. The default project is the whole-org view and renders the file
// this plane has always opened, so an org that names no project still reads every
// assertion it has ever made.
func tenantOf(ctx context.Context, s *cloud.Service[*state]) (scope, *store, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return scope{}, nil, zip.ErrForbidden("no validated principal")
	}
	// principal.Acting, not OrgFrom plus a sentence of our own: a caller who
	// authenticated but carries no org scope is refused for THAT, and telling
	// them their principal is invalid sends them to fix the one thing that is
	// already right. The wording lives in one place so it cannot fork.
	org, err := principal.Acting(ctx)
	if err != nil {
		return scope{}, nil, err
	}
	ns, err := cloud.OrgNamespace(org, principal.ProjectScopeFrom(ctx))
	if err != nil {
		return scope{}, nil, err
	}
	st, err := s.State.stores.For(ns)
	if err != nil {
		return scope{}, nil, err
	}
	return scope{ns: ns, by: actor(c, org)}, st, nil
}

func actor(c *zip.Ctx, org string) string {
	home := principal.Owner(c)
	if home == "" {
		home = org
	}
	user := strings.TrimSpace(c.User())
	if user == "" {
		return home
	}
	return home + "/" + user
}
