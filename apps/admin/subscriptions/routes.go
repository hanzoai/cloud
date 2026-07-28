package subscriptions

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import "github.com/zap-proto/zip"

// Routes registers the fleet subscription view (SuperAdmin only, cross-tenant).
func Routes(z *zip.App) {
	zip.Get(z, "/v1/admin/subscriptions", Subscriptions, zip.WithOperationID("adminSubscriptions"))
}
