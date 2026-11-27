// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// What the durable cron engine believes, published for collection.
//
// Moving the platform's cron off Kubernetes CronJobs took the signal with it.
// A CronJob that stops firing leaves kube_cronjob_status_last_schedule_time
// behind and kube-state-metrics keeps publishing it, so "this has not run"
// stayed answerable by accident. The durable engine keeps a better record —
// and kept it entirely to itself. Nothing left the process, so a schedule that
// silently stopped firing was invisible to every rule in the estate.
//
// THE MISS IS MEASURED AGAINST THE ENGINE'S OWN PROMISE, not against a cron
// expression parsed a second time here. The engine publishes NextActionTime:
// the instant it intends to fire, re-anchored on every fire. So a miss is
// simply `now` well past a NextActionTime that never moved — true for a
// stalled sweeper, a wedged worker, an unregistered queue and a crashed
// process alike, without this file knowing what "*/5 * * * *" means. Re-deriving
// the schedule here would be a second implementation of the thing being
// checked, and it would agree with the engine exactly when it did not matter.
//
// These are OBSERVABLE gauges: schedule state is a fact that is true at
// collection time, not an event to be tracked.

package cron

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/fault"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName matches the process meter so cron's series sit beside cloud's own.
const meterName = "github.com/hanzoai/cloud"

// scheduleSource is the engine view the gauges read. It is set once cron is
// live; until then the callbacks observe nothing, which is correct — a process
// that has not started cron must not claim its schedules are late.
var (
	sourceMu     sync.RWMutex
	scheduleView *tasksengine.View
	metricsOnce  sync.Once
)

// publishScheduleMetrics points the gauges at a live engine view and registers
// them once. Called from start() after the worker is up.
func publishScheduleMetrics(v tasksengine.View) {
	sourceMu.Lock()
	scheduleView = &v
	sourceMu.Unlock()
	metricsOnce.Do(registerScheduleGauges)
}

// view returns the live engine view, or nil before cron is up.
func view() *tasksengine.View {
	sourceMu.RLock()
	defer sourceMu.RUnlock()
	return scheduleView
}

func registerScheduleGauges() {
	m := otel.Meter(meterName)

	next, nerr := m.Float64ObservableGauge("hanzo_cron_next_action_timestamp_seconds",
		metric.WithDescription("Unix time the engine intends to fire this schedule next. Now well past it means a MISS."))
	last, lerr := m.Float64ObservableGauge("hanzo_cron_last_action_timestamp_seconds",
		metric.WithDescription("Unix time this schedule last fired (a start, not an outcome)."))
	fires, ferr := m.Int64ObservableGauge("hanzo_cron_action_count",
		metric.WithDescription("Fires this schedule has started since it was created."))
	fails, xerr := m.Int64ObservableGauge("hanzo_cron_consecutive_failures",
		metric.WithDescription("Consecutive failed runs for this schedule. 1 is a blip; a streak is the incident."))
	if nerr != nil || lerr != nil || ferr != nil || xerr != nil {
		return
	}

	_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		v := view()
		if v == nil {
			return nil
		}
		schedules, err := v.ListSchedules(namespace)
		if err != nil {
			// A view that cannot be read is not a fleet of on-time schedules.
			// Observing nothing lets the series go stale, which the staleness
			// rule reports — far better than observing a reassuring zero.
			return nil
		}
		for _, s := range schedules {
			attrs := metric.WithAttributes(attribute.String("entry", s.ScheduleId))
			if t, ok := stamp(s.Info.NextActionTime); ok {
				o.ObserveFloat64(next, t, attrs)
			}
			if t, ok := stamp(s.Info.UpdateTime); ok {
				o.ObserveFloat64(last, t, attrs)
			}
			o.ObserveInt64(fires, s.Info.ActionCount, attrs)
		}
		// Failure streaks are keyed by ScheduleId too, so a schedule that fires
		// on time and fails every time is a different alert from one that stopped
		// firing — two failure modes a single "last run" number cannot separate.
		streaks, err := v.FailureStreaks(namespace)
		if err != nil {
			return nil
		}
		for _, f := range streaks {
			if f.ScheduleId == "" {
				continue
			}
			o.ObserveInt64(fails, f.ConsecutiveFailures,
				metric.WithAttributes(attribute.String("entry", f.ScheduleId)))
		}
		return nil
	}, next, last, fires, fails)
}

// stamp parses an engine RFC3339 timestamp into unix seconds. An unparseable or
// empty value is reported as absent rather than as zero: zero is 1970, which
// every staleness rule would read as fifty years late.
// runs counts every run a fire started, by entry and outcome ("ok", "failed",
// or "elsewhere": the fire reached a process that does not hold cron, and the
// engine retries it until the one that does runs it). The schedule gauges say a fire STARTED; this says whether it did
// anything, which is the half that went unreported while the nightly backups
// produced nothing.
var runs = sync.OnceValue(func() metric.Int64Counter {
	c, _ := otel.Meter(meterName).Int64Counter("hanzo_cron_runs_total",
		metric.WithDescription("Runs a cron fire started, by entry and outcome (ok, failed, or elsewhere)."))
	return c
})

// report states one run's outcome: a line naming the entry, what it made (the
// Job, or the poke's target) and how it ended, and the runs counter. A run that
// fails is an error line every time — a broken nightly entry fails once a
// night, so there is nothing to throttle.
func report(kind, entry, made string, err error) {
	outcome := "ok"
	switch {
	case errors.Is(err, coord.ErrNotHeld):
		outcome = "elsewhere"
	case err != nil:
		outcome = "failed"
	}
	if c := runs(); c != nil {
		c.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("entry", idPrefix+entry), attribute.String("outcome", outcome)))
	}
	log := activityLog()
	switch outcome {
	case "elsewhere":
		log.Warn("cron: this process does not hold cron; the run is retried", "entry", entry, "kind", kind, "err", err)
		return
	case "failed":
		log.Error("cron: run failed", "entry", entry, "kind", kind, "made", made, "err", err)
		return
	}
	log.Info("cron: run finished", "entry", entry, "kind", kind, "made", made)
}

// statusMap is the ConfigMap that holds each entry's last run, so anyone who can
// read a ConfigMap in this namespace, Hanzo CD included, can see whether an entry
// works and why it does not. The engine's own record (the run's failure in the Tasks
// console, the "cron: run failed" line) is readable only by a platform administrator,
// and the Job and its pods are reaped an hour after they end: two backups failed every
// night for over two weeks with no reason anyone outside the cluster could read
// (hanzo-inc/cloud#828). universe declares the ConfigMap empty and grants cron
// get and patch on that one name.
//
// Each entry has two keys, both a runStatus as JSON: cron-<entry>, its last run,
// and cron-<entry>.failed, its last failure, which a later success leaves alone.
const statusMap = "cron-status"

// statusErrBytes bounds a run's error in statusMap. jobFailure is already at most
// whyBytes; this bounds everything else a run can fail with.
const statusErrBytes = 2000

// runStatus is one run as statusMap holds it.
type runStatus struct {
	At     string `json:"at"`             // when the run ended, RFC 3339 UTC
	Kind   string `json:"kind"`           // job or poke
	Result string `json:"result"`         // ok or failed
	Made   string `json:"made,omitempty"` // the Job, or the poke's target
	Error  string `json:"error,omitempty"`
}

// record writes one run into statusMap. A run is never failed or retried for it:
// a status that cannot be written is a warning, and the run's own answer stands. A
// run this process did not hold is not written; the process that holds cron writes it.
func record(ctx context.Context, kind, entry, made string, err error) {
	if errors.Is(err, coord.ErrNotHeld) {
		return
	}
	s := runStatus{At: time.Now().UTC().Format(time.RFC3339), Kind: kind, Result: "ok", Made: made}
	if err != nil {
		s.Result = "failed"
		s.Error = clipError(err.Error())
		cloud.ReportFault(fault.Failed(entry, err))
	}
	b, jerr := json.Marshal(s)
	if jerr != nil {
		return
	}
	key := idPrefix + entry
	data := map[string]string{key: string(b)}
	if err != nil {
		data[key+".failed"] = string(b)
	}
	// The run may have ended because its context did; the record still goes.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if werr := activityKube().recordRun(wctx, data); werr != nil {
		activityLog().Warn("cron: the run is not in "+statusMap, "entry", entry, "err", werr)
	}
}

// clipError keeps an error to statusErrBytes of valid UTF-8 on one line.
func clipError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > statusErrBytes {
		s = strings.ToValidUTF8(s[:statusErrBytes], "") + " …"
	}
	return s
}

func stamp(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, false
	}
	return float64(t.Unix()), true
}
