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
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/o11y/pkg/types/alertmanagertypes"
	amalert "github.com/prometheus/alertmanager/api/v2/restapi/operations/alert"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
)

// exposeFiring publishes the platform's firing alerts on the internal plane. Use calls
// it.
func exposeFiring() {
	cloud.Plane().Post("/o11y/alerts", planeAlerts,
		zip.WithOperationID(client.O11yAlerts),
		zip.WithSummary("The platform's firing alerts"))
}

// planeAlerts answers what the platform's own ruler holds firing: the active alerts
// nobody silenced or inhibited, in the o11y org the fleet's rules live in — the org the
// egresses page as (CLOUD_ALERTS_ORG, hanzo by default). The watchdog is left out: it
// fires forever by design and says only that the ruler is alive.
//
// SuperAdmin only. A process with no alerting runtime answers 503 rather than an empty
// list, because "nothing is firing" and "nothing could be asked" are opposite facts.
func planeAlerts(ctx context.Context, _ *cloud.Unit) (*client.Firing, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	rt := embeddedRuntime
	if rt == nil || rt.Alertmanager == nil || rt.Modules.OrgGetter == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "o11y alerts: no alerting runtime runs in this process")
	}
	name := env.Or(alertsOrgEnv, defaultAlertsOrg)
	org, err := rt.Modules.OrgGetter.GetByName(ctx, name)
	if err != nil || org == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "o11y alerts: the %s org could not be read", name)
	}
	yes, no := true, false
	got, err := rt.Alertmanager.GetAlerts(ctx, org.ID.StringValue(), alertmanagertypes.GettableAlertsParams{
		GetAlertsParams: amalert.GetAlertsParams{Active: &yes, Silenced: &no, Inhibited: &no, Unprocessed: &yes},
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "o11y alerts: the ruler could not be read: %v", err)
	}
	return &client.Firing{Alerts: firing(got)}, nil
}

// firing projects the ruler's alerts, the watchdog dropped, the longest-firing first.
func firing(in alertmanagertypes.DeprecatedGettableAlerts) []client.Fired {
	out := make([]client.Fired, 0, len(in))
	for _, a := range in {
		if a == nil || a.Alert == nil || a.Labels["watchdog"] == "true" {
			continue
		}
		labels := map[string]string{}
		for k, v := range a.Labels {
			if !bookkeeping(string(k)) {
				labels[string(k)] = string(v)
			}
		}
		out = append(out, client.Fired{
			Name:     string(a.Labels["alertname"]),
			Severity: string(a.Labels["severity"]),
			Summary:  string(a.Annotations["summary"]),
			Labels:   client.Fields(labels),
			Since:    a.StartsAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out
}

// bookkeeping reports whether a label is the ruler's own record of the rule rather than
// a fact about what fired.
func bookkeeping(k string) bool {
	switch k {
	case "alertname", "severity", "ruleId", "ruleSource", "threshold.name":
		return true
	}
	return strings.HasPrefix(k, "__")
}
