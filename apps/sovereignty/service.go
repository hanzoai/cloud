// SPDX-License-Identifier: Apache-2.0 OR MIT

package sovereignty

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

type netHTTPContextKey struct{}

// Mount exposes the HIP-1351 full sovereignty and network exit HTTP surface on
// app under /v1/sovereignty. It imports only zip, luxlog, the Go standard
// library, and this package — never its host.
func Mount(app *zip.App, log luxlog.Logger, coord *Coordinator) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if coord == nil {
		coord = NewCoordinator(DefaultDomain())
	}
	o := httpOps{coord: coord}
	g := app.Group("/v1/sovereignty")
	g.Get("/health", o.health)
	g.Post("/drill/run", o.runDrill)
	g.Get("/drill/latest", o.latestDrill)
	g.Post("/regimes/run", o.runRegimes)
	g.Get("/fork/export", o.exportFork)
	g.Post("/fork/verify", o.verifyFork)

	if log != nil {
		dom := coord.Domain()
		log.Info("sovereignty mounted", "protocol", ProtocolVersion, "chainId", dom.ChainID)
	}
	return nil
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

// MountBootstrap registers BootstrapServer's /v1/* JSON operations through
// zip's typed operation registry (REST, OpenAPI, MCP, CLI, SDK).
func MountBootstrap(app *zip.App, b *BootstrapServer) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if b == nil {
		return errors.New("sovereignty: nil bootstrap server")
	}
	v1 := app.Group("/v1")
	v1.Get("/health", b.opHealth)
	v1.Post("/peers/register", b.opRegisterPeer)
	v1.Get("/peers", b.opListPeers)
	v1.Post("/models", b.opRegisterModel)
	v1.Get("/models", b.opListModels)
	v1.Get("/models/:id", b.opGetModel)
	v1.Post("/governance/vote", b.opGovernanceVote)
	v1.Get("/governance/:id", b.opGetGovernance)
	return nil
}

// NewBootstrapApp constructs a standalone zip.App with b mounted via typed operations.
func NewBootstrapApp(b *BootstrapServer, log luxlog.Logger) (*zip.App, error) {
	name := "sovereignty-bootstrap"
	if b != nil && b.ID != "" {
		name = b.ID
	}
	app := newBaseZipApp(name, log)
	if err := MountBootstrap(app, b); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountMirror registers ArtifactMirrorServer's health typed op and content-addressed
// blob transfer routes onto app.
func MountMirror(app *zip.App, a *ArtifactMirrorServer) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if a == nil {
		return errors.New("sovereignty: nil mirror server")
	}
	v1 := app.Group("/v1")
	v1.Get("/health", a.opHealth)
	v1.Raw(http.MethodPut, "/blobs/:digest", a.zipPutBlob)
	v1.Raw(http.MethodGet, "/blobs/:digest", a.zipGetBlob)
	return nil
}

// NewMirrorApp constructs a standalone zip.App with a mounted.
func NewMirrorApp(a *ArtifactMirrorServer, log luxlog.Logger) (*zip.App, error) {
	name := "sovereignty-mirror"
	if a != nil && a.ID != "" {
		name = a.ID
	}
	app := newBaseZipApp(name, log)
	if err := MountMirror(app, a); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountProvider registers ProviderNodeServer's /v1/* JSON operations through
// zip's typed operation registry.
func MountProvider(app *zip.App, p *ProviderNodeServer) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if p == nil {
		return errors.New("sovereignty: nil provider server")
	}
	v1 := app.Group("/v1")
	v1.Get("/health", p.opHealth)
	v1.Post("/infer", p.opInfer)
	return nil
}

// NewProviderApp constructs a standalone zip.App with p mounted via typed operations.
func NewProviderApp(p *ProviderNodeServer, log luxlog.Logger) (*zip.App, error) {
	name := "sovereignty-provider"
	if p != nil && p.ProviderID != "" {
		name = p.ProviderID
	}
	app := newBaseZipApp(name, log)
	if err := MountProvider(app, p); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountScheduler registers SchedulerServer's /v1/* JSON operations through
// zip's typed operation registry.
func MountScheduler(app *zip.App, s *SchedulerServer) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if s == nil {
		return errors.New("sovereignty: nil scheduler server")
	}
	v1 := app.Group("/v1")
	v1.Get("/health", s.opHealth)
	v1.Post("/schedule", s.opSchedule)
	return nil
}

// NewSchedulerApp constructs a standalone zip.App with s mounted via typed operations.
func NewSchedulerApp(s *SchedulerServer, log luxlog.Logger) (*zip.App, error) {
	name := "sovereignty-scheduler"
	if s != nil && s.ID != "" {
		name = s.ID
	}
	app := newBaseZipApp(name, log)
	if err := MountScheduler(app, s); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountFacilitator registers FacilitatorServer's /v1/* JSON operations through
// zip's typed operation registry.
func MountFacilitator(app *zip.App, f *FacilitatorServer) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if f == nil {
		return errors.New("sovereignty: nil facilitator server")
	}
	v1 := app.Group("/v1")
	v1.Get("/health", f.opHealth)
	v1.Post("/payments/mint", f.opMint)
	v1.Post("/payments/settle", f.opSettle)
	v1.Get("/payments/balance/:address", f.opBalance)
	v1.Get("/payments/ledger", f.opLedger)
	return nil
}

// NewFacilitatorApp constructs a standalone zip.App with f mounted via typed operations.
func NewFacilitatorApp(f *FacilitatorServer, log luxlog.Logger) (*zip.App, error) {
	name := "sovereignty-facilitator"
	if f != nil && f.ID != "" {
		name = f.ID
	}
	app := newBaseZipApp(name, log)
	if err := MountFacilitator(app, f); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountVault registers SovereignVaultServer's /v1/* JSON operations through
// zip's typed operation registry.
func MountVault(app *zip.App, v *SovereignVaultServer) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if v == nil {
		return errors.New("sovereignty: nil vault server")
	}
	v1 := app.Group("/v1")
	v1.Get("/health", v.opHealth)
	v1.Post("/vault/snapshots", v.opPutSnapshot)
	v1.Get("/vault/snapshots/:id", v.opGetSnapshot)
	v1.Get("/vault/export", v.opExportAll)
	return nil
}

// NewVaultApp constructs a standalone zip.App with v mounted via typed operations.
func NewVaultApp(v *SovereignVaultServer, log luxlog.Logger) (*zip.App, error) {
	name := "sovereignty-vault"
	if v != nil && v.ID != "" {
		name = v.ID
	}
	app := newBaseZipApp(name, log)
	if err := MountVault(app, v); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

// MountHanzoGateway registers the Hanzo convenience gateway's /v1/health typed op.
func MountHanzoGateway(app *zip.App, hp *HanzoControlPlane) error {
	if app == nil {
		return errors.New("sovereignty: nil app")
	}
	if hp == nil {
		return errors.New("sovereignty: nil hanzo control plane")
	}
	app.Group("/v1").Get("/health", hp.opGatewayHealth)
	return nil
}

// NewHanzoGatewayApp constructs a standalone zip.App for the Hanzo gateway plane.
func NewHanzoGatewayApp(hp *HanzoControlPlane, log luxlog.Logger) (*zip.App, error) {
	app := newBaseZipApp("hanzo-gateway", log)
	if err := MountHanzoGateway(app, hp); err != nil {
		return nil, err
	}
	if err := app.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

type httpOps struct {
	coord *Coordinator
}

type emptyIn struct{}

// HealthOut reports sovereignty harness status and drill history.
type HealthOut struct {
	Service         string `json:"service"`
	ProtocolVersion string `json:"protocolVersion"`
	Status          string `json:"status"`
	ChainID         uint64 `json:"chainId"`
	DrillsCompleted int64  `json:"drillsCompleted"`
}

func (o httpOps) health(_ context.Context, _ *emptyIn) (*HealthOut, error) {
	dom := o.coord.Domain()
	return &HealthOut{
		Service:         "sovereignty",
		ProtocolVersion: ProtocolVersion,
		Status:          "ok",
		ChainID:         dom.ChainID,
		DrillsCompleted: o.coord.RunsCount(),
	}, nil
}

func (o httpOps) runDrill(_ context.Context, _ *emptyIn) (*NetworkExitReport, error) {
	rep, _, err := o.coord.RunDrill()
	if err != nil {
		return nil, mapError(err)
	}
	return rep, nil
}

func (o httpOps) latestDrill(_ context.Context, _ *emptyIn) (*NetworkExitReport, error) {
	rep, ok := o.coord.LatestReport()
	if !ok {
		return nil, mapError(ErrNotFound)
	}
	return rep, nil
}

// RegimesIn configures the number of trials per negative/boundary regime.
type RegimesIn struct {
	Trials int `json:"trials"`
}

// RegimesOut returns all three honest negative and boundary regime results.
type RegimesOut struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Regimes         []NegativeRegimeResult `json:"regimes"`
}

func (o httpOps) runRegimes(_ context.Context, in *RegimesIn) (*RegimesOut, error) {
	trials := 25
	if in != nil && in.Trials > 0 {
		trials = in.Trials
	}
	res, err := RunNegativeAndBoundaryRegimes(o.coord.Domain(), trials)
	if err != nil {
		return nil, mapError(err)
	}
	return &RegimesOut{
		ProtocolVersion: ProtocolVersion,
		Regimes:         res,
	}, nil
}

func (o httpOps) exportFork(_ context.Context, _ *emptyIn) (*NetworkForkBundle, error) {
	fork, ok := o.coord.LatestForkBundle()
	if !ok {
		var err error
		_, fork, err = o.coord.RunDrill()
		if err != nil {
			return nil, mapError(err)
		}
	}
	return fork, nil
}

// ForkVerifyOut summarizes offline cryptographic verification of a NetworkForkBundle.
type ForkVerifyOut struct {
	Valid             bool   `json:"valid"`
	ProtocolVersion   string `json:"protocolVersion"`
	OperatorID        string `json:"operatorId"`
	BundleSHA256      string `json:"bundleSha256"`
	ProviderCount     int    `json:"providerCount"`
	ModelCount        int    `json:"modelCount"`
	LedgerEntries     int    `json:"ledgerEntries"`
	VaultSnapshotSize int    `json:"vaultSnapshotSize"`
}

func (o httpOps) verifyFork(_ context.Context, in *NetworkForkBundle) (*ForkVerifyOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	if err := VerifyForkBundleOffline(o.coord.Domain(), *in); err != nil {
		return nil, mapError(err)
	}
	return &ForkVerifyOut{
		Valid:             true,
		ProtocolVersion:   in.ProtocolVersion,
		OperatorID:        in.OperatorID,
		BundleSHA256:      in.BundleSHA256,
		ProviderCount:     len(in.Providers),
		ModelCount:        len(in.Models),
		LedgerEntries:     len(in.SettlementLedger),
		VaultSnapshotSize: len(in.EncryptedVault),
	}, nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return zip.Errorf(http.StatusNotFound, "%s", err.Error())
	case errors.Is(err, ErrInvalidSignature),
		errors.Is(err, ErrSignerMismatch),
		errors.Is(err, ErrUnauthorizedDataAccess):
		return zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
	case errors.Is(err, ErrForkBundleCorrupted),
		errors.Is(err, ErrModelDigestMismatch),
		errors.Is(err, ErrInvalidReceipt):
		return zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	case errors.Is(err, ErrHanzoHostBlocked),
		errors.Is(err, ErrHanzoPlaneOffline),
		errors.Is(err, ErrBootstrapQuorumLost):
		return zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
	case errors.Is(err, ErrReplayDetected):
		return zip.Errorf(http.StatusConflict, "%s", err.Error())
	case errors.Is(err, ErrInsufficientBalance):
		return zip.Errorf(http.StatusPaymentRequired, "%s", err.Error())
	default:
		return zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
}
