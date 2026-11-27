package host

// The dead-card regression: a refusal a handler PROPAGATES must reach the client
// as the status the refusal chose, carrying a code the console can branch on.
// Before ErrorHandler every one of these rendered 500 {"error":"…"} unless the
// handler remembered to call Denied — which is why an unfunded chat drew "something
// went wrong" where a paywall belonged.
//
// They drive real requests through the zip/fiber stack against an app configured
// exactly as Serve configures it.

import (
	"net/http"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// newErrApp serves ONE route that returns err — the propagating handler.
func newErrApp(err error) *zip.App {
	app := zip.New(zip.Config{ErrorHandler: cloud.ErrorHandler})
	app.Raw(http.MethodGet, "/v1/thing", func(c *zip.Ctx) error { return err })
	return app
}
