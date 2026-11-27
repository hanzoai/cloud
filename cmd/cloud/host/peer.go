package host

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/edge"
	"github.com/hanzoai/cloud/sdk/env"
	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// peerScheme is the transport this host serves its peers on: the same router as
// every other listener, behind mutual TLS under the deployment's authority.
const peerScheme = "peer"

// joinDeployment takes this pod's place in the deployment, and this is the only
// process that takes it: the root of the pod, which exists before any child and
// outlives every one. A child the reaper stops when idle must not take the pod out
// of the live set with it, so children read the set and never write it.
//
// It answers the Coord and the listener addresses the pod's peers reach it on:
// none for a pod with no peers to reach it (no KV, no CLOUD_PEERS). Another live
// pod under this pod's id is a refusal; a KV that is down is not — the pod joins
// when it answers.
func joinDeployment(log luxlog.Logger) (*coord.Coord, []string, error) {
	cfg := coord.Env()
	cfg.Root = true
	cfg.Log = log
	c, err := coord.New(cfg)
	if err != nil {
		// The children refuse to serve on the same configuration (cloud.Listen), so
		// the reason is logged here once and the pod answers alone until it is fixed.
		log.Error("coord: this pod cannot take its place in the deployment", "err", err)
		return coord.Alone(), nil, nil
	}
	c.Start(context.Background())
	if err := c.Err(); err != nil {
		return nil, nil, fmt.Errorf("coord: %w", err)
	}
	if !c.Shared() && cfg.Declared < 2 {
		return c, nil, nil
	}
	srv, err := c.ServerTLS()
	if err != nil {
		log.Error("coord: no peer listener; this pod's peers cannot forward to it", "err", err)
		return c, nil, nil
	}
	zip.RegisterTransport(peerScheme, zip.Transport{
		Serve: func(addr string, h fasthttp.RequestHandler) zip.Server {
			return &peerServer{addr: addr, tls: srv, srv: &fasthttp.Server{
				Handler:            h,
				ReadBufferSize:     edge.ReadBufferSize(),
				MaxRequestBodySize: edge.BodyLimit(),
			}}
		},
	})
	return c, []string{peerScheme + "://" + env.Or(coord.PeerListenEnv, coord.DefaultPeerListen)}, nil
}

// watchDeployment halts the host when another pod takes its id while it serves:
// two pods answering to one id each believe they own the same orgs. It exits
// cleanly when ctx is cancelled so its ticker does not outlive the host.
func watchDeployment(ctx context.Context, c *coord.Coord, log luxlog.Logger, halt func(reason string, cause error)) {
	watchDeploymentEvery(ctx, c, 5*time.Second, log, halt)
}

func watchDeploymentEvery(ctx context.Context, c *coord.Coord, interval time.Duration, log luxlog.Logger, halt func(reason string, cause error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Err(); err != nil {
				log.Error("coord: stopping — another live pod holds this pod's id", "err", err)
				halt("deployment_coordinator", err)
				return
			}
		}
	}
}

// peerServer is the peer listener: fasthttp over TLS that demands a certificate
// from the deployment's authority of every caller.
type peerServer struct {
	addr string
	tls  *tls.Config
	srv  *fasthttp.Server
}

func (p *peerServer) ListenAndServe() error {
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return err
	}
	return p.srv.Serve(tls.NewListener(ln, p.tls))
}

func (p *peerServer) Close() error { return p.srv.Shutdown() }
