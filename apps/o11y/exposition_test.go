// Copyright 2025-2026 Hanzo AI Inc. All Rights Reserved.
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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/o11y/pkg/prober"

	"github.com/hanzoai/cloud"
)

// TestExpositionCarriesTheFleetGauge is the write path end to end, in one
// process: the prober records hanzo_service_up through the meter, and a
// collector reading the exposition finds it.
//
// It exists because the failure it guards against was invisible. The prober ran
// in production 21 times a minute against a meter provider with no way out, the
// code looked instrumented, every log line said the probes were running, and the
// only symptom was /v1/summary answering 503 for a series that was never
// written. Nothing was broken enough to fail; the path simply had no end.
func TestExpositionCarriesTheFleetGauge(t *testing.T) {
	installTestMeter(t)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()

	p, err := prober.New(prober.Config{
		Targets: []prober.Target{
			{Name: "serving", URL: up.URL},
			{Name: "broken", URL: down.URL},
		},
		Interval: time.Hour, // one immediate probe on Start is the whole measurement
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	p.Start(context.Background())
	defer p.Stop()

	// The names summary.go queries, exactly. A translation strategy that appended
	// suffixes would publish something else under a name no reader asks for, and
	// the endpoint would still answer 503 with the gauge sitting right there.
	//
	// Both targets, because a 5xx is DOWN and not absent: "nothing" is what a
	// crashed prober also publishes, and telling those apart is the reason this
	// gauge exists.
	body := scrapeUntil(t,
		`hanzo_service_up{service="serving"} 1`,
		`hanzo_service_up{service="broken"} 0`,
	)

	if strings.Contains(body, `hanzo_service_up{service="broken"} 1`) {
		t.Error("a target answering 500 was published as up")
	}
}

// TestExpositionDoesNotDoubleSuffixUnits guards the instrument whose name already
// ends in its unit. The exporter appends a unit suffix by default, so a histogram
// declared with unit "s" and named ..._seconds publishes as ..._seconds_seconds —
// a series no dashboard or rule in this estate refers to.
func TestExpositionDoesNotDoubleSuffixUnits(t *testing.T) {
	installTestMeter(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	p, err := prober.New(prober.Config{
		Targets:  []prober.Target{{Name: "timed", URL: target.URL}},
		Interval: time.Hour,
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	p.Start(context.Background())
	defer p.Stop()

	body := scrapeUntil(t, "hanzo_service_probe_duration_seconds")

	if strings.Contains(body, "_seconds_seconds") {
		t.Errorf("unit suffix applied twice\n--- body ---\n%s", body)
	}
}

// TestExpositionOffPublishesNothing: an explicit empty address is the off switch,
// and off must mean no listener rather than a listener on the default port.
func TestExpositionOffPublishesNothing(t *testing.T) {
	t.Setenv("O11Y_SCRAPE_LISTEN", "")
	startExposition(luxlog.New("test"))
	t.Cleanup(func() { stopExposition(context.Background()) })

	if exposition != nil || expositionAddr != "" {
		t.Fatalf("exposition published with the off switch set: addr=%q", expositionAddr)
	}
}

// installTestMeter installs the process meter provider once for the whole
// package: the provider registers a collector with a process-wide registry, so a
// second install would be a duplicate registration rather than a fresh start.
// Every test here therefore reads the SAME registry, which is also how the
// process behaves.
func installTestMeter(t *testing.T) {
	t.Helper()
	meterOnce.Do(func() {
		// No span destination is configured here on purpose: metrics must install
		// on their own, or the fleet gauge would depend on a tracing setting.
		cloud.InstallTelemetry(context.Background(), luxlog.New("test"), "o11y-test")
	})
}

var meterOnce sync.Once

// scrapeUntil starts the listener on a kernel-chosen port and collects until
// every wanted line is published, returning the last body — what a collector
// would see.
//
// It polls because the producer is asynchronous: each target is probed on its own
// goroutine, and a single collection can land between the two Records one probe
// makes, reporting the duration of a check whose verdict is not in yet. A
// collector re-reads every interval and would see the settled value; a test that
// reads once sees whichever instant it caught.
func scrapeUntil(t *testing.T, want ...string) string {
	t.Helper()
	t.Setenv("O11Y_SCRAPE_LISTEN", "127.0.0.1:0")
	startExposition(luxlog.New("test"))
	t.Cleanup(func() { stopExposition(context.Background()) })

	if expositionAddr == "" {
		t.Fatal("exposition did not bind")
	}

	var body string
	deadline := time.Now().Add(10 * time.Second)
	for {
		body = collect(t)
		missing := ""
		for _, w := range want {
			if !strings.Contains(body, w) {
				missing = w
				break
			}
		}
		if missing == "" {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("exposition never published %q\n--- body ---\n%s", missing, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// collect performs one scrape of the exposition.
func collect(t *testing.T) string {
	t.Helper()
	resp, err := http.Get("http://" + expositionAddr + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("scrape body: %v", err)
	}
	return string(body)
}
