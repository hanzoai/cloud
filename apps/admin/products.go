package admin

// products.go — GET /v1/admin/products, the fleet workload registry the operator
// Infrastructure board (admin.hanzo.ai) renders: every operator App CR across the platform
// namespaces with its declared vs running image tag, operator-reconciled health/phase, and
// the drift verdict.
//
// SOURCE — reuse, never fork. The inventory is the SAME observation the native PaaS control
// plane already computes for /v1/platform/fleet (clients/platform: observeFleet → observeCR →
// drift.go, one k8s dynamic client, one drift model). paas publishes it as an in-process
// seam (platform.CurrentFleet, fleet.go); admin RESOLVES that seam and projects each AppView
// onto the productRow the SPA decodes. There is no second k8s client and no second drift
// definition — the admin board and the PaaS board can never disagree about what the fleet is
// or what "drift" means. When the PaaS plane is not co-resident, or its k8s client did not
// resolve, the registry is honestly empty — never a fabricated row.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/zap-proto/zip"
)

// products lists the fleet workload registry: every operator App CR across the platform
// namespaces with its declared vs running image tag, reconciled health/phase and drift
// verdict. Optionally narrowed by kind, tier or env, each an exact match.
//
// The rows are the SAME observation /v1/platform/fleet renders — read through the in-process
// platform seam, not a second k8s client — so the two boards can never disagree about what
// the fleet is. A PaaS plane that is not co-resident yields an honestly empty registry,
// never a fabricated row.
//
// Example: {"tier":"data","env":"main"}
// Response: {"status":"ok","msg":"","data":[{"name":"sql","kind":"sql","tier":"data",
// "org":"hanzoai","cluster":"hanzo-k8s","env":"main","namespace":"hanzo","repo":"hanzoai/sql",
// "phase":"Running","declaredTag":"v1.4.2","runningTag":"v1.4.2","latestTag":"","health":"green",
// "drift":false,"driftSeverity":"ok","updated":""}],"data2":1}
func products(ctx context.Context, in *productsIn) (*productsOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	rows, _, err := fleetProducts(ctx, c)
	if err != nil {
		return &productsOut{Status: core.Err, Msg: err.Error()}, nil
	}
	kind := strings.TrimSpace(in.Kind)
	tier := strings.TrimSpace(in.Tier)
	env := strings.TrimSpace(in.Env)
	out := make([]productRow, 0, len(rows))
	for _, r := range rows {
		if kind != "" && r.Kind != kind {
			continue
		}
		if tier != "" && r.Tier != tier {
			continue
		}
		if env != "" && r.Env != env {
			continue
		}
		out = append(out, r)
	}
	return &productsOut{Status: core.OK, Data: out, Data2: core.Total(len(out))}, nil
}

// productRollup is the fleet count the overview KPIs fold: total observed workloads, how many
// are healthy (green), and how many are drifting.
type productRollup struct{ Total, Active, Drift int }

// fleetProducts ASKS the platform app for the operator's fleet view and projects
// it onto the productRow board, returning the rows plus the rollup the overview
// KPIs read.
//
// It used to import apps/platform and call CurrentFleet(), which resolves a
// package global — nil in any binary that does not mount platform, and admin is
// its own binary. So the board rendered empty while linking client-go,
// apimachinery and their applyconfigurations to do it: about 160 packages to
// print strings it never received.
//
// An unmounted or unready observer still yields honest-empty with a nil error —
// "the operator has not observed yet" is a real state, decided on platform's
// side where the observer lives. A platform that cannot be REACHED is an error,
// because an unreachable estate and an empty estate must never look alike.
func fleetProducts(ctx context.Context, c *zip.Ctx) ([]productRow, productRollup, error) {
	reply, err := cloud.Dial("platform").As(c).Call(ctx, "platform.fleet", nil)
	if err != nil {
		return nil, productRollup{}, err
	}
	apps, err := cloud.Apps(reply)
	if err != nil {
		return nil, productRollup{}, err
	}
	rows := make([]productRow, 0, len(apps))
	var roll productRollup
	for _, v := range apps {
		r := productFromView(v)
		rows = append(rows, r)
		roll.Total++
		if r.Health == "green" {
			roll.Active++
		}
		if r.Drift {
			roll.Drift++
		}
	}
	return rows, roll, nil
}

// productFromView projects a paas fleet AppView onto a productRow: the declared/running tags
// + operator-reconciled health/phase verbatim, the drift verdict rolled to a boolean +
// severity, and the derived infra tier for the board's grouping.
func productFromView(v cloud.App) productRow {
	return productRow{
		Name:          v.Name,
		Kind:          v.Role, // the operator's OWN declared class (sql|kv|generic|ingress) or ""
		Tier:          tierOf(v),
		Org:           v.Org,
		Cluster:       v.Cluster,
		Env:           v.Env,
		Namespace:     v.Namespace,
		Repo:          v.Repo,
		Phase:         v.Phase,
		DeclaredTag:   v.DeclaredTag,
		RunningTag:    v.RunningTag,
		LatestTag:     v.LatestTag,
		Health:        healthLabel(v.Health),
		Drift:         v.DriftSeverity != driftOK,
		DriftSeverity: v.DriftSeverity,
		Updated:       "", // the CR carries no per-row reconcile timestamp; observation is live
	}
}

// tierOf classifies a fleet workload into an infra TIER for the operator board's grouping
// (cloud roles · managed-DB ring · edge · external daemons · PaaS deployments · general apps).
//
// HONESTY: this is a cloud-side DERIVATION over REAL fields — the operator App CR's declared
// role, its namespace, and the image-repo family (what the workload actually RUNS) — NOT an
// operator-declared tier. The operator does not label a tier today (only spec.role, and only
// for sql/kv/generic/ingress), so the board groups on this derivation. A declarative
// `hanzo.ai/tier` label on the App CRs would make it authoritative — a universe/operator
// follow-up; until then this stays the single, documented classifier (one place, no fork).
func tierOf(v cloud.App) string {
	// A workload in a tenant namespace is a customer / PaaS deployment, not platform infra.
	// (Today the paas observer scans only the platform namespaces, so this is future-proofing
	// for when the scan federates tenant/other clusters.)
	if isTenantNamespace(v.Namespace) {
		return "paas"
	}
	// The operator's OWN declared role is authoritative when present.
	switch v.Role {
	case "sql", "kv":
		return "data"
	case "ingress":
		return "edge"
	}
	// Otherwise classify by the image-repo family — a real, stable property of the workload.
	switch repoName(v.Registry) {
	case "cloud":
		return "cloud" // the node-specialized cloud binary roles (cloud / cloud-reader / canary)
	case "sql", "kv", "datastore", "vector", "search", "search-fts5", "s3", "registry", "superbase", "base":
		return "data" // the managed-DB / storage ring
	case "ingress", "dns", "static":
		return "edge" // the ingress / DNS / static edge
	case "arcbuild", "o11y", "visor", "livekit", "git", "mpc", "zt", "analytics", "analytics-collector":
		return "daemon" // external daemons (build / observability / realtime / vcs / mpc / zt)
	}
	return "app" // a general platform service (chat, iam, console, engine, …)
}

// driftOK is the operator's "no drift" severity. It is compared as the string it
// arrives as: the board's only question is whether the operator flagged
// anything, so carrying the enum's type across the wire would buy nothing and
// cost the caller the package that declares it.
const driftOK = "ok"

// healthLabel maps the paas health vocabulary ("" ⇒ unknown) onto the ProductHealth the SPA
// decodes (green|yellow|red|unknown) — an unknown health is honest, never a fabricated green.
func healthLabel(h string) string {
	if strings.TrimSpace(h) == "" {
		return "unknown"
	}
	return h
}

// repoName returns the final path segment of an image repository
// (ghcr.io/hanzoai/cloud → cloud; docker.io/getmeili/meilisearch → meilisearch), the family
// key tierOf classifies on.
func repoName(registry string) string {
	registry = strings.TrimSpace(registry)
	if i := strings.LastIndex(registry, "/"); i >= 0 {
		return registry[i+1:]
	}
	return registry
}

// isTenantNamespace reports whether ns is OUTSIDE the platform tier (hanzo/-testnet/-devnet) —
// i.e. a customer / PaaS-tenant namespace. Kept in lockstep with the paas scanOrder.
func isTenantNamespace(ns string) bool {
	switch strings.TrimSpace(ns) {
	case "hanzo", "hanzo-testnet", "hanzo-devnet", "":
		return false
	}
	return true
}
