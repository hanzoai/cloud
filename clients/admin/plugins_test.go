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

package admin

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// testSelf is the replica id the harness mounts admin with, so the board's Host is
// asserted against a known value instead of whatever hostname the runner has.
const testSelf = "cloud-0"

// decodeBoard unwraps the /v1 envelope { status, data } into the plugin board.
func decodeBoard(t *testing.T, body []byte) pluginBoard {
	t.Helper()
	var env struct {
		Status string      `json:"status"`
		Msg    string      `json:"msg"`
		Data   pluginBoard `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode plugins envelope: %v (body=%s)", err, body)
	}
	if env.Status != "ok" {
		t.Fatalf("status = %q msg=%q, want ok (body=%s)", env.Status, env.Msg, body)
	}
	return env.Data
}

// TestPlugins_SuperAdminReadsHostTruth proves the route is admitted for a SuperAdmin,
// answers the /v1 envelope, and names the replica that answered. The harness mounts no
// plugins, so this also pins the honest-empty contract: a host that links everything in
// returns an empty list and zeroed counts — never an error, and never a fabricated row.
func TestPlugins_SuperAdminReadsHostTruth(t *testing.T) {
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "http://127.0.0.1:0")

	resp, body := do("GET", "/v1/admin/plugins", adminHdr())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SuperAdmin GET /v1/admin/plugins: got %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	b := decodeBoard(t, body)
	if b.Host != testSelf {
		t.Errorf("Host = %q, want %q — a one-replica answer must name its replica", b.Host, testSelf)
	}
	if b.At.IsZero() {
		t.Error("At is zero: the board must date its own read")
	}
	if b.Total != 0 || b.Running != 0 || b.Down != 0 || b.Crashed != 0 {
		t.Errorf("rollup = %+v, want all zero on a host with no plugins", b)
	}
	// json.Unmarshal of `[]` yields a non-nil empty slice; of `null` it yields nil.
	// The contract is `[]` — a UI that maps over the field must never see null.
	if b.Plugins == nil {
		t.Error("plugins encoded as null; the honest empty answer is []")
	}
}

// TestPlugins_DeniedWithoutSuperAdmin proves the gate. Plugin digests, pids and RSS are
// operational detail, so this route is SuperAdmin-only like every other platform read —
// an anonymous caller and a validated org admin are both refused, and the org admin is
// refused even though they pass the org-scoped tier used by /overview and friends.
//
// (TestGate_DeniesEveryRoute covers the same two callers across the whole surface via
// platformAdminRoutes; this states it for THIS route, at THIS route's threat model.)
func TestPlugins_DeniedWithoutSuperAdmin(t *testing.T) {
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "http://127.0.0.1:0")

	for _, tc := range []struct {
		name string
		hdr  map[string]string
	}{
		{"anonymous", nil},
		// A client that forges an org header still has no validated principal.
		// (A forged X-User-IsAdmin is not tested here because it cannot reach this
		// code: SanitizeIdentity strips it at ingress and re-mints it only for
		// owner == AdminOrg, so the c.IsAdmin() read the gate makes is authoritative.
		// Asserting it at this layer would test the harness, not the gate.)
		{"forged X-Org-Id, no validated user", map[string]string{"X-Org-Id": "victim"}},
		{"validated org admin of an ENABLED white-label tenant", map[string]string{
			"X-Org-Id": "maxpower", "X-User-Id": "maxpower/max",
			"X-User-Email": "max@maxpower.test", "X-User-IsOrgAdmin": "true",
		}},
	} {
		resp, body := do("GET", "/v1/admin/plugins", tc.hdr)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403 (body=%s)", tc.name, resp.StatusCode, body)
		}
	}
}

// TestReadPlugins_ProjectsHostStatus drives the pure projection with the statuses zip
// actually reports, and pins every field the operator board renders — including the two
// derived ones (uptime from Since, crashed from Restarts).
func TestReadPlugins_ProjectsHostStatus(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	ps := []zip.PluginStatus{
		{
			Name: "o11y", Prefix: "/v1/o11y", Prefixes: []string{"/v1/o11y", "/v1/sentry"},
			Source: "url", Version: "cafebabe", Addr: "/run/zip/o11y.sock", PID: 4242,
			Running: true, Since: now.Add(-90 * time.Minute), Reloads: 2, Restarts: 0,
			Usage: zip.Usage{CPU: 30 * time.Second, RSS: 128 << 20, Threads: 9, FDs: 41},
		},
		{
			Name: "search", Prefix: "/v1/search", Prefixes: []string{"/v1/search"},
			Source: "embedded", Running: true, Since: now.Add(-30 * time.Second),
			Reloads: 0, Restarts: 3, PID: 4243,
			Usage: zip.Usage{CPU: 2 * time.Second, RSS: 16 << 20, Threads: 4, FDs: 12},
		},
		{
			// Unloaded, or died with no reload to replace it: its routes stay
			// registered and answer 503. "Deployed but down" is not "not deployed".
			Name: "billing", Prefix: "/v1/billing", Prefixes: []string{"/v1/billing"},
			Source: "path", Running: false, Restarts: 1,
		},
	}

	b := readPlugins(ps, "cloud-2", now)

	if b.Host != "cloud-2" || !b.At.Equal(now) {
		t.Fatalf("host/at = %q/%v, want cloud-2/%v", b.Host, b.At, now)
	}
	if b.Total != 3 || b.Running != 2 || b.Down != 1 || b.Crashed != 2 {
		t.Fatalf("rollup total/running/down/crashed = %d/%d/%d/%d, want 3/2/1/2",
			b.Total, b.Running, b.Down, b.Crashed)
	}

	o := b.Plugins[0]
	if !slices.Equal(o.Prefixes, []string{"/v1/o11y", "/v1/sentry"}) {
		t.Errorf("o11y prefixes = %v, want both subtrees — the blast radius, not just the first", o.Prefixes)
	}
	if o.Version != "cafebabe" || o.Source != "url" || o.PID != 4242 || o.Addr != "/run/zip/o11y.sock" {
		t.Errorf("o11y identity mis-projected: %+v", o)
	}
	if o.UptimeSec != 5400 {
		t.Errorf("o11y uptime = %ds, want 5400 (Since resets on reload — age of what RUNS)", o.UptimeSec)
	}
	if o.CPUSec != 30 || o.RSSBytes != 128<<20 || o.Threads != 9 || o.FDs != 41 {
		t.Errorf("o11y usage mis-projected: %+v", o)
	}
	// Two reloads are DELIBERATE swaps. That is a deploy, not a crash.
	if o.Reloads != 2 || o.Crashed {
		t.Errorf("o11y reloads=%d crashed=%v — reloads must never read as a crash", o.Reloads, o.Crashed)
	}

	s := b.Plugins[1]
	if s.Restarts != 3 || !s.Crashed {
		t.Errorf("search restarts=%d crashed=%v — a supervised resurrection IS a crash", s.Restarts, s.Crashed)
	}
	if s.Version != "" {
		t.Errorf("search version = %q; an embedded plugin has no artifact digest and must report none", s.Version)
	}

	// Down, but still crashed: the plugin died and no reload replaced it. Counting it
	// as healthy because it is not running would hide exactly the outage that matters.
	d := b.Plugins[2]
	if d.Running || !d.Crashed || d.UptimeSec != 0 || d.PID != 0 {
		t.Errorf("billing row mis-projected: %+v", d)
	}
}

// TestReadPlugins_UnknownUptimeIsZero proves an unknown or nonsensical Since never
// becomes a number a reader might trust: a zero Since (nothing running) and a
// future Since (clock skew) both yield 0, never a negative or absurd age.
func TestReadPlugins_UnknownUptimeIsZero(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	b := readPlugins([]zip.PluginStatus{
		{Name: "zero", Prefix: "/v1/zero"},
		{Name: "future", Prefix: "/v1/future", Running: true, Since: now.Add(time.Hour)},
	}, "cloud-0", now)
	for _, r := range b.Plugins {
		if r.UptimeSec != 0 {
			t.Errorf("%s uptime = %d, want 0 for an unknowable age", r.Name, r.UptimeSec)
		}
	}
}

// TestPrefixesOf_NeverEmptyBlastRadius proves the fallback: a host that reports only
// the legacy single Prefix still yields that one subtree, and a plugin with neither
// yields [] rather than null. Reporting an empty blast radius for a plugin that
// clearly serves something would understate exactly what an operator is deciding on.
func TestPrefixesOf_NeverEmptyBlastRadius(t *testing.T) {
	if got := prefixesOf(zip.PluginStatus{Prefix: "/v1/only"}); !slices.Equal(got, []string{"/v1/only"}) {
		t.Errorf("legacy single-prefix status = %v, want [/v1/only]", got)
	}
	both := zip.PluginStatus{Prefix: "/v1/a", Prefixes: []string{"/v1/a", "/v1/b"}}
	if got := prefixesOf(both); !slices.Equal(got, []string{"/v1/a", "/v1/b"}) {
		t.Errorf("multi-prefix status = %v, want both", got)
	}
	if got := prefixesOf(zip.PluginStatus{}); got == nil || len(got) != 0 {
		t.Errorf("empty status = %v, want a non-nil empty slice (never null on the wire)", got)
	}
}
