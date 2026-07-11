// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	luxlog "github.com/luxfi/log"
)

// In-process auto-recharge sweep.
//
// Standalone commerce was swept by an external k8s CronJob curling
// POST /v1/billing/auto-recharge/run-all with the COMMERCE_SERVICE_TOKEN
// bearer every 15 minutes. Folding commerce into the cloud binary makes that
// loop (CronJob + curl image + cluster Service + token hop) redundant: the
// binary can invoke its own gin engine. This sweeper dispatches the SAME
// request loopback through the embedded handler — full middleware chain
// (TokenRequired service-token branch → PlatformOnly mint gate → datastore/KMS
// context), byte-identical to the wire path — so retiring the CronJob changes
// no behavior, only removes moving parts.
//
// It runs ONLY where commerce is mounted (the single-writer cloud pod; the
// reader role never mounts commerce), so exactly one sweeper exists per
// deployment — same single-writer guarantee the CronJob's single schedule gave.
const (
	// sweepPath is the money-mint sweep route registered by api/billing
	// handlers.go — PlatformOnly-gated, idempotent per config (charges only
	// below-threshold orgs and stamps LastRechargedAt).
	sweepPath = "/v1/billing/auto-recharge/run-all"

	// sweepIntervalEnv overrides the cadence (Go duration; "0" or "off"
	// disables). Default matches the retired CronJob's */15 schedule.
	sweepIntervalEnv     = "COMMERCE_AUTORECHARGE_INTERVAL"
	defaultSweepInterval = 15 * time.Minute
)

// sweepInterval resolves the cadence from the environment. Returns 0 when the
// sweeper is explicitly disabled or the value is unparseable (fail-safe: a bad
// value must not silently pick a surprising cadence for a card-charging loop).
func sweepInterval(log luxlog.Logger) time.Duration {
	raw := os.Getenv(sweepIntervalEnv)
	switch raw {
	case "":
		return defaultSweepInterval
	case "0", "off", "false":
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Warn("invalid "+sweepIntervalEnv+" — auto-recharge sweep disabled", "value", raw, "err", err)
		return 0
	}
	return d
}

// sweepOnce dispatches one sweep loopback through the embedded commerce
// handler and returns the HTTP status plus the response body.
func sweepOnce(handler http.Handler, token string) (int, []byte) {
	req := httptest.NewRequest(http.MethodPost, sweepPath, bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// startAutoRechargeSweep launches the periodic sweep and returns its stop
// function (never nil). Disabled — returning a no-op stop — when the interval
// is off or COMMERCE_SERVICE_TOKEN is absent (without the token the mint gate
// would 403 every tick; better to say so once at boot than to spam).
//
// The first sweep fires after one FULL interval, not at boot: during a
// rollout the previous sweeper (pod or legacy CronJob) may have just run, and
// auto-recharge is a card-charging loop — never front-run it.
func startAutoRechargeSweep(handler http.Handler, log luxlog.Logger) func() {
	interval := sweepInterval(log)
	if interval == 0 {
		log.Info("auto-recharge sweep disabled", "env", sweepIntervalEnv)
		return func() {}
	}
	token := os.Getenv("COMMERCE_SERVICE_TOKEN")
	if token == "" {
		log.Warn("COMMERCE_SERVICE_TOKEN not set — in-process auto-recharge sweep disabled (mint gate would reject every run)")
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				status, body := sweepOnce(handler, token)
				// The success body is {"charged":N,"orgs":M,...}; surface the
				// counts, not the body — per-org results can carry error detail.
				var out struct {
					Charged int `json:"charged"`
					Orgs    int `json:"orgs"`
				}
				_ = json.Unmarshal(body, &out)
				if status == http.StatusOK {
					log.Info("auto-recharge sweep", "charged", out.Charged, "orgs", out.Orgs)
				} else {
					log.Error("auto-recharge sweep failed", "status", status, "body_bytes", len(body))
				}
			}
		}
	}()
	log.Info("auto-recharge sweep scheduled in-process", "interval", interval, "path", sweepPath)

	var once bool
	return func() {
		if !once {
			once = true
			close(done)
		}
	}
}
