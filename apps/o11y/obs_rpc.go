// Copyright © 2026 Hanzo AI. MIT License.

package o11y

// obs_rpc.go — the observability plane's two claims on the ONE event door,
// published as plane ops.
//
// analytics owns POST /v1/event and its subtree, but THIS process owns the
// LLM-obs sink and the Sentry runtime. A plugin is a process, so the package
// globals these used to ride (cloud.SetObsEventIngest / SetObsErrorIngest) were
// written here and read as nil in analytics — the Sentry alias answered 503
// "error ingest not initialized" and every LLM-obs batch fell silently through
// to the product wire. The door asks over the socket instead, exactly as x402
// asks commerce to move money.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// exposeObs publishes both claims. MountO11y calls it.
func exposeObs() {
	zip.Post[plane.ObsClaimIn, plane.ObsClaimed](cloud.Plane(), "/obs/event/claim", planeObsClaim,
		zip.WithOperationID(plane.ObsEventClaim),
		zip.WithSummary("Offer one /v1/event body to the LLM-obs sink; declines what is not its shape"))
	zip.Post[plane.ObsErrorIn, plane.ObsErrorOut](cloud.Plane(), "/obs/error/post", planeObsError,
		zip.WithOperationID(plane.ObsErrorPost),
		zip.WithSummary("Relay one Sentry envelope/store request to the o11y runtime"))
}

// planeObsClaim offers a body to the LLM-obs sink. Claimed=false is the normal
// answer for a product event and MUST leave the body untouched — the door then
// runs its own wire, so a wrong claim here silently reroutes a tenant's data.
func planeObsClaim(ctx context.Context, in *plane.ObsClaimIn) (*plane.ObsClaimed, error) {
	if eventIngestSink == nil {
		// No sink in this deployment: decline, never error. An absent sink must
		// read as "not mine", so the product wire keeps working.
		return &plane.ObsClaimed{}, nil
	}
	o := ingestOps{sink: eventIngestSink, threshold: blobThreshold(), log: eventIngestLog}
	accepted, dropped, claimed, err := o.claim(ctx, in.Org, in.Body)
	if err != nil {
		return nil, err
	}
	return &plane.ObsClaimed{Accepted: accepted, Dropped: dropped, Claimed: claimed}, nil
}

// planeObsError relays one Sentry-wire request to the runtime and returns its
// answer VERBATIM — a 401 "invalid ingest key" must reach the SDK as a 401, not
// be reshaped into a plane error. The request is rebuilt here rather than
// forwarded as bytes because the runtime is an http.Handler.
func planeObsError(ctx context.Context, in *plane.ObsErrorIn) (*plane.ObsErrorOut, error) {
	mapped, ok := eventToRuntimePath(http.MethodPost, in.Path)
	if !ok {
		return &plane.ObsErrorOut{Status: http.StatusNotFound}, nil
	}
	h := runtimeHandler
	if h == nil {
		return &plane.ObsErrorOut{Status: http.StatusServiceUnavailable,
			ContentType: "text/plain", Body: []byte("o11y runtime not initialized")}, nil
	}
	u := &url.URL{Path: mapped}
	if in.Query != "" {
		u.RawQuery = in.Query
	}
	req := httptest.NewRequest(http.MethodPost, u.String(), strings.NewReader(string(in.Body)))
	req = req.WithContext(ctx)
	for _, hd := range in.Headers {
		req.Header.Set(hd.Name, hd.Value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	return &plane.ObsErrorOut{
		Status:      res.StatusCode,
		ContentType: res.Header.Get("Content-Type"),
		Body:        rec.Body.Bytes(),
	}, nil
}
