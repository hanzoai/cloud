package social

import (
	"context"
	"os"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// scheduler.go is the in-process scheduler: a periodic goroutine that advances every
// org's `scheduled → published` posts whose time has arrived, by invoking the ONE
// publish path (publishPost). It is the native, in-binary equivalent of the live stack's
// Temporal timer + hourly missing-post safety-net poller (github.com/hanzoai/social
// orchestrator) — folded to a single self-contained sweep, no external scheduler, no
// Redis, no Temporal. It mirrors clients/commerce/sweep.go (ticker + idempotent stop).

const (
	// schedulerIntervalEnv overrides the sweep cadence (Go duration; "0"/"off"/"false"
	// disables). Default is a tight cadence — scheduling granularity, not a money loop.
	schedulerIntervalEnv     = "CLOUD_SOCIAL_SCHEDULER_INTERVAL"
	defaultSchedulerInterval = 30 * time.Second

	// schedulerBatch bounds how many due posts one tick dispatches, so a large backlog
	// drains over several ticks rather than blocking a single tick unboundedly.
	schedulerBatch = 100

	// publishTimeout bounds one post's publish attempt inside a tick (a slow provider
	// edge never wedges the sweep).
	publishTimeout = 30 * time.Second
)

// schedulerInterval resolves the sweep cadence from env. "0"/"off"/"false" disables; an
// unparseable value disables (fail-safe — never silently pick a surprising cadence).
func schedulerInterval(log luxlog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv(schedulerIntervalEnv))
	switch raw {
	case "":
		return defaultSchedulerInterval
	case "0", "off", "false":
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Warn("invalid "+schedulerIntervalEnv+" — social scheduler disabled", "value", raw, "err", err)
		return 0
	}
	return d
}

// startScheduler launches the periodic due-post sweep and returns its stop function
// (never nil, idempotent). Because social is mounted only on the single-writer cloud pod,
// exactly one scheduler exists per deployment — the same single-writer guarantee the live
// stack's single Temporal worker gave.
func startScheduler(s *cloud.Service[state]) func() {
	interval := schedulerInterval(s.Log)
	if interval == 0 {
		s.Log.Info("social scheduler disabled", "env", schedulerIntervalEnv)
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				sweepDue(s)
			}
		}
	}()
	s.Log.Info("social scheduler started", "interval", interval, "batch", schedulerBatch)

	// stop signals the loop AND waits for an in-flight sweep to finish, so Shutdown never
	// closes the store out from under a running publish. Idempotent.
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		close(done)
		<-stopped
	}
}

// sweepDue publishes every due scheduled post (across orgs), one at a time, through the
// org-scoped publish path. A single post's failure never aborts the sweep — it is
// recorded on that post (publishPost) and the sweep moves on. A failed post drops out of
// 'scheduled', so it is never re-swept in a loop (a user retries it explicitly).
func sweepDue(s *cloud.Service[state]) {
	due, err := s.State.store.DueScheduled(context.Background(), time.Now().Unix(), schedulerBatch)
	if err != nil {
		s.Log.Error("social scheduler: due query failed", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	published, failed := 0, 0
	for _, d := range due {
		ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
		post, perr := publishPost(ctx, s, d.Org, d.ID)
		cancel()
		if perr == nil && post.Status == statusPublished {
			published++
			continue
		}
		failed++ // not-configured / provider failure / vanished — recorded on the post
	}
	s.Log.Info("social scheduler sweep", "due", len(due), "published", published, "failed", failed)
}
