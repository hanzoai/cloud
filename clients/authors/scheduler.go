package authors

import (
	"os"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// scheduler.go is authors' in-process money loop: a periodic goroutine that ACCRUES
// every approved author's royalty for the current period AND AUTO-PAYS their pending
// balance — so the creator payout runs with NO human in the loop. It mirrors
// clients/social/scheduler.go (ticker + idempotent stop) and inherits the same
// single-writer guarantee: authors is mounted only on the single-writer cloud pod, so
// exactly one scheduler exists per deployment. The manual /v1/admin/authors/sweep and
// /v1/admin/authors/:id/payout endpoints remain as operator OVERRIDES; this drives the
// DEFAULT automatic path.

const (
	// schedulerIntervalEnv overrides the accrual+payout cadence (a Go duration;
	// "0"/"off"/"false" disables it, e.g. to drive the loop manually). Default is
	// hourly: accrual is month-to-date and idempotent per period, so an hourly cadence
	// keeps balances fresh and pays authors promptly without hammering commerce.
	schedulerIntervalEnv     = "CLOUD_AUTHORS_SCHEDULER_INTERVAL"
	defaultSchedulerInterval = time.Hour
)

// schedulerInterval resolves the cadence from env. "0"/"off"/"false" disables; an
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
		log.Warn("invalid "+schedulerIntervalEnv+" — authors scheduler disabled", "value", raw, "err", err)
		return 0
	}
	return d
}

// startScheduler launches the periodic accrual+auto-payout loop and returns its stop
// function (never nil, idempotent). The first sweep runs one interval in (the deploy
// path's lazy sweep + recordDeploy already accrue live in the meantime). stop signals
// the loop AND waits for an in-flight sweep to finish, so Shutdown never closes the
// store out from under a running payout.
func startScheduler(s *cloud.Service[state]) func() {
	interval := schedulerInterval(s.Log)
	if interval == 0 {
		s.Log.Info("authors scheduler disabled", "env", schedulerIntervalEnv)
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
				sweepAndPayout(s)
			}
		}
	}()
	s.Log.Info("authors scheduler started (auto accrual + payout)", "interval", interval)

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
