// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"os"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/commerce/models/catalogentry"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposeCatalogRefresh publishes the scheduled model sync as a plane op. Mount
// calls it.
//
// It is the body of the module's POST /v1/catalog/models/refresh, reached by
// name instead of by URL: the cron app asks it as the platform, so there is no
// bearer to present and no edge to cross. The gate is the same fact that route
// checks — a PLATFORM caller — read off the plane's stated caller rather than a
// header, and it refuses a tenant, because a sync writes every org's upstream
// cost and a tenant running one would be writing the fleet's.
func exposeCatalogRefresh() {
	zip.Post[struct{}, plane.Refreshed](cloud.Plane(), "/commerce/catalog/refresh", planeCatalogRefresh,
		zip.WithOperationID(plane.CatalogRefresh),
		zip.WithSummary("Refresh the model catalog by reading the upstream provider"))
}

// Pulls the upstream model list and lands it through the same upsert the push
// endpoint uses, so the rule that a sync owns cost and an administrator owns
// price holds no matter which door a row came through. An upstream that cannot
// be read is an error and writes NOTHING: a sync that cannot see its source must
// never conclude the source is empty, because that would withdraw every model on
// sale.
func planeCatalogRefresh(ctx context.Context, _ *struct{}) (*plane.Refreshed, error) {
	if cloud.Who(ctx).Org != authz.AdminOrg {
		return nil, zip.ErrForbidden("catalog refresh: the platform runs the sync")
	}
	rows, err := catalogentry.FetchOpenRouter(ctx, os.Getenv("OPENROUTER_API_KEY"))
	if err != nil {
		return nil, zip.Errorf(502, "read the upstream model catalog: %v", err)
	}
	res, err := catalogentry.Refresh(catalogentry.SystemDB(ctx), catalogentry.ServesOpenRouter, rows)
	if err != nil {
		return nil, zip.Errorf(500, "refresh the model catalog: %v", err)
	}
	return &plane.Refreshed{
		Serves: res.Serves, Upstream: res.Upstream, Created: res.Created, Updated: res.Updated,
		Withdrawn: res.Withdrawn, Restored: res.Restored, SyncedAt: res.SyncedAt,
	}, nil
}
