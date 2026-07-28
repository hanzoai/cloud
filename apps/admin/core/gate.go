package core

import (
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/zap-proto/zip"
)

// CallerCreds captures the caller's replayed authorization context for the IAM
// fan-out: the raw Cookie header (session model) and the Authorization bearer.
func CallerCreds(c *zip.Ctx) iam.Creds {
	return iam.Creds{
		Cookie: string(c.Fiber().Request().Header.Peek("Cookie")),
		Auth:   c.Header("Authorization"),
	}
}
