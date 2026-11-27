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

package platform

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/sdk"
)

// withStatus sets a CD Application's sync and health verdicts.
func withStatus(o map[string]any, sync, health string) map[string]any {
	o["status"] = map[string]any{
		"sync":   map[string]any{"status": sync},
		"health": map[string]any{"status": health},
	}
	return o
}

// Production runs as CD Applications and holds no App CRs, so the board — and the
// admin overview that counts it — must carry the Applications, in the board's own
// colours and drift kinds, and only the ones the caller may see.
func TestFleetCarriesCDApplications(t *testing.T) {
	healthy := cdAppObj("hanzo-cloud", "hanzo", "hanzo-platform")
	withStatus(healthy.Object, "Synced", "Healthy")
	behind := cdAppObj("lux-explorer", "lux", "lux-fleet")
	withStatus(behind.Object, "OutOfSync", "Progressing")
	gone := cdAppObj("zoo-node", "zoo", "zoo-fleet")
	withStatus(gone.Object, "Synced", "Missing")
	customer := cdAppObj("acme-shop", "acme", "acme")
	withStatus(customer.Object, "Synced", "Healthy")
	s := fakeCDService(healthy, behind, gone, customer)

	super := fleetPrincipal{Authority: cloud.Authority{Validated: true, Super: true}}
	views, err := fleetViews(s, context.Background(), super, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AppView{}
	for _, v := range views {
		got[v.App] = v
	}
	want := []struct {
		app, org, env, health string
		sev                   DriftSeverity
	}{
		{"hanzo-cloud", "hanzo", "main", "green", SeverityOK},
		{"lux-explorer", "lux", "", "yellow", SeverityYellow},
		{"zoo-node", "zoo", "", "red", SeverityRed},
		{"acme-shop", "acme", "", "green", SeverityOK},
	}
	if len(views) != len(want) {
		t.Fatalf("board holds %d rows, want %d: %+v", len(views), len(want), views)
	}
	for _, w := range want {
		v, ok := got[w.app]
		if !ok {
			t.Fatalf("%s is not on the board", w.app)
		}
		if v.Org != w.org || v.Env != w.env || v.Health != w.health || v.Drift.Severity != w.sev {
			t.Errorf("%s: org=%q env=%q health=%q drift=%q, want %q %q %q %q",
				w.app, v.Org, v.Env, v.Health, v.Drift.Severity, w.org, w.env, w.health, w.sev)
		}
	}

	// An org admin sees its own org's Applications and no other, and never the
	// platform's brand namespaces.
	mine, err := fleetViews(s, context.Background(), orgAdmin("acme"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].App != "acme-shop" {
		t.Fatalf("org admin of acme sees %+v, want only acme-shop", mine)
	}
}
