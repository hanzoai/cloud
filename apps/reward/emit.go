package reward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/engine"
	"github.com/hanzoai/reward/refuse"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// emitTimeout bounds one statement to the rules engine.
const emitTimeout = 30 * time.Second

// state is what the feeders share: the engine, the store beside it, and the lease
// the loops that must run once per deployment are held under.
type state struct {
	roll rolled
	eng  *engine.Engine
	db   orm.DB
	base cloud.Base
}

// emit states one event to the rules engine, in process. The engine is idempotent
// by the event's id: the same id again is the same claim, so a feeder that runs
// twice, or two replicas that both see a delivery, award nothing twice. An event
// the catalog refuses is a feeder bug and comes back as the refusal.
func (s *state) emit(ctx context.Context, ev catalog.Event) (engine.Outcome, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitTimeout)
	defer cancel()
	out, err := s.eng.Evaluate(ctx, ev)
	if err != nil {
		if refuse.Is(err) {
			return out, fmt.Errorf("reward: %s %s refused: %w", ev.Type, ev.ID, err)
		}
		return out, fmt.Errorf("reward: %s %s: %w", ev.Type, ev.ID, err)
	}
	return out, nil
}

// claimed is how many claims an outcome made now.
func claimed(out engine.Outcome) int {
	n := 0
	for _, d := range out.Decisions {
		if d.Result == engine.Claimed {
			n++
		}
	}
	return n
}

// platform reports whether the caller of a plane op is the platform: the brand
// whose deployment this is, or the admin org. A plane op names no member, so only
// a peer that acts for one of these may state events or read across tenants.
func platform(ctx context.Context) bool {
	org, ok := cloud.Tenant(ctx)
	return ok && (org == cloud.Brand() || org == authz.AdminOrg)
}

// errNotPlatform is the refusal for a plane op asked by anyone else.
var errNotPlatform = zip.ErrForbidden("reward: only the platform states events")

// exposeGitHub publishes the GitHub delivery on the internal plane. Use calls it.
func (s *state) exposeGitHub() {
	cloud.Plane().Post("/reward/github", s.github,
		zip.WithOperationID(client.RewardGitHub),
		zip.WithSummary("Tell the rewards program one verified GitHub delivery"))
	cloud.Plane().Post("/reward/event", s.event,
		zip.WithOperationID(client.RewardEvent),
		zip.WithSummary("State one event to the rewards program's rules engine"))
}

// event states one event another process saw, as the catalog lists it. The engine
// refuses what is not exactly an event of the catalog, and a refusal is the
// caller's bug, answered 400. Only the platform asks.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (s *state) event(ctx context.Context, in *client.RewardEventIn) (*client.RewardEventOut, error) {
	if !platform(ctx) {
		return nil, errNotPlatform
	}
	if in == nil {
		return nil, zip.ErrBadRequest("event: an event is required")
	}
	fields, err := in.FieldMap()
	if err != nil {
		return nil, zip.ErrBadRequest("event: " + err.Error())
	}
	ev := catalog.Event{ID: in.ID, Type: in.Type, Subject: in.Subject, Org: in.Org, Fields: fields, At: time.Now().UTC()}
	if len(in.Evidence) > 0 {
		var e catalog.Evidence
		if err := json.Unmarshal(in.Evidence, &e); err != nil {
			return nil, zip.ErrBadRequest("event: evidence is not evidence")
		}
		ev.Evidence = &e
	}
	out, err := s.emit(ctx, ev)
	if err != nil {
		if refuse.Is(err) {
			return nil, zip.ErrBadRequest(err.Error())
		}
		return nil, zip.Errorf(502, "event: %v", err)
	}
	return &client.RewardEventOut{Claims: claimed(out)}, nil
}

// github takes one GitHub webhook delivery, as the provider app verified it, and
// states what it earns to the rules engine. A delivery that earns nothing is
// answered with the reason, never failed, so GitHub is not asked to retry it.
// Only the platform asks.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (s *state) github(ctx context.Context, in *client.GitHubDelivery) (*client.GitHubTold, error) {
	if !platform(ctx) {
		return nil, errNotPlatform
	}
	if in == nil || in.Event == "" || len(in.Body) == 0 {
		return nil, zip.ErrBadRequest("github: event and body are required")
	}
	n, why, err := s.delivery(ctx, in.Event, in.Delivery, in.Body)
	if err != nil {
		if errors.Is(err, errUnreadable) {
			return nil, zip.ErrBadRequest("github: " + err.Error())
		}
		return nil, zip.Errorf(502, "github: %v", err)
	}
	return &client.GitHubTold{Events: n, Reason: why}, nil
}
