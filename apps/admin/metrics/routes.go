package metrics

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import "github.com/zap-proto/zip"

// Routes registers the SaaS-metrics god-view (SuperAdmin only, cross-tenant business
// aggregate).
func Routes(z *zip.App) {
	zip.Get(z, "/v1/admin/metrics", Metrics, zip.WithOperationID("adminMetrics"))
}
