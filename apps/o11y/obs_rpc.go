// Copyright © 2026 Hanzo AI. MIT License.

package o11y

// obs_rpc.go — the observability plane's claim on the ONE event endpoint,
// published as a plane op.
//
// analytics owns POST /v1/event and its subtree, but THIS process owns the
// Sentry runtime. A plugin is a process, so the package global this used to ride
// (cloud.SetObsErrorIngest) was written here and read as nil in analytics — the
// Sentry alias answered 503 "error ingest not initialized". The endpoint asks
// over the socket instead, exactly as x402 asks commerce to move money.
//
// There were TWO claims here. The other offered every authenticated /v1/event
// body to an LLM-observability sink before the product wire saw it; it is gone
// with the sink (see planesink.go's datastoreSink for why), so the endpoint now
// runs its own wire with no cross-process round-trip in front of it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	module "github.com/hanzoai/o11y"
	"github.com/zap-proto/zip"
)

// exposeObs publishes the claim. Mount calls it.
func exposeObs() {
	zip.Post[client.ObsErrorIn, client.ObsErrorOut](cloud.Plane(), "/obs/error/post", planeObsError,
		zip.WithOperationID(client.ObsErrorPost),
		zip.WithSummary("Relay one Sentry envelope/store request to the o11y runtime"))
}

// planeObsError relays one Sentry-wire request to the runtime and returns its
// answer VERBATIM — a 401 "invalid ingest key" must reach the SDK as a 401, not
// be reshaped into a plane error. The request is rebuilt here rather than
// forwarded as bytes because the runtime is an http.Handler.
func planeObsError(ctx context.Context, in *client.ObsErrorIn) (*client.ObsErrorOut, error) {
	// THE ADDRESS DOES NOT MOVE. The runtime serves its ingest endpoint at the
	// same /v1/event the caller called and a minted DSN spells, so there is
	// nothing to translate — only to ADMIT. IngestWire is the module's own
	// predicate, so the endpoint that answers and the gate that lets a request
	// reach it cannot come to disagree.
	if !module.IngestWire(http.MethodPost, in.Path) {
		return &client.ObsErrorOut{Status: http.StatusNotFound}, nil
	}
	h := runtimeHandler
	if h == nil {
		return &client.ObsErrorOut{Status: http.StatusServiceUnavailable,
			ContentType: "text/plain", Body: []byte("o11y runtime not initialized")}, nil
	}
	u := &url.URL{Path: in.Path}
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
	return &client.ObsErrorOut{
		Status:      res.StatusCode,
		ContentType: res.Header.Get("Content-Type"),
		Body:        rec.Body.Bytes(),
	}, nil
}
