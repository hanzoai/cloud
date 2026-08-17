package main

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// This file is about ONE property, and it is the complement of mount_test.go's.
// That file pins "a subsystem that cannot start must not take the others down".
// This one pins "…and it must not be HIDDEN either".
//
// Written against 2026-08-01. o11y v1.5.41 linked a zapingest service that
// defaulted enabled on :4317-:4319 — the ports cloud's own planesink binds. The
// host won the race, so the `ai` CHILD's listen failed, serve.go made that
// fatal, and mount() degraded `ai` to absent, which is correct. What was not
// correct is what a probe could see: `ai` owns the greedy "/v1", so
// api.hanzo.ai/v1/models and /v1/chat/completions answered
//
//	503 {"error":"mount /v1: no instance running"}
//
// for ~30 minutes while the pod reported Ready with 0 restarts. The reason was
// in /healthz's `absent` FIELD; the probe reads the STATUS CODE. Every
// specifically-mounted prefix (/v1/sentry, /v1/o11y, /v1/commerce/catalog,
// /v1/admin/*) kept answering from its own subsystem, so nothing else showed it.
//
// The tests use the REAL app name "ai" rather than a fixture, because the
// property under test is a claim about THIS fleet's manifest: that the row
// owning "/v1" is the row marked Vital. A fixture would pass while the manifest
// said anything at all.

// TestVitalAbsenceIsNotReadyButStaysAlive is the outage, as a test.
func TestVitalAbsenceIsNotReadyButStaysAlive(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	absent := map[string]string{}

	// The product API, dead exactly as it died: a child that exits before it
	// binds. Mounting it must still succeed — degrading to absent is the fix
	// mount_test.go pins, and this test depends on it.
	if err := mount(app, dead(t, "ai", "/v1"), true, absent); err != nil {
		t.Fatalf("a dead `ai` aborted the host: %v — it must degrade to absent", err)
	}
	if err := mount(app, good(t, "flags", "/v1/flags", `{"flag":"on"}`), false, absent); err != nil {
		t.Fatal(err)
	}
	health(app, absent)

	// 1. LIVENESS STAYS 200. Restarting cannot help: the cause is in the image or
	// the config, so the replacement fails identically — and the restart would
	// take the console, the log stream and every healthy sibling with it.
	code, _, body := do(t, app, "/healthz")
	if code != 200 {
		t.Errorf("GET /healthz = %d, want 200 — liveness must not flap for an absent child, that is a restart loop", code)
	}
	if !strings.Contains(body, "absent") || !strings.Contains(body, "ai") {
		t.Errorf("/healthz body = %q, want the absence and its reason still reported", body)
	}

	// 2. READINESS IS 503. This is the signal that did not exist on 08-01.
	code, ctype, body := do(t, app, "/readyz")
	if code != 503 {
		t.Fatalf("GET /readyz = %d, want 503 — the entire product API is absent and this pod is "+
			"advertising itself as fit to serve it. That is the 30-minute outage: green probe, dead /v1", code)
	}
	if !strings.Contains(ctype, "application/json") {
		t.Errorf("/readyz Content-Type = %q, want JSON", ctype)
	}
	// The code takes the pod out of rotation; the body is what tells an operator
	// WHICH promise broke and why. A bare 503 would just relocate the mystery.
	if !strings.Contains(body, "ai") {
		t.Errorf("/readyz body = %q, want it to name the absent subsystem", body)
	}
	if !strings.Contains(body, "unfit") {
		t.Errorf("/readyz body = %q, want status \"unfit\" — distinguishable from \"draining\"", body)
	}

	// 3. THE SIBLINGS STILL SERVE. NotReady is about routing, not about killing
	// the process: an operator can still reach the console, the logs and every
	// healthy subsystem on this pod.
	code, _, body = do(t, app, "/v1/flags")
	if code != 200 || !strings.Contains(body, `"flag":"on"`) {
		t.Errorf("GET /v1/flags = %d %q, want the healthy sibling still serving on a NotReady pod", code, body)
	}
}

// TestNonVitalAbsenceStaysReady is the guard against overcorrecting. Taking a
// pod out of rotation because one minor subsystem died turns a partial failure
// into a total one — the same mistake as aborting the host, made later.
func TestNonVitalAbsenceStaysReady(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	absent := map[string]string{}

	if err := mount(app, dead(t, "pubsub", "/v1/pubsub"), true, absent); err != nil {
		t.Fatal(err)
	}
	health(app, absent)

	code, _, body := do(t, app, "/readyz")
	if code != 200 {
		t.Errorf("GET /readyz = %d with only a NON-vital subsystem absent, want 200 — "+
			"pulling the pod for pubsub would make a minor failure a total outage", code)
	}
	// Ready, but not silent: the absence is still on the wire, it just is not a
	// reason to stop routing.
	if !strings.Contains(body, "absent") || !strings.Contains(body, "pubsub") {
		t.Errorf("/readyz body = %q, want the non-vital absence still reported", body)
	}
}

// TestAHealthyHostIsReadyAndSaysNothingElse — the negative control. A probe that
// cannot pass is as useless as one that cannot fail.
func TestAHealthyHostIsReadyAndSaysNothingElse(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	absent := map[string]string{}
	if err := mount(app, good(t, "flags", "/v1/flags", `{}`), false, absent); err != nil {
		t.Fatal(err)
	}
	health(app, absent)

	code, _, body := do(t, app, "/readyz")
	if code != 200 {
		t.Errorf("GET /readyz on a healthy host = %d, want 200", code)
	}
	if strings.Contains(body, "absent") || strings.Contains(body, "unfit") {
		t.Errorf("/readyz on a healthy host = %q, want a bare ok", body)
	}
}

// TestDrainingIsNotReady closes the loop drain.go describes and nothing
// delivered. drain.go states the contract — "on SIGTERM the process flips to
// draining and /readyz returns 503, so Kubernetes marks the pod NotReady" — and
// the root package implements it on the ops listener at :9090. The k8s probes
// point at :8000, where /readyz did not exist, so a rolling upgrade never
// observed the drain at all and the M3 writer-election handoff it guards ran on
// a signal nobody read.
func TestDrainingIsNotReady(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	health(app, map[string]string{})

	draining.Store(true)
	t.Cleanup(func() { draining.Store(false) })

	code, _, body := do(t, app, "/readyz")
	if code != 503 {
		t.Errorf("GET /readyz while draining = %d, want 503 — a pod on its way out must stop taking new work", code)
	}
	if !strings.Contains(body, "draining") {
		t.Errorf("/readyz body = %q, want status \"draining\" — an operator must be able to tell "+
			"a planned rollout from a broken subsystem, both of which are 503 here", body)
	}

	// Liveness must NOT flip: the process has to stay up long enough to finish
	// in-flight work, which is the entire point of draining rather than exiting.
	if code, _, _ := do(t, app, "/healthz"); code != 200 {
		t.Errorf("GET /healthz while draining = %d, want 200 — killing a draining pod loses the requests it is finishing", code)
	}
}

// TestAiIsTheOnlyVitalApp is the argued declaration, pinned — the counterpart of
// TestNothingIsRequiredByDefault.
//
// Adding a name here must be a deliberate diff with a reason. The bar is NOT
// Required's ("serving without it is unsafe") but a narrower one: serving
// without it is POINTLESS, because the traffic that arrives will not be
// answered. `ai` qualifies because "/v1" is not a subsystem's prefix, it is the
// product API's remainder. A subsystem owning a named prefix does not: its
// absence 503s its own routes and leaves the other 111 working, which is a
// degraded pod, not an unroutable one.
func TestAiIsTheOnlyVitalApp(t *testing.T) {
	var vital []string
	for _, a := range manifest.Apps {
		if a.Vital {
			vital = append(vital, a.Name)
		}
	}
	if len(vital) != 1 || vital[0] != "ai" {
		t.Errorf("Vital apps = %v, want exactly [ai]. Vital pulls the whole pod out of the "+
			"Service, so each name costs every other subsystem's availability: justify it here "+
			"or drop it", vital)
	}
}

// TestVitalNamesARoutedApp is the trap this would otherwise walk into. Vital
// only means anything for an app the host actually mounts and can find missing:
// a Coresident app routes no prefix and mount() returns before it can ever be
// recorded absent, so marking one Vital would be a readiness gate that can
// never fire — decoration that reads as protection.
func TestVitalNamesARoutedApp(t *testing.T) {
	for _, a := range manifest.Apps {
		if !a.Vital {
			continue
		}
		if a.Coresident {
			t.Errorf("%s is Vital and Coresident: it routes no prefix, so mount() never records "+
				"it absent and the gate can never fire", a.Name)
		}
		if len(a.Prefixes) == 0 {
			t.Errorf("%s is Vital but claims no prefix: there is nothing whose absence could be detected", a.Name)
		}
	}
}

// TestUnfitIgnoresAbsencesTheManifestDoesNotCallVital keeps the intersection
// honest in the one direction the other tests cannot reach: a name in the
// absence set that the manifest has never heard of must not become a readiness
// failure. Absence is keyed by whatever the mount loop was handed; vitality is
// keyed by the manifest. Only the intersection may stop traffic.
func TestUnfitIgnoresAbsencesTheManifestDoesNotCallVital(t *testing.T) {
	if u := unfit(map[string]string{"not-an-app": "boom", "pubsub": "boom"}); len(u) != 0 {
		t.Errorf("unfit(non-vital absences) = %v, want empty", u)
	}
	if u := unfit(map[string]string{"ai": "listen tcp :4317: bind: address already in use"}); len(u) != 1 || u["ai"] == "" {
		t.Errorf("unfit(ai absent) = %v, want ai carrying its reason", u)
	}
}

// TestProbeAnswersAreByteStable pins the exact bodies and statuses of both
// probes. They became typed ops, and the conversion must be invisible to every
// caller: the raw handlers marshalled a map, encoding/json writes map keys
// sorted, and probeOut declares its fields in that same order — so every body a
// dashboard, a script or the kubelet has ever parsed is the same bytes still.
func TestProbeAnswersAreByteStable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		absent map[string]string
		drain  bool
		path   string
		code   int
		body   string
	}{
		{"healthz clean", nil, false, "/healthz", 200, `{"status":"ok"}`},
		{"healthz reports an absence", map[string]string{"pubsub": "boom"}, false, "/healthz", 200,
			`{"absent":{"pubsub":"boom"},"status":"ok"}`},
		{"healthz stays alive while draining", nil, true, "/healthz", 200, `{"status":"ok"}`},
		{"readyz clean", nil, false, "/readyz", 200, `{"status":"ok"}`},
		{"readyz with a non-vital absence", map[string]string{"pubsub": "boom"}, false, "/readyz", 200,
			`{"absent":{"pubsub":"boom"},"status":"ok"}`},
		{"readyz with the vital app absent", map[string]string{"ai": "boom"}, false, "/readyz", 503,
			`{"absent":{"ai":"boom"},"status":"unfit"}`},
		{"readyz while draining", nil, true, "/readyz", 503, `{"status":"draining"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
			absent := tc.absent
			if absent == nil {
				absent = map[string]string{}
			}
			health(app, absent)
			if tc.drain {
				draining.Store(true)
				t.Cleanup(func() { draining.Store(false) })
			}
			code, ctype, body := do(t, app, tc.path)
			if code != tc.code {
				t.Errorf("GET %s = %d, want %d", tc.path, code, tc.code)
			}
			if !strings.Contains(ctype, "application/json") {
				t.Errorf("GET %s Content-Type = %q, want JSON", tc.path, ctype)
			}
			if body != tc.body {
				t.Errorf("GET %s body = %s, want %s — the typed op changed the bytes", tc.path, body, tc.body)
			}
		})
	}
}

// TestProbesAreTypedOps is the other half of the conversion: the routes must be
// IN the registry every projection reads, or typing them bought nothing. The
// registry is read through the CLI projection, the same a.ops the OpenAPI
// subset and the MCP tool list are built from.
func TestProbesAreTypedOps(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	health(app, map[string]string{})

	got := map[string]bool{}
	for _, c := range app.Commands() {
		got[c.Method+" "+c.Path] = true
	}
	for _, want := range []string{"GET /healthz", "GET /readyz"} {
		if !got[want] {
			t.Errorf("%s is not a registered op — it slipped back to a raw handler and is invisible "+
				"to OpenAPI, MCP, the CLI and typed Call", want)
		}
	}
}
