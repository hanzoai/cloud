package ingress

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	luxlog "github.com/luxfi/log"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// leStagingDirectory is Let's Encrypt's staging ACME endpoint — untrusted certs
// but no rate limits, used for dev/tests (CLOUD_INGRESS_ACME_STAGING=true). The
// production endpoint is autocert's default (acme.LetsEncryptURL).
const leStagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

// edgeConfig is the deployment-level ACME/listener config, resolved from env at
// edge start. It is the ONLY part of TLS that is process-lifetime; which hosts get
// certs (route.tls + tls.extraHosts) is runtime, via the Engine's TLSHost set.
type edgeConfig struct {
	httpAddr  string // :80 — ACME HTTP-01 + edge router fallback
	httpsAddr string // :443 — SNI TLS termination + edge router
	cacheDir  string // autocert cert cache directory
	email     string // ACME account contact
	staging   bool   // use LE staging directory
}

// Edge is the cloud-in-ingress-role DATA plane: the net/http listeners that
// terminate TLS (ACME certs via autocert) and reverse-proxy by Host through the
// Engine. It is orthogonal to the zip control plane — /v1/ingress CONFIGURES the
// edge; the Edge SERVES it. It is started only when the ingress role is enabled;
// in app role the listeners never bind and cloud stays a pure application.
type Edge struct {
	mgr      *autocert.Manager
	httpSrv  *http.Server
	httpsSrv *http.Server
	log      luxlog.Logger
}

func newEdge(engine *Engine, cfg edgeConfig, log luxlog.Logger) *Edge {
	mgr := &autocert.Manager{
		Cache:  autocert.DirCache(cfg.cacheDir),
		Prompt: autocert.AcceptTOS,
		Email:  cfg.email,
		// Certs are issued ONLY for hosts the live config marks for TLS — never for
		// arbitrary SNI. The predicate reads the Engine's hot-swapped snapshot, so
		// adding a TLS route enables issuance for its host with no edge restart.
		HostPolicy: func(_ context.Context, host string) error {
			if engine.TLSHost(host) {
				return nil
			}
			return fmt.Errorf("ingress: host %q not configured for TLS", host)
		},
	}
	if cfg.staging {
		mgr.Client = &acme.Client{DirectoryURL: leStagingDirectory}
	}

	tlsCfg := &tls.Config{
		GetCertificate: mgr.GetCertificate,
		// acme.ALPNProto enables the tls-alpn-01 challenge over the same :443
		// listener; h2/http1 are the served protocols.
		NextProtos: []string{"h2", "http/1.1", acme.ALPNProto},
		MinVersion: tls.VersionTLS12,
	}

	return &Edge{
		mgr: mgr,
		// :80 serves ACME HTTP-01 challenges; everything else falls through to the
		// edge router, so plain-HTTP routes work and a redirectScheme middleware
		// can force TLS where the operator configures it.
		httpSrv: &http.Server{
			Addr:              cfg.httpAddr,
			Handler:           mgr.HTTPHandler(engine),
			ReadHeaderTimeout: 10 * time.Second,
		},
		// :443 terminates TLS (SNI → autocert) then hands to the same edge router.
		httpsSrv: &http.Server{
			Addr:              cfg.httpsAddr,
			Handler:           engine,
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: 10 * time.Second,
		},
		log: log,
	}
}

// start binds both listeners in the background. A listener error other than a
// graceful shutdown is logged (the process keeps running — the control plane and
// the other listener stay up).
func (e *Edge) start() {
	go func() {
		e.log.Info("ingress edge HTTP listener", "addr", e.httpSrv.Addr)
		if err := e.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			e.log.Error("ingress edge HTTP listener stopped", "err", err)
		}
	}()
	go func() {
		e.log.Info("ingress edge HTTPS listener", "addr", e.httpsSrv.Addr)
		// Certs come from TLSConfig.GetCertificate (autocert), so the cert/key file
		// args are empty.
		if err := e.httpsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			e.log.Error("ingress edge HTTPS listener stopped", "err", err)
		}
	}()
}

// stop gracefully drains both listeners within ctx's deadline.
func (e *Edge) stop(ctx context.Context) error {
	var first error
	if err := e.httpSrv.Shutdown(ctx); err != nil {
		first = err
	}
	if err := e.httpsSrv.Shutdown(ctx); err != nil && first == nil {
		first = err
	}
	return first
}
