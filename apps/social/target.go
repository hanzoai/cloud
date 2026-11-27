package social

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// exposeTarget records a connected account on the plane the provider asks
// after a consent. The token stays in the provider. The plane app is one per
// process, so a second mount must not register the op again.
var exposeTargetOnce sync.Once

func exposeTarget() {
	exposeTargetOnce.Do(func() {
		cloud.Plane().Post("/social/targets", recordTarget,
			zip.WithOperationID(client.SocialRecordTarget),
			zip.WithSummary("Record one connected social account for the caller's org"))
		cloud.Plane().Post("/social/targets/forget", forgetTarget,
			zip.WithOperationID(client.SocialForgetTarget),
			zip.WithSummary("Forget one connected social account of the caller's org"))
	})
}

// recordTarget upserts the account the consent named. The same platform id
// updates the row the org already has. A different id is a new account.
func recordTarget(ctx context.Context, in *client.SocialTargetIn) (*client.SocialTargetOut, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok || mounted == nil {
		return nil, zip.ErrForbidden("social: no org on the call")
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if !providers[provider] {
		return nil, zip.ErrBadRequest("unknown provider")
	}
	externalID := strings.TrimSpace(in.ExternalID)
	handle := strings.TrimSpace(in.Handle)
	if handle == "" {
		handle = externalID
	}
	saved, err := mounted.State.store.UpsertTarget(ctx, org, provider, externalID, handle, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record account: %v", err)
	}
	return &client.SocialTargetOut{ID: saved.ID}, nil
}

// forgetTarget removes the account a disconnect ended. Forgetting one that is not
// listed is not an error: the outcome asked for is that it is gone.
func forgetTarget(ctx context.Context, in *client.SocialTargetIn) (*client.SocialTargetOut, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok || mounted == nil {
		return nil, zip.ErrForbidden("social: no org on the call")
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if !providers[provider] {
		return nil, zip.ErrBadRequest("unknown provider")
	}
	externalID := strings.TrimSpace(in.ExternalID)
	if externalID == "" {
		return nil, zip.ErrBadRequest("account has no platform id")
	}
	if err := mounted.State.store.DeleteTarget(ctx, org, provider, externalID); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "forget account: %v", err)
	}
	return &client.SocialTargetOut{ID: idFor(org, provider, externalID)}, nil
}
