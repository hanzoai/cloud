package auto

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/script"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// exposeProviderRun publishes the in-process connector run on the plane. Use
// calls it.
func exposeProviderRun() {
	cloud.Plane().Post("/auto/provider/run", planeProviderRun,
		zip.WithOperationID(client.AutoProviderRun),
		zip.WithSummary("Run one connector action in-process with a credential the caller resolved"))
}

// planeProviderRun runs one connector action in this process's runtime (script)
// for the CALLER'S org, read off the plane context, with the credential the
// caller resolved and sent. It is how a sibling — knowledge's long-tail sync —
// reaches the runtime without an HTTP hop or a shared secret: the plane is
// already the trust boundary. A failure of the action is an answer (ok false).
func planeProviderRun(ctx context.Context, in *client.ProviderRunIn) (*client.ProviderRunOut, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("provider run: no org on the call")
	}
	if !script.Has(in.Provider) {
		return nil, zip.Errorf(http.StatusNotFound, "unknown provider %q", in.Provider)
	}
	var auth any
	if len(in.Auth) > 0 {
		if err := json.Unmarshal(in.Auth, &auth); err != nil {
			return nil, zip.ErrBadRequest("provider run: auth is not JSON")
		}
	}
	var props map[string]any
	if len(in.Props) > 0 {
		if err := json.Unmarshal(in.Props, &props); err != nil {
			return nil, zip.ErrBadRequest("provider run: props is not a JSON object")
		}
	}
	out, err := script.Run(ctx, org, in.Provider, in.Action, auth, props)
	if err != nil {
		return &client.ProviderRunOut{Error: err.Error()}, nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &client.ProviderRunOut{Ok: true, Output: raw}, nil
}
