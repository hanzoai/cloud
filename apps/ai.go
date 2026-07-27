// Copyright © 2026 Hanzo AI. MIT License.

package apps

import (
	"context"
	"fmt"

	aiobject "github.com/hanzoai/ai/object"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// ai.go binds what the co-resident inference module (hanzoai/ai) resolves from its
// HOST process: the money plane its prepaid gate reads and debits, and the durable
// queue it hands long ingests to. The module declares each as a typed hook over
// plain data and calls whatever the host installed — so a debit is a direct Go call,
// not an HTTP request the host has to re-route back to itself.
//
// The composition root is the ONE place that may know both sides. Package cloud must
// not: it is imported by nearly every clients/* package, so calling the module's
// setters from there linked the module's whole closure (~1.5k packages) into all of
// them to hand over four functions. Same decoupling as wire_seams.go and ledger.go —
// the root composes leaves that must not import each other.

// installAI installs every host binding the inference module resolves. Called by
// Wire(), before cloud.Serve, so both bindings are in place before the module mounts
// and long before it can serve a request.
func installAI() {
	// The durable queue. dialTasks resolves the engine at DIAL time, not here: the
	// engine starts after MountAll, long after Wire().
	aiobject.SetIngestDialer(dialTasks)

	// The money plane needs the metering client and the published ledger, so cloud
	// calls back once BuildDeps has built both.
	cloud.RegisterBillingInstaller(installBilling)
}

// installBilling binds the module's prepaid gate to cloud's in-process money plane.
// Three hooks, each fail-SAFE or fail-CLOSED exactly as the money it guards demands:
//
//   - TIER (subscription): the caller's commerce plan, read through the SAME
//     co-resident commerce client the metering gate bills over — in-process when
//     commerce is folded in, S2S HTTP with the service token otherwise, NEVER an
//     authed self-call to the cloud edge. That self-call is the toothless-gate bug:
//     the edge 401/403s a service call to /v1/billing/*, so the module's own HTTP
//     lookup always returned "" in-cluster and every tier-gated SKU failed OPEN.
//     Client.Tier folds a commerce error or an unknown plan to "", which the gate
//     treats as ALLOW, so a commerce blip never locks out a paying caller.
//
//   - BALANCE + USAGE (cash): the per-org SQLite double-entry wallet. There is NO
//     exempt path (hanzoai/ai >= v1.805.8): every principal is gated on a positive
//     prepaid balance, fail-closed.
//
// A hook left unset is not a gap: the module then takes its own HTTP billing path,
// which is the correct behavior for a split deployment where the money layer is not
// co-resident.
func installBilling(deps cloud.Deps) {
	if m := deps.Metering; m != nil && m.Enabled() {
		aiobject.SetTierReader(m.Tier)
		deps.Logger.Info("ai per-tier SKU gate wired to co-resident commerce (in-process tier read, fail-safe)")
	}

	fin := finance.Current()
	if fin == nil {
		return // money layer not co-resident (split-deploy); ai falls back to HTTP.
	}

	// Money is billed to the SUBJECT's wallet, inside the org's ledger.
	//
	// org  = which ledger (the tenant's books)
	// subject = which wallet in it (ai resolves it: a person => "org/name", an
	//           org-owned application/service key => the org's own account)
	//
	// So a personal account has a PERSONAL balance and a personal plan, and an org
	// pays for what its applications and service keys spend — which is the product:
	// sign up as yourself, then stand up an org whose users are your customers.
	//
	// Keying both hooks on the org collapsed every member onto the tenant's pool
	// wallet: every new signup lives in "hanzo", so a brand-new $0 account read
	// HANZO's balance and sailed through the gate — we enforced our own wallet.
	//
	// The invariant that must never break: the gate READ and the usage DEBIT key on
	// the SAME wallet, or spend can outrun the balance that admitted it. Both use
	// subject; keep them together. The gate reads a coarse cents balance (a >0
	// threshold only); the DEBIT is 18-decimal-exact.
	aiobject.SetBalanceReader(func(ctx context.Context, subject, namespace, currency string) (int64, error) {
		bal, err := fin.Balance(ctx, namespace, subject, currency, false)
		if err != nil {
			return 0, err
		}
		return bal.Cents(), nil
	})
	// The DEBIT is exact: the module emits the cost as a decimal-USD string, parsed
	// here to 18-decimal USD (1e-18) so a sub-cent call bills precisely and is never floored.
	aiobject.SetUsageRecorder(func(ctx context.Context, u aiobject.UsageEvent) error {
		amt, err := money.ParseUSD(u.USD)
		if err != nil {
			return err
		}
		return fin.RecordUsage(ctx, types.UsageInput{
			Org: u.Namespace, Subject: u.Subject, Amount: amt,
			Currency: u.Currency, Model: u.Model, Provider: u.Provider, RequestID: u.RequestID,
		})
	})
	deps.Logger.Info("ai prepaid gate wired to the in-process finance ledger (per-subject wallet, 18-decimal-exact, fail-closed)")
}

// dialTasks opens a client on the process's embedded tasks engine — the ONE durable
// queue — for the module's ai-ingest workflows. The engine binds loopback and shares
// cloud's trust boundary, so this dial is ungated; data isolation lives in the
// workflow INPUT (owner-scoped), which is why every org enqueues into the engine's
// always-registered `default` namespace rather than a per-org one (the embedded
// engine registers no others, and dialing an unregistered namespace makes the worker
// poll forever and silently forces ingest back inline).
//
// Before the engine is up — and if it failed to embed at all — this reports the
// module's own ErrTasksNotConfigured, which its ingest handler answers by running
// the ingest inline. That is the same fail-soft path an uninstalled dialer takes, so
// the durable plane is opt-in-by-availability and never a hard dependency.
func dialTasks(string) (tasksclient.Client, error) {
	emb := cloud.EmbeddedTasks()
	if emb == nil {
		return nil, aiobject.ErrTasksNotConfigured
	}
	return tasksclient.Dial(tasksclient.Options{
		HostPort:  fmt.Sprintf("127.0.0.1:%d", emb.ZAPPort()),
		Namespace: "default",
	})
}
