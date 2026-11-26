// SPDX-License-Identifier: Apache-2.0 OR MIT

package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

type netHTTPContextKey struct{}

// Mount exposes the OpenAI-compatible Enso gateway and stateless scheduling
// HTTP surface on app under /v1. It imports only zip, fasthttp, luxlog, the Go
// standard library, and this package — never its host.
func Mount(app *zip.App, log luxlog.Logger, gw *Gateway) error {
	if app == nil {
		return errors.New("gateway: nil zip app")
	}
	if gw == nil {
		var err error
		gw, err = NewGateway(GatewayConfig{
			GatewayID:    "enso-community-default",
			OperatorName: "Community Default Gateway",
			OperatorType: OperatorTypeCommunity,
			Strategy:     StrategyWeightedSLOScore,
		})
		if err != nil {
			return err
		}
	}

	o := zipOps{gw: gw}
	v1 := app.Group("/v1")
	v1.Get("/models", o.models)
	v1.Post("/chat/completions", o.chatCompletions)

	gwGroup := v1.Group("/gateway")
	gwGroup.Get("/health", o.health)
	gwGroup.Get("/providers", o.providers)
	gwGroup.Post("/quotes", o.quotes)
	gwGroup.Post("/receipts/verify", o.verifyReceipt)
	gwGroup.Get("/export", o.exportBundle)

	if log != nil {
		log.Info("gateway mounted",
			"gatewayId", gw.GatewayID(),
			"operatorType", gw.OperatorType(),
			"protocol", ProtocolVersion,
			"strategy", string(gw.Config().Strategy),
		)
	}
	return nil
}

// NewApp constructs a standalone zip.App with gw mounted via the typed
// operation registry and builds all projections (REST, OpenAPI, MCP, CLI, SDK).
func NewApp(gw *Gateway, log luxlog.Logger) (*zip.App, error) {
	name := "enso-gateway"
	if gw != nil && gw.GatewayID() != "" {
		name = gw.GatewayID()
	}
	app := newBaseZipApp(name, log)
	if err := Mount(app, log, gw); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountWorker registers a WorkerNode's /worker/v1/* protocol operations onto
// app through zip's typed operation registry.
func MountWorker(app *zip.App, w *WorkerNode) error {
	if app == nil {
		return errors.New("gateway: nil zip app")
	}
	if w == nil {
		return errors.New("gateway: nil worker node")
	}
	wg := app.Group("/worker/v1")
	wg.Get("/record", w.opRecord)
	wg.Post("/quote", w.opQuote)
	wg.Post("/execute", w.opExecute)
	return nil
}

// NewWorkerApp constructs a standalone zip.App with w mounted via the typed
// operation registry and builds all projections (REST, OpenAPI, MCP, CLI, SDK).
func NewWorkerApp(w *WorkerNode, log luxlog.Logger) (*zip.App, error) {
	name := "enso-worker"
	if w != nil && w.ProviderID() != "" {
		name = w.ProviderID()
	}
	app := newBaseZipApp(name, log)
	if err := MountWorker(app, w); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountDirectory registers a DirectoryPeerServer's /directory/v1/* protocol
// operations onto app through zip's typed operation registry.
func MountDirectory(app *zip.App, d *DirectoryPeerServer) error {
	if app == nil {
		return errors.New("gateway: nil zip app")
	}
	if d == nil {
		return errors.New("gateway: nil directory peer server")
	}
	dg := app.Group("/directory/v1")
	dg.Get("/workers", d.opListWorkers)
	dg.Post("/workers", d.opRegisterWorker)
	dg.Post("/gossip", d.opGossip)
	dg.Get("/snapshot", d.opSnapshot)
	return nil
}

// NewDirectoryApp constructs a standalone zip.App with d mounted via the typed
// operation registry and builds all projections (REST, OpenAPI, MCP, CLI, SDK).
func NewDirectoryApp(d *DirectoryPeerServer, log luxlog.Logger) (*zip.App, error) {
	name := "enso-directory"
	if d != nil && d.PeerID != "" {
		name = d.PeerID
	}
	app := newBaseZipApp(name, log)
	if err := MountDirectory(app, d); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

func newBaseZipApp(name string, log luxlog.Logger) *zip.App {
	logger := log
	if logger == nil {
		logger = luxlog.NewNoOpLogger()
	}
	app := zip.New(zip.Config{
		AppName:               name,
		Logger:                logger,
		DisableStartupMessage: true,
	})
	app.Use(zip.Handler(func(c *zip.Ctx) error {
		if ctx, ok := c.Fiber().RequestCtx().UserValue(netHTTPContextKey{}).(context.Context); ok && ctx != nil {
			c.SetContext(ctx)
		}
		return c.Next()
	}))
	return app
}

type zipOps struct {
	gw *Gateway
}

type emptyIn struct{}

// HealthResponse summarizes live gateway status, operator identity, and call counts.
type HealthResponse struct {
	Service         string `json:"service"`
	GatewayID       string `json:"gatewayId"`
	OperatorType    string `json:"operatorType"`
	OperatorAddress string `json:"operatorAddress"`
	ProtocolVersion string `json:"protocolVersion"`
	Strategy        string `json:"strategy"`
	Status          string `json:"status"`
	GatewayFeeBps   uint64 `json:"gatewayFeeBps"`
	Calls           int64  `json:"calls"`
}

// ProvidersResponse lists verified workers currently known to this gateway.
type ProvidersResponse struct {
	GatewayID       string         `json:"gatewayId"`
	ProtocolVersion string         `json:"protocolVersion"`
	Providers       []WorkerRecord `json:"providers"`
}

func (o zipOps) health(_ context.Context, _ *emptyIn) (*HealthResponse, error) {
	if o.gw.IsDisabled() {
		return nil, mapError(ErrGatewayDisabled)
	}
	cfg := o.gw.Config()
	return &HealthResponse{
		Service:         "enso-gateway",
		GatewayID:       o.gw.GatewayID(),
		OperatorType:    o.gw.OperatorType(),
		OperatorAddress: o.gw.Address(),
		ProtocolVersion: ProtocolVersion,
		Strategy:        string(cfg.Strategy),
		Status:          "ok",
		GatewayFeeBps:   cfg.GatewayFeeBps,
		Calls:           o.gw.Calls(),
	}, nil
}

func (o zipOps) models(ctx context.Context, _ *emptyIn) (*ModelsListResponse, error) {
	res, err := o.gw.ListModels(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return res, nil
}

func (o zipOps) chatCompletions(ctx context.Context, in *ChatCompletionRequest) (*ChatCompletionResponse, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	res, err := o.gw.ChatCompletions(ctx, *in)
	if err != nil {
		return nil, mapError(err)
	}
	return res, nil
}

func (o zipOps) providers(ctx context.Context, _ *emptyIn) (*ProvidersResponse, error) {
	list, err := o.gw.ListProviders(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return &ProvidersResponse{
		GatewayID:       o.gw.GatewayID(),
		ProtocolVersion: ProtocolVersion,
		Providers:       list,
	}, nil
}

func (o zipOps) quotes(ctx context.Context, in *QuotesQuery) (*QuotesResponse, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	res, err := o.gw.FetchQuotes(ctx, *in)
	if err != nil {
		return nil, mapError(err)
	}
	return res, nil
}

func (o zipOps) verifyReceipt(_ context.Context, in *VerifyReceiptRequest) (*VerifyReceiptResponse, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	res, err := o.gw.VerifyReceipt(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return res, nil
}

func (o zipOps) exportBundle(_ context.Context, _ *emptyIn) (*GatewayExportBundle, error) {
	if o.gw.IsDisabled() {
		return nil, mapError(ErrGatewayDisabled)
	}
	b, err := o.gw.ExportBundle()
	if err != nil {
		return nil, mapError(err)
	}
	return b, nil
}

func httpStatusForError(err error) int {
	switch {
	case errors.Is(err, ErrGatewayDisabled),
		errors.Is(err, ErrNoHealthyProviders),
		errors.Is(err, ErrProviderUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrInvalidSignature),
		errors.Is(err, ErrSignerMismatch):
		return http.StatusUnauthorized
	case errors.Is(err, ErrInvalidQuote),
		errors.Is(err, ErrQuoteExpired),
		errors.Is(err, ErrInvalidReceipt),
		errors.Is(err, ErrOutputHashMismatch),
		errors.Is(err, ErrDirectoryCorrupted):
		return http.StatusUnprocessableEntity
	case errors.Is(err, ErrReplayNonce):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func mapError(err error) error {
	return zip.Errorf(httpStatusForError(err), "%s", err.Error())
}

// NewHTTPHandler builds a standard net/http handler derived directly from
// Mount's typed zip.App operation registry, exposing the identical REST,
// OpenAPI (/.well-known/openapi.json), and MCP (/mcp) projections over real
// TCP/loopback HTTP.
func NewHTTPHandler(gw *Gateway) http.Handler {
	app, err := NewApp(gw, nil)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		})
	}
	return zipHTTPHandler(app)
}

func zipHTTPHandler(app *zip.App) http.Handler {
	handler := app.Fiber().Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var fctx fasthttp.RequestCtx
		fctx.Request.Header.SetMethod(r.Method)
		fctx.Request.SetRequestURI(r.URL.RequestURI())
		if r.Host != "" {
			fctx.Request.SetHost(r.Host)
		}
		for k, vals := range r.Header {
			for _, v := range vals {
				fctx.Request.Header.Add(k, v)
			}
		}
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}
			fctx.Request.SetBody(body)
		}
		fctx.SetUserValue(netHTTPContextKey{}, r.Context())
		handler(&fctx)

		fctx.Response.Header.VisitAll(func(k, v []byte) {
			key := string(k)
			if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Transfer-Encoding") {
				return
			}
			w.Header().Add(key, string(v))
		})
		status := fctx.Response.StatusCode()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(fctx.Response.Body())
	})
}

// StartLoopbackServer launches gw on an ephemeral loopback TCP HTTP server
// backed by the typed zip.App operation registry.
func StartLoopbackServer(gw *Gateway) *httptest.Server {
	return httptest.NewServer(NewHTTPHandler(gw))
}
