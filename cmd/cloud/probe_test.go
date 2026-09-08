// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package main

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/zap-proto/zip"
)

// chart is the deployment this binary ships with, so reading it here is reading
// what the kubelet is actually told to do.
const chart = "../../helm/cloud/values.yaml"

// httpPort is the ONE port name this host answers HTTP on. main.go's last line
// binds two addresses and no others — the ZAP listener and the HTTP listener —
// so every probe has exactly one place it can land.
const httpPort = "http"

// probe is the part of a k8s probe that decides whether it can connect at all.
type probe struct {
	HTTPGet struct {
		Path string `json:"path"`
		Port string `json:"port"`
	} `json:"httpGet"`
}

// probes reads the chart's probe set by name.
func probes(t *testing.T) map[string]probe {
	t.Helper()
	b, err := os.ReadFile(chart)
	if err != nil {
		t.Fatalf("read %s: %v", chart, err)
	}
	var v struct {
		Probes map[string]probe `json:"probes"`
	}
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s is not values YAML: %v", chart, err)
	}
	if len(v.Probes) == 0 {
		t.Fatalf("%s declares no probes — an unprobed pod is Ready the instant it is scheduled", chart)
	}
	return v.Probes
}

// TestEveryProbeTargetsAPortThisHostBinds is the deploy that could not roll.
//
// The ops port serve.go opens — healthMux on CLOUD_HEALTH_LISTEN, :9090 —
// belongs to cloud.Listen, and THIS host does not call it. The fused monolith
// that did is gone; the router that replaced it links zip, the manifest and
// webui, and never the root package. 9090 survived as a containerPort with
// nothing behind it.
//
// Readiness pointed there anyway, on the strength of a comment that was true
// while the monolith was the host: "/readyz there is the ONLY drain-aware
// endpoint". So the probe drew connection-refused every period, the pod never
// reported Ready, and a Deployment whose pod never reports Ready cannot roll.
// It reads like a crashing image and leaves nothing in the logs, because the
// process was healthy and merely was not listening where it had been asked.
//
// The endpoint moved rather than vanished: health() registers /readyz on the
// app, on the HTTP port, and it answers BOTH draining and a Vital absence
// (readyz_test.go) — strictly more than healthMux ever knew.
func TestEveryProbeTargetsAPortThisHostBinds(t *testing.T) {
	for name, p := range probes(t) {
		if p.HTTPGet.Port != httpPort {
			t.Errorf("%s probe targets port %q, but this host binds only %q — the kubelet gets "+
				"connection refused forever, so the pod never reports Ready and the rollout stalls "+
				"on an image that is running fine", name, p.HTTPGet.Port, httpPort)
		}
	}
}

// TestEveryProbePathIsServed pins the other half: a probe may name the right
// port and still ask for a path nothing registered. On the HTTP listener an
// unclaimed path is answered by the console SPA catch-all, which returns 200
// and HTML — so a liveness probe on a misspelled path passes forever by reading
// a web page, and reports the API healthy while it is gone.
func TestEveryProbePathIsServed(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	health(app, map[string]string{})

	for name, p := range probes(t) {
		code, ctype, body := do(t, app, p.HTTPGet.Path)
		if code != 200 {
			t.Errorf("%s probe GET %s = %d, want 200 — a healthy host must pass its own probe",
				name, p.HTTPGet.Path, code)
			continue
		}
		// JSON is the proof it came from health() and not from the catch-all.
		if !hasJSON(ctype) {
			t.Errorf("%s probe GET %s answered %s (%q), not JSON — nothing registered that path, "+
				"so this probe is reading the console and would pass with the API dead",
				name, p.HTTPGet.Path, ctype, trim(body))
		}
	}
}

func hasJSON(ctype string) bool {
	for i := 0; i+4 <= len(ctype); i++ {
		if ctype[i:i+4] == "json" {
			return true
		}
	}
	return false
}

func trim(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
