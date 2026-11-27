// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package o11y

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/o11y/pkg/types/alertmanagertypes"
	"github.com/prometheus/common/model"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

func ruled(name, severity string, since time.Time, labels model.LabelSet) *alertmanagertypes.DeprecatedGettableAlert {
	ls := model.LabelSet{"alertname": model.LabelValue(name), "severity": model.LabelValue(severity), "ruleId": "r1", "__temporality__": "Unspecified"}
	for k, v := range labels {
		ls[k] = v
	}
	return &alertmanagertypes.DeprecatedGettableAlert{Alert: &model.Alert{
		Labels:      ls,
		Annotations: model.LabelSet{"summary": model.LabelValue(name + " fired")},
		StartsAt:    since,
	}}
}

// What reaches the console is what fired, oldest first: the watchdog, which fires
// forever by design, is not an alert, and the ruler's bookkeeping labels are not facts
// about what fired.
func TestFiringDropsTheHeartbeatAndTheBookkeeping(t *testing.T) {
	t0 := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	got := firing(alertmanagertypes.DeprecatedGettableAlerts{
		ruled("HanzoServiceDown", "critical", t0.Add(time.Minute), model.LabelSet{"service": "models"}),
		ruled("HanzoMonitoringWatchdog", "none", t0, model.LabelSet{"watchdog": "true"}),
		ruled("AlertEgressFailing", "critical", t0, nil),
		nil,
	})
	if len(got) != 2 {
		t.Fatalf("firing = %+v, want the two real alerts", got)
	}
	if got[0].Name != "AlertEgressFailing" || got[1].Name != "HanzoServiceDown" {
		t.Fatalf("order = %s, %s; want the longest-firing first", got[0].Name, got[1].Name)
	}
	down := got[1]
	if down.Severity != "critical" || down.Summary != "HanzoServiceDown fired" || down.Since != "2026-10-02T01:01:00Z" {
		t.Errorf("HanzoServiceDown projected as %+v", down)
	}
	if len(down.Labels) != 1 || down.Labels[0] != (client.Field{Name: "service", Value: "models"}) {
		t.Errorf("labels = %v, want only service=models", down.Labels)
	}
}

// Only a SuperAdmin reads the platform's alerts, and a process with no ruler says so
// instead of answering that nothing is firing.
func TestPlaneAlertsIsSuperAdminOnlyAndNeverEmptyByDefault(t *testing.T) {
	if _, err := planeAlerts(context.Background(), &cloud.Unit{}); err == nil {
		t.Fatal("a caller with no identity read the platform's alerts")
	}
	member := zip.WithCaller(context.Background(), zip.Caller{User: "eve", Org: "acme"})
	if _, err := planeAlerts(member, &cloud.Unit{}); err == nil {
		t.Fatal("an org member read the platform's alerts")
	}
	super := zip.WithCaller(context.Background(), zip.Caller{User: "z", Org: "admin", Admin: true})
	held := embeddedRuntime
	embeddedRuntime = nil
	defer func() { embeddedRuntime = held }()
	got, err := planeAlerts(super, &cloud.Unit{})
	if err == nil || got != nil {
		t.Fatalf("with no ruler a SuperAdmin got %+v, %v; want a refusal, not an empty list", got, err)
	}
}
