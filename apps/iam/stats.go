// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

const publicStatsTTL = 5 * time.Minute

// PublicStats holds public, aggregate counts of Hanzo IAM entities. No
// personally identifiable information or per-tenant detail is exposed.
type PublicStats struct {
	// Users is the total number of active Hanzo IAM user accounts.
	Users int64 `json:"users"`
	// Organizations is the total number of Hanzo IAM organizations.
	Organizations int64 `json:"organizations"`
}

// ResponseHeaders advertises public 5-minute edge caching with 1-hour
// stale-while-revalidate so callers (such as forge.hanzo.ai and public
// dashboards) share a single cached copy at the edge.
func (*PublicStats) ResponseHeaders() map[string]string {
	return map[string]string{
		"Cache-Control": "public, max-age=300, s-maxage=300, stale-while-revalidate=3600",
	}
}

var (
	statsMu      sync.Mutex
	statsDB      orm.DB
	statsCached  PublicStats
	statsAt      time.Time
)

func resetPublicStatsCache() {
	statsMu.Lock()
	statsDB = nil
	statsCached = PublicStats{}
	statsAt = time.Time{}
	statsMu.Unlock()
}

// exposeStats registers GET /v1/iam/stats on the public IAM router.
func exposeStats(host cloud.Router) {
	host.Get("/v1/iam/stats", publicStats,
		zip.WithOperationID("iamPublicStats"),
		zip.WithSummary("Public aggregate counts of Hanzo IAM users and organizations"),
		zip.WithTags("iam"),
		zip.WithResponseHeader("Cache-Control"))
}

// publicStats answers with live aggregate counts of active Hanzo IAM users and
// organizations. Results are cached in-process for 5 minutes and carry public
// edge Cache-Control headers. When the identity store is unavailable, it fails
// closed with HTTP 503 rather than returning zeroes.
func publicStats(ctx context.Context, _ *cloud.Unit) (*PublicStats, error) {
	db := DB()
	if db == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "iam: identity store unavailable")
	}

	now := time.Now()
	statsMu.Lock()
	if statsDB == db && !statsAt.IsZero() && now.Sub(statsAt) < publicStatsTTL {
		out := statsCached
		statsMu.Unlock()
		return &out, nil
	}
	statsMu.Unlock()

	totalUsers, err := orm.TypedQuery[model.User](db).Count(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "iam: count users: %v", err)
	}
	// IsDeleted carries `omitempty`, so active rows omit the field altogether.
	// Fetch only rows where the field is present and subtract those marked true
	// in Go, avoiding backend-specific boolean JSON comparison quirks.
	if flagged, derr := orm.TypedQuery[model.User](db).Filter("IsDeleted!=", nil).GetAll(ctx); derr == nil {
		for _, u := range flagged {
			if u != nil && u.IsDeleted {
				totalUsers--
			}
		}
		if totalUsers < 0 {
			totalUsers = 0
		}
	}

	totalOrgs, err := orm.TypedQuery[model.Organization](db).Count(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "iam: count organizations: %v", err)
	}

	out := PublicStats{
		Users:         int64(totalUsers),
		Organizations: int64(totalOrgs),
	}

	statsMu.Lock()
	statsDB = db
	statsCached = out
	statsAt = time.Now()
	statsMu.Unlock()

	return &out, nil
}
