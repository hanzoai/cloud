package core

import (
	"context"

	"github.com/hanzoai/cloud"
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

// Acting is this request's principal, lifted OFF the request, for the money
// fan-out. [OrgMoney] re-points it at each tenant it reads.
//
// Read ONCE, here, on the handler's own goroutine — the same discipline
// [CallerCreds] already applies to the IAM reads, and for the same reason.
// The identity lives in fasthttp request headers, whose reads mutate a scratch
// buffer on the request, so resolving it inside the fold is a data race on the
// very request that authorized it: /overview, /customers and /revenue each read
// a dozen orgs in parallel. Off the request it is a value, and a value can be
// read by as many goroutines as there are orgs.
func Acting(c *zip.Ctx) context.Context { return cloud.As(c, "") }
