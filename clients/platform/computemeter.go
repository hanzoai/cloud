package platform

// Usage-native metering for RUNNING deployments — the last wire in the OSS
// compute-royalty loop. The build meter (buildmeter.go) prices the one-shot build
// Job; THIS meter prices the ongoing thing a build produces: a `live` app that runs
// in-cluster hour after hour. Each tick it charges every live app's own org for the
// compute it consumed since the last tick, at the app's SBOM compute rate
// (blueprint.EstimateService → the SAME rate card the console shows and the blueprint
// estimator uses). The debit lands on the org's commerce ledger through the shared
// cloud.ResourceMeter — the identical spine resource_billing and the build meter use
// — so the org's metered spend now includes real running-deployment compute.
//
// Why that closes the loop: the author-royalty sweep (clients/authors) accrues 20%
// of a deploying org's METERED spend to the authors whose repos that org deployed.
// Until now a running deployment added nothing to that spend (only build minutes
// were metered, once), so the royalty had no compute basis. With this meter the
// org's spend carries the running compute, and the sweep automatically takes its 20%
// — creator/treasury paid, no code in this file that knows anything about royalties.
// Metering the org for its OWN app's compute is correct (it is their compute); the
// self-deploy exclusion that prevents a self-royalty lives entirely in the sweep.
//
// Idempotent + at-most-once per (app, span): each app carries a compute watermark
// (platform_apps.compute_metered_at) — the second through which it has been billed.
// A tick charges (now − watermark) and compare-and-set advances the watermark in the
// SAME store, so a double-tick or a restart never double-charge; a crash after
// advancing at worst drops one span (never bills it twice). The watermark is reset to
// `now` on every transition INTO live (FinalizeLive + start), so a stopped/redeployed
// app is never billed for the gap it was not running.
//
// Single-writer: started next to the build reconciler under the same cancel context
// (Mount, when the cluster client resolved), so exactly one sweeper runs per
// deployment — the same guarantee the build meter already relies on.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/blueprint"
	"github.com/hanzoai/cloud/clients/metering"
)

const (
	// computeMeterIntervalEnv overrides the sweep cadence (a Go duration;
	// "0"/"off"/"false" disables it, e.g. to drive it manually in a test).
	// Default is hourly: the charge is (now − watermark), so cadence only controls
	// freshness, never the amount — a longer interval bills the same total in fewer,
	// larger debits.
	computeMeterIntervalEnv     = "CLOUD_PLATFORM_COMPUTE_INTERVAL"
	defaultComputeMeterInterval = time.Hour

	// minComputeMeterSecs is the smallest span a tick will bill. A sub-threshold
	// sliver is left on the watermark and folded into the next tick (never lost), and
	// it collapses a rapid double-tick to a single charge. Well under the hourly
	// default, so a normal tick always crosses it.
	minComputeMeterSecs int64 = 60

	// secsPerHour converts the per-hour rate card to a per-second charge.
	secsPerHour int64 = 3600
)

// computeMeterInterval resolves the cadence from env. "0"/"off"/"false" disables; an
// unparseable value disables (fail-safe — never silently pick a surprising cadence).
// Mirrors the authors scheduler's knob exactly.
func computeMeterInterval(log luxlog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv(computeMeterIntervalEnv))
	switch raw {
	case "":
		return defaultComputeMeterInterval
	case "0", "off", "false":
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Warn("invalid "+computeMeterIntervalEnv+" — platform compute meter disabled", "value", raw, "err", err)
		return 0
	}
	return d
}

// computeMicros prices an elapsed live span at a per-hour microdollar rate, integer-
// exact with half-up rounding: micros = round(ratePerHour × elapsedSecs / 3600). A
// non-positive span or rate yields 0 (→ no debit). Mirrors buildMinutesCents.
func computeMicros(microPerHour, elapsedSecs int64) int64 {
	if microPerHour <= 0 || elapsedSecs <= 0 {
		return 0
	}
	return (microPerHour*elapsedSecs + secsPerHour/2) / secsPerHour
}

// runningImage is the image ref used to classify an app's footprint: the image the
// container actually runs (image apps) or the built image (git apps), falling back
// to the slug so class inference still has a name to sniff before the first build.
func runningImage(a Application) string {
	repo := strings.TrimSpace(a.ImageRepo)
	tag := strings.TrimSpace(a.ImageTag)
	switch {
	case repo != "" && tag != "":
		return repo + ":" + tag
	case repo != "":
		return repo
	case tag != "":
		return tag
	default:
		return strings.TrimSpace(a.Slug)
	}
}

// effReplicas is the guaranteed-running replica count the meter prices: the app's
// fixed replicas, floored by the autoscaling minimum, never below 1.
func effReplicas(a Application) int {
	r := a.Replicas
	if a.MinScale > r {
		r = a.MinScale
	}
	if r < 1 {
		r = 1
	}
	return r
}

// sweepComputeMeter is the testable core of one tick: over every live app it advances
// the watermark and emits the org debit for the span since it was last billed. rate
// prices an app's footprint per hour (µ$); emit records the debit (production: the
// shared ResourceMeter). Returns the number of apps metered. Both rate and emit are
// injected so a test drives the whole selection + idempotency path with a real store
// and a capturing emit — no commerce, no cluster.
func sweepComputeMeter(ctx context.Context, store *Store, now int64, rate func(Application) int64, emit func(org string, u metering.Usage)) (int, error) {
	targets, err := store.RunningApps(ctx)
	if err != nil {
		return 0, err
	}
	metered := 0
	for _, t := range targets {
		r := rate(t.App)
		if r <= 0 {
			continue // free/unpriced footprint → no meter (mirrors the edge gate's price==0 skip)
		}
		// First sight (never billed, or a live app that predates its FinalizeLive
		// stamp at feature rollout): start the clock at now, never back-charge to app
		// creation. The next tick meters forward from here.
		if t.MeteredAt <= 0 {
			if _, err := store.AdvanceComputeMeter(ctx, t.App.ID, t.MeteredAt, now); err != nil {
				return metered, err
			}
			continue
		}
		elapsed := now - t.MeteredAt
		if elapsed < minComputeMeterSecs {
			continue // sub-threshold sliver: leave the watermark so the span folds into the next tick (also the double-tick guard)
		}
		// Advance the watermark BEFORE emitting: on the winning CAS we own this span
		// exactly once. A concurrent/duplicate tick loses the CAS and emits nothing.
		won, err := store.AdvanceComputeMeter(ctx, t.App.ID, t.MeteredAt, now)
		if err != nil {
			return metered, err
		}
		if !won {
			continue
		}
		micros := computeMicros(r, elapsed)
		if micros <= 0 {
			continue
		}
		emit(t.App.Org, metering.Usage{
			AmountMicros: micros,
			Provider:     "compute",
			Service:      "compute",
			Model:        "compute",
			Project:      t.App.ProjectID, // per-scope attribution (issue #70)
			RequestID:    "compute-" + t.App.ID + "-" + strconv.FormatInt(now, 10),
		})
		metered++
	}
	return metered, nil
}

// blueprintRate prices a running app's footprint per hour through the SAME rate card
// the blueprint estimator uses (class inferred from the image, footprint × replicas).
// This is the ONE consumer of blueprint.EstimateService: the SBOM compute rate the
// org is metered on, and the basis the 20% author royalty is taken from.
func blueprintRate(a Application) int64 {
	return blueprint.EstimateService(runningImage(a), effReplicas(a)).MicroUSDPerHour
}

// runComputeMeter is the single-writer ticker loop: each interval it sweeps every
// live app and meters its org for the running compute since the last tick. Stopped by
// cancelling ctx (Shutdown). A disabled interval returns immediately.
func runComputeMeter(s *cloud.Service[state], ctx context.Context) {
	interval := computeMeterInterval(s.Log)
	if interval == 0 {
		s.Log.Info("platform compute meter disabled", "env", computeMeterIntervalEnv)
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	s.Log.Info("platform compute meter started (running-deployment SBOM compute → org spend → 20% author royalty)", "interval", interval)
	emit := func(org string, u metering.Usage) { s.Bill.MeterUsage(org, "compute", u) }
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := sweepComputeMeter(ctx, s.State.store, time.Now().Unix(), blueprintRate, emit)
			if err != nil {
				s.Log.Warn("compute meter sweep failed", "err", err)
			} else if n > 0 {
				s.Log.Info("compute meter swept", "metered", n)
			}
		}
	}
}
