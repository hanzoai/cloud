package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type state struct {
	stores *cloud.OrgStore[*store]
	log    luxlog.Logger
	// audit is the trail an erasure and a declaration are written to before
	// they commit. Nil on a deployment with none, and both are then refused.
	audit *audit.Recorder
	// ai is the model plane an answer completes through (answer.go), metered
	// per call against the calling org. Nil where none is configured, and then
	// only an answer is refused.
	ai infer.Client
}

// mounted is the process handle, so Shutdown closes what Mount opened.
var mounted *state

// scope is the tenant a request writes as. `by` is stamped from the validated
// principal and never from the body: an attributable record whose attribution
// the caller chose is not attributable.
type scope struct {
	ns  namespace.Namespace
	by  string
	org string
}

func build(deps cloud.Deps) (*cloud.Service[*state], error) {
	if cloud.DataDir() == "" {
		return nil, fmt.Errorf("graph.Use:  empty cloud.DataDir(), so no assertion could be kept")
	}
	b := cloud.NewBase(deps, "graph")
	return &cloud.Service[*state]{Base: b, State: &state{
		stores: cloud.NewOrgStore[*store](b, "graph", openStore),
		log:    b.Log,
		audit:  deps.Audit,
		ai:     deps.AI,
	}}, nil
}

func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("graph: nil app")
	}
	s, err := build(deps)
	if err != nil {
		return err
	}
	mounted = s.State
	routes(app, s)
	exposeRestate()
	s.Log.Info("graph plane mounted", "env", cloud.Env())
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
	ns, err := principal.OrgNamespace(org, principal.ProjectScopeFrom(ctx))
	if err != nil {
		return scope{}, nil, err
	}
	st, err := s.State.stores.For(ns)
	if err != nil {
		return scope{}, nil, err
	}
	return scope{ns: ns, by: actor(c, org), org: org}, st, nil
}

// admin reports whether the caller administers the organization the request
// acts in: cloud.Admin, less the half of it that is platform sudo. The identity
// boundary mints OrgAdmin for the org ACTED IN, so a SuperAdmin switched into a
// tenant is refused unless that tenant made them one of its own admins. An
// erasure and a schema write each change the graph every member of the
// organization reads, so both ask this and nothing else.
func admin(ctx context.Context) bool {
	a := cloud.AuthorityIn(ctx)
	return cloud.Admin.Admits(a) && a.OrgAdmin
}

// act is the audit record of one change an admin made to this graph: who, from
// where, on which request, and the change's resource and account of itself. An
// erasure and a declaration are both recorded through here, in one shape.
func act(c *zip.Ctx, org, action, id string, at time.Time, after map[string]any) (audit.Record, error) {
	b, err := json.Marshal(after)
	if err != nil {
		return audit.Record{}, err
	}
	return audit.Record{
		Time:      at,
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    action,
		Resource:  audit.Resource{Type: action, ID: id},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: principal.Super(c)},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  geo.IP(c),
		RequestID: c.RequestID(),
		After:     audit.Redact(b),
	}, nil
}

// unmade answers a record the trail took for a change that then did not commit.
// The trail is append-only, so the record is answered beside itself, under the
// same resource, rather than left to claim a change that did not happen.
func (s *state) unmade(ctx context.Context, r audit.Record, graph string) {
	r.Outcome = audit.Outcome{Result: "error", Status: http.StatusServiceUnavailable,
		Reason: "recorded and not committed; nothing was changed"}
	if _, err := s.audit.Append(ctx, r); err != nil {
		s.log.Error("graph: the trail holds a change that did not commit, and the correction could not be recorded",
			"graph", graph, "action", r.Action, "resource", r.Resource.ID, "err", err)
	}
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
