package plugin

import (
	"context"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

const (
	statusOK  = "ok"
	statusErr = "error"
)

type creds struct {
	Cookie string
	Auth   string
}

func callerCreds(c *zip.Ctx) creds {
	return creds{
		Cookie: string(c.Fiber().Request().Header.Peek("Cookie")),
		Auth:   c.Header("Authorization"),
	}
}

func admitSuper(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !principal.Super(c) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	return c, nil
}

func admitChange(ctx context.Context) (*zip.Ctx, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	return admitSuper(ctx)
}
