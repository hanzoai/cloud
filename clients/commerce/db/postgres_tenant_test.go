// Copyright © 2026 Hanzo AI. MIT License.

package db

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

func nsCtx(ns string) context.Context {
	return nscontext.WithNamespace(context.Background(), ns)
}

// TestPostgresQuery_AlwaysTenantScoped proves the CRIT-2 Postgres property:
// EVERY entity query is scoped to the caller's tenant. buildSQL/buildWhere always
// emit a `tenant_id = $2` predicate bound to the resolved tenant — with or
// without field filters or an ancestor — so a List/Count/Keys/Run can never
// enumerate another org's rows. On the pre-fix schema (PK on id alone, static
// TenantID="system") a per-org read fell through to the shared/system rows.
func TestPostgresQuery_AlwaysTenantScoped(t *testing.T) {
	for _, tenant := range []string{"acme", "beta", ""} {
		name := tenant
		if name == "" {
			name = "empty-default"
		}
		t.Run(name, func(t *testing.T) {
			// Bare query.
			bare := &postgresQuery{kind: "order"}
			sql, args := bare.buildSQL(tenant)
			assertTenantScoped(t, sql, args, tenant)

			// With a field filter — tenant predicate must still be present and the
			// filter must be additive (AND), never replacing the tenant scope.
			filtered := (&postgresQuery{kind: "order"}).
				FilterField("Status", "=", "paid").(*postgresQuery)
			fsql, fargs := filtered.buildSQL(tenant)
			assertTenantScoped(t, fsql, fargs, tenant)
			if !strings.Contains(fsql, "data->>'status'") {
				t.Fatalf("field filter dropped from query: %s", fsql)
			}

			// Count and Keys share buildWhere — same tenant scoping.
			where, wargs := bare.buildWhere(tenant)
			if !strings.Contains(where, "tenant_id = $2") {
				t.Fatalf("buildWhere not tenant-scoped: %q", where)
			}
			if len(wargs) == 0 || wargs[0] != tenant {
				t.Fatalf("buildWhere tenant arg = %v, want %q", wargs, tenant)
			}
		})
	}
}

func assertTenantScoped(t *testing.T, sql string, args []interface{}, tenant string) {
	t.Helper()
	if !strings.Contains(sql, "tenant_id = $2") {
		t.Fatalf("query NOT tenant-scoped (missing `tenant_id = $2`): %s", sql)
	}
	// buildSQL prepends kind as $1, so args = [kind, tenant, ...filters].
	if len(args) < 2 {
		t.Fatalf("expected >=2 args (kind, tenant), got %v", args)
	}
	if args[0] != "order" {
		t.Fatalf("args[0] = %v, want kind \"order\"", args[0])
	}
	if args[1] != tenant {
		t.Fatalf("tenant arg (args[1]) = %v, want %q", args[1], tenant)
	}
}

// TestPostgresTenantFor proves tenantFor resolves the per-request namespace over
// the static config tenant: a namespaced context wins (shared PostgresDB scoping
// by ctx), and absent a namespace it falls back to the instance's configured
// tenant (per-org PostgresDB). Either way a caller can never read tenant "" by
// omission when a namespace is in play.
func TestPostgresTenantFor(t *testing.T) {
	db := &PostgresDB{config: &PostgresDBConfig{TenantID: "system"}}

	// Namespaced context overrides the static config tenant.
	if got := db.tenantFor(nsCtx("acme")); got != "acme" {
		t.Fatalf("tenantFor(ns=acme) = %q, want acme (ctx must win)", got)
	}
	// No namespace ⇒ fall back to the configured tenant (per-org instance).
	if got := db.tenantFor(nsCtx("")); got != "system" {
		t.Fatalf("tenantFor(no-ns) = %q, want system (config fallback)", got)
	}
}
