// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/commerce"
	gitpeer "github.com/hanzoai/cloud/client/git"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// sweeps serves a stand-in commerce on its socket whose two sweeps answer what the
// real ops answer — a *client.Renewal and a *client.Recharge, never a map — and
// refuse any caller but the platform, as the real renewal does. It counts the runs
// that got past that refusal.
func sweeps(t *testing.T) *atomic.Int32 {
	t.Helper()
	var runs atomic.Int32
	app := zip.New(zip.Config{AppName: commerce.App})
	app.Post("/billing/renew", func(ctx context.Context, _ *struct{}) (*client.Renewal, error) {
		if zip.CallerOf(ctx).Org != authz.AdminOrg {
			return nil, zip.ErrForbidden("renewal sweep: the platform runs it")
		}
		runs.Add(1)
		return &client.Renewal{Orgs: 3, Due: 1, Renewed: 1, Results: []client.Renewed{}}, nil
	}, zip.WithOperationID(client.BillingRenew))
	app.Post("/billing/recharge", func(context.Context, *struct{}) (*client.Recharge, error) {
		runs.Add(1)
		return &client.Recharge{}, nil
	}, zip.WithOperationID(client.BillingRecharge))
	planetest.Bind(t, commerce.App, app)
	return &runs
}

// wirePoke makes the cluster hold one poke entry that asks commerce's op by name.
func wirePoke(t *testing.T, name, op string) {
	t.Helper()
	setWiring(&fakeKube{entries: map[string]entry{
		name: {Name: name, Schedule: "0 * * * *", Kind: kindPoke, Poke: pokeSpec{App: commerce.App, Op: op}},
	}}, luxlog.NewNoOpLogger(), quick)
	t.Cleanup(func() { setWiring(nil, nil, steady) })
}

// The renewal sweep the schedule asks for runs once and the run is a success.
//
// Before, the poke asked for the reply as a map. The op answers a *client.Renewal:
// in this process the plane hands that back as is and refused it as the wrong type,
// and over a socket a map does not decode. Either way the sweep had already run, so
// cron-cloud-billing-renew failed every hour after it was enabled (hanzo-inc/cloud#828)
// while commerce swept each time, and the engine retried each failure twice more.
func TestAPlanePokeReadsTheReplyItsOpAnswers(t *testing.T) {
	runs := sweeps(t)
	wirePoke(t, "cloud-billing-renew", client.BillingRenew)

	out, err := poke(context.Background(), EntryInput{Name: "cloud-billing-renew"})
	if err != nil {
		t.Fatalf("the sweep answered and the run failed: %v", err)
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("the sweep ran %d times, want once", n)
	}
	if out.URL != "plane:commerce/billing_renew" || out.Status != http.StatusOK {
		t.Fatalf("result = %+v, want plane:commerce/billing_renew 200", out)
	}
}

// An op cron holds no client for is refused before it is asked, so a schedule can
// never run an op whose answer the run cannot read and then report it failed.
func TestAPlanePokeCronCannotReadNeverRuns(t *testing.T) {
	runs := sweeps(t)
	wirePoke(t, "cloud-billing-recharge", client.BillingRecharge)

	_, err := poke(context.Background(), EntryInput{Name: "cloud-billing-recharge"})
	if err == nil || !strings.Contains(err.Error(), "commerce/billing_recharge") {
		t.Fatalf("err = %v, want a refusal naming commerce/billing_recharge", err)
	}
	if n := runs.Load(); n != 0 {
		t.Fatalf("an op cron cannot read ran %d times", n)
	}
}

// The estate's open source collection is asked by name as the platform, and the run
// reads the *client.MetricsCollected the op answers.
func TestAPlanePokeAsksTheOpenSourceCollection(t *testing.T) {
	var runs atomic.Int32
	app := zip.New(zip.Config{AppName: gitpeer.App})
	app.Post("/git/metrics/collect", func(ctx context.Context, _ *struct{}) (*client.MetricsCollected, error) {
		if zip.CallerOf(ctx).Org != authz.AdminOrg {
			return nil, zip.ErrForbidden("metrics collection: the platform runs it")
		}
		runs.Add(1)
		return &client.MetricsCollected{Repos: 4, Figures: 16}, nil
	}, zip.WithOperationID(client.GitMetricsCollect))
	planetest.Bind(t, gitpeer.App, app)
	setWiring(&fakeKube{entries: map[string]entry{
		"oss-metrics": {Name: "oss-metrics", Schedule: "17 3 * * *", Kind: kindPoke, Poke: pokeSpec{App: gitpeer.App, Op: client.GitMetricsCollect}},
	}}, luxlog.NewNoOpLogger(), quick)
	t.Cleanup(func() { setWiring(nil, nil, steady) })

	out, err := poke(context.Background(), EntryInput{Name: "oss-metrics"})
	if err != nil {
		t.Fatalf("the collection answered and the run failed: %v", err)
	}
	if runs.Load() != 1 || out.URL != "plane:git/git_metrics_collect" || out.Status != http.StatusOK {
		t.Fatalf("runs %d, result %+v, want one run and plane:git/git_metrics_collect 200", runs.Load(), out)
	}
}
