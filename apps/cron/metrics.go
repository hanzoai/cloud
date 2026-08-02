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
	"sync"
	"time"

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
