package admin

// products.go — GET /v1/admin/products, the fleet workload registry the operator
// Infrastructure board (admin.hanzo.ai) renders: every operator App CR across the platform
// namespaces with its declared vs running image tag, operator-reconciled health/phase, and
// the drift verdict.
//
// SOURCE — reuse, never fork. The inventory is the SAME observation the native PaaS control
// plane already computes for /v1/paas/apps (clients/paas: observeFleet → observeCR →
// drift.go, one k8s dynamic client, one drift model). paas publishes it as an in-process
// seam (paas.CurrentFleet, fleet.go); admin RESOLVES that seam and projects each AppView
// onto the productRow the SPA decodes. There is no second k8s client and no second drift
// definition — the admin board and the PaaS board can never disagree about what the fleet is
// or what "drift" means. When the PaaS plane is not co-resident, or its k8s client did not
// resolve, the registry is honestly empty — never a fabricated row.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/paas"
	"github.com/zap-proto/zip"
)

// products answers GET /v1/admin/products — the workload registry, optionally narrowed by
// ?tier=, ?kind= (operator role), or ?env=. SuperAdmin only (core.Guard on the route).
func products(s *cloud.Service[core.State], c *zip.Ctx) error {
	rows, _, err := fleetProducts(c.Context())
	if err != nil {
		return core.Fail(c, err.Error())
	}
	kind := strings.TrimSpace(c.Query("kind"))
	tier := strings.TrimSpace(c.Query("tier"))
	env := strings.TrimSpace(c.Query("env"))
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
	return core.OKList(c, out, len(out))
}

// productRollup is the fleet count the overview KPIs fold: total observed workloads, how many
// are healthy (green), and how many are drifting.
type productRollup struct{ Total, Active, Drift int }

// fleetProducts observes the platform fleet through the paas seam and projects it onto the
// productRow board shape, returning the rows plus the rollup the overview KPIs read. A nil
// seam (PaaS not co-resident) or an unready k8s client yields an honest-empty registry with a
// nil error, so both the board and the KPIs degrade to empty rather than failing; only a hard
// observation error (e.g. an RBAC denial listing apps.hanzo.ai) surfaces as an error.
func fleetProducts(ctx context.Context) ([]productRow, productRollup, error) {
	fleet := paas.CurrentFleet()
	if fleet == nil {
		return []productRow{}, productRollup{}, nil
	}
	if ok, _ := fleet.Ready(); !ok {
		return []productRow{}, productRollup{}, nil
	}
	views, err := fleet.Observe(ctx)
	if err != nil {
		return nil, productRollup{}, err
	}
	rows := make([]productRow, 0, len(views))
	var roll productRollup
	for _, v := range views {
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
func productFromView(v paas.AppView) productRow {
	return productRow{
		Name:          v.App,
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
		Drift:         v.Drift.Severity != paas.SeverityOK,
		DriftSeverity: string(v.Drift.Severity),
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
func tierOf(v paas.AppView) string {
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
