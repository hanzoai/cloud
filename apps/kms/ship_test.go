package kms

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// trail is the deployment's audit trail as kms reaches it: admin's chain behind a
// Remote recorder, or a holder that does not answer.
type trail struct {
	chain *audit.Recorder
	down  bool
}

func (t trail) Append(ctx context.Context, r audit.Record) (audit.Record, error) {
	if t.down {
		return audit.Record{}, errors.New("admin is not answering")
	}
	return t.chain.Append(ctx, r)
}

func (t trail) Query(ctx context.Context, f audit.Filter) ([]audit.Record, int, error) {
	return t.chain.Query(ctx, f)
}

// recorder is ops writing to admin's chain through a Remote recorder, the shape a
// kms process has in production. down makes admin unreachable.
func recorder(t *testing.T, down bool) (ops, *audit.Recorder) {
	t.Helper()
	chain, err := audit.Open(t.TempDir(), "audit-admin", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	st := state{audit: audit.Remote("admin", trail{chain: chain, down: down})}
	return ops{s: &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "kms"), State: st}}, chain
}

var ci = principal.Principal{Org: "hanzo", User: "admin/hanzo-ci", Holder: "hanzo-ci", Client: "hanzo-ci"}

// The trail takes EVERY verdict, the served read included, with the holder, the
// client, the verb, the coordinate and the verdict — once each, in the chain
// /v1/admin/audit reads.
func TestEveryVerdictIsOnTheTrail(t *testing.T) {
	o, chain := recorder(t, false)
	verdicts := []string{verdictGrant, verdictSuper, verdictDeny, verdictWouldDeny}
	for _, v := range verdicts {
		if err := o.record(context.Background(), nil, ci, "hanzo", Read, "/deploy/GITHUB_TOKEN@prod", v); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	rows, total, err := chain.Query(context.Background(), audit.Filter{Action: "kms.read"})
	if err != nil || total != len(verdicts) {
		t.Fatalf("the trail took %d records (err %v), want one per verdict", total, err)
	}
	for _, r := range rows {
		if r.Actor.Org != "hanzo" || r.Resource.ID != "hanzo:/deploy/GITHUB_TOKEN@prod" || r.Actor.Sub != "admin/hanzo-ci" {
			t.Errorf("record: %+v", r)
		}
	}
}

// An access the trail does not take is refused (AU-5): every verdict that opens a
// name or a value answers 503, and a deny stays a deny.
func TestAnAccessTheTrailDidNotTakeIsRefused(t *testing.T) {
	o, _ := recorder(t, true)
	for _, v := range []string{verdictGrant, verdictAdmin, verdictWouldDeny, verdictSuper} {
		err := o.record(context.Background(), nil, ci, "hanzo", Read, "/x@prod", v)
		var ze *zip.HTTPError
		if !errors.As(err, &ze) || ze.Status != 503 {
			t.Errorf("%s served with the trail down: %v", v, err)
		}
	}
	if err := o.record(context.Background(), nil, ci, "hanzo", Read, "/x@prod", verdictDeny); err != nil {
		t.Errorf("a deny answered %v; it is refused by its verdict, not by the trail", err)
	}
}

// Health says when the trail is not taking records, and readiness goes with it.
func TestHealthReportsTheTrail(t *testing.T) {
	o, _ := recorder(t, true)
	o.s.State.trailed = &trailed{}
	_ = o.record(context.Background(), nil, ci, "hanzo", Read, "/x@prod", verdictGrant)
	if !o.s.State.trailed.failed() {
		t.Fatal("a record the trail refused left health reading ok")
	}
	up, _ := recorder(t, false)
	up.s.State.trailed = o.s.State.trailed
	if err := up.record(context.Background(), nil, ci, "hanzo", Read, "/x@prod", verdictGrant); err != nil || up.s.State.trailed.failed() {
		t.Fatalf("a record the trail took left health failing (%v)", err)
	}
}

// A SuperAdmin act the trail did not take is refused (AU-5), and so is one in a
// process with no trail at all.
func TestASuperAdminActNeedsTheTrail(t *testing.T) {
	o, _ := recorder(t, true)
	if err := o.record(context.Background(), nil, ci, "hanzo", Read, "/x@prod", verdictSuper); err == nil {
		t.Fatal("a SuperAdmin read the trail did not take was served")
	}
	none := ops{s: &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "kms")}}
	if err := none.record(context.Background(), nil, ci, "hanzo", Read, "/x@prod", verdictSuper); err == nil {
		t.Fatal("a SuperAdmin read with no trail was served")
	}
	o, _ = recorder(t, false)
	if err := o.record(context.Background(), nil, ci, "hanzo", Read, "/x@prod", verdictSuper); err != nil {
		t.Fatalf("a SuperAdmin read the trail took was refused: %v", err)
	}
}
