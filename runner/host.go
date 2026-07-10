// Host role: the JIT runner daemon for bare-metal workstations.
//
// Polls every org listed in the config for queued workflow jobs whose
// labels are a subset of this host's labels. On match, mints a
// just-in-time runner config via the GitHub Actions API and spawns
// actions-runner with --jitconfig. The runner picks the one job,
// exits, and auto-deregisters server-side.
//
// In addition to the JIT loop, when a control channel is configured
// (Config.Dialer + Config.ControlPlaneAddr, injected by the CLI) the
// host opens a session to the in-cluster controller and heartbeats.
// Tenant configuration delivered over that channel is intended to take
// precedence over the local YAML file — the control plane is the new
// path and the local file is the offline fallback. When no control
// channel is configured (the default), the host runs fully standalone:
// only the local YAML config drives tenant selection.
package runner

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"
)

// RunHost runs the host-role JIT daemon with the given config until ctx
// is cancelled. It mirrors arcd's runOnHost: the JIT poll loop always
// runs; the control channel runs alongside it only when cfg carries a
// Dialer and a ControlPlaneAddr. Failure to reach the control plane
// never stops the local JIT loop — YAML-only mode is a supported
// offline fallback.
func RunHost(ctx context.Context, cfg Config) error {
	c := &cfg
	logger := newHostLogger()

	d, err := NewJITDaemon(c, logger)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)

	// Existing JIT poll loop — unchanged.
	g.Go(func() error { return d.Run(gctx) })

	// Optional: control channel to the in-cluster controller. Best-effort
	// — if the control plane is unreachable, log loudly and keep the local
	// JIT loop running on YAML.
	if c.Dialer != nil && c.ControlPlaneAddr != "" {
		g.Go(func() error {
			runControlChannel(gctx, logger, c)
			return nil
		})
	} else {
		logger.Warn("arctransport.control_plane_unset",
			"effect", "no cluster sync; tenants pulled from local YAML only",
			"hint", "inject Config.Dialer + Config.ControlPlaneAddr to enable the cluster control channel")
	}

	return g.Wait()
}

// runControlChannel maintains the control-channel session to the
// controller. Once connected, sends a heartbeat every 30s and pulls the
// tenant set every 5 minutes.
//
// All errors are logged and retried on the next tick; this loop never
// returns until ctx is cancelled. Failure to reach the control plane
// does NOT stop the host's local JIT loop — running on local YAML config
// is a documented offline fallback.
func runControlChannel(ctx context.Context, logger *slog.Logger, cfg *Config) {
	cc, err := cfg.Dialer(ctx, cfg.ControlPlaneAddr, cfg)
	if err != nil {
		logger.Error("arctransport.dial_failed",
			"error", err.Error(),
			"effect", "control channel disabled; falling back to YAML-only mode")
		return
	}
	if cc == nil {
		logger.Warn("arctransport.control_plane_unavailable",
			"effect", "control channel disabled; falling back to YAML-only mode")
		return
	}
	defer cc.Close()

	// Probe once at start to surface auth problems immediately.
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	_, perr := cc.GetTenants(probeCtx, cfg.HostName, cfg.Labels, runtime.GOARCH, runtime.GOOS)
	probeCancel()
	if perr != nil {
		logger.Warn("arctransport.probe_failed",
			"error", perr.Error(),
			"effect", "will keep retrying every 30s alongside heartbeat")
	} else {
		logger.Info("arctransport.connected", "control_plane", cfg.ControlPlaneAddr)
	}

	hbTicker := time.NewTicker(30 * time.Second)
	tenantTicker := time.NewTicker(5 * time.Minute)
	defer hbTicker.Stop()
	defer tenantTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-hbTicker.C:
			hbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := cc.Heartbeat(hbCtx, cfg.HostName, time.Now().Unix(), Version)
			cancel()
			if err != nil {
				logger.Debug("arctransport.heartbeat_failed", "error", err.Error())
			}
		case <-tenantTicker.C:
			tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := cc.GetTenants(tCtx, cfg.HostName, cfg.Labels, runtime.GOARCH, runtime.GOOS)
			cancel()
			if err != nil {
				logger.Debug("arctransport.tenant_pull_failed", "error", err.Error())
			}
			// Applying pulled tenants to the JIT daemon's poll set is the
			// controller-precedence path: once the precedence rule is
			// settled (cluster over-rides local? union? local wins?), wire
			// it in by promoting the Config poll fields to atomic pointers
			// and swapping under the dedup mutex.
		}
	}
}

// newHostLogger returns a JSON slog logger tagged with build metadata.
// Mirrors the legacy `arcd jit` startup logger so log shape is unchanged
// for fleet operators.
func newHostLogger() *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	l := slog.New(h).With(
		"app", "arcd",
		"version", Version,
		"pid", os.Getpid(),
		"started", time.Now().UTC().Format(time.RFC3339),
	)
	slog.SetDefault(l)
	return l
}
