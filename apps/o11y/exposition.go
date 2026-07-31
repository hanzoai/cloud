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

// This process's own measurements, published for collection.
//
// The fleet prober (probes.go) knocks on every target every 30s and records
// hanzo_service_up through this process's meter. A meter only holds a number;
// something has to carry it to the store, and until this listener existed nothing
// did — the gauge was written 21 times a minute into a provider with no way out,
// while /v1/summary answered 503 because the series it reads did not exist.
//
// Collection is a PULL here because that is how metrics reach the store this
// estate actually keeps them in. Every metric reader in this package —
// vmquery.go, the SuperAdmin VM proxy, status.go's up-inventory, summary.go —
// queries VictoriaMetrics, and VM is filled by scraping Prometheus endpoints.
// Publishing the meter is what closes the circuit: the gauge this app writes
// lands in the store this app reads.
//
// It is a SEPARATE listener rather than a route on the product API. A scrape must
// not be identified, rate-limited or metered, and every /v1 path is all three; a
// collector also has to reach this process while the API plane is saturated,
// which is exactly when the answer matters. o11y already owns listeners for the
// receiving side of this plane (:4317, :4318, :4319) — this is the same plane,
// read from.
//
// Opt-out, like the prober it publishes. A signal whose only exit defaults to off
// is a signal that is off in the deployment that needed it.

package o11y

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// defaultExpositionListen is the conventional port for an OpenTelemetry
// Prometheus exporter, which is what this is. Naming it by convention means a
// collector configured against any such process finds this one too.
const defaultExpositionListen = ":9464"

// exposition is pinned for the process life so Shutdown can stop it, alongside
// the address it actually bound — which is not always the one asked for (a port
// of 0 is chosen by the kernel, a host name is resolved), and the address a
// collector must be pointed at is the resolved one.
var (
	exposition     *http.Server
	expositionAddr string
)

// startExposition publishes this process's metrics for collection.
//
// O11Y_SCRAPE_LISTEN moves it; an explicit empty value turns it off. Fail-soft:
// a listener that cannot bind is logged and skipped, never fatal — losing the
// metrics surface must not cost the query plane, which is the same posture the
// receiving side (metrics.go) and the prober (probes.go) take.
func startExposition(log luxlog.Logger) {
	listen := defaultExpositionListen
	if v, ok := os.LookupEnv("O11Y_SCRAPE_LISTEN"); ok {
		listen = strings.TrimSpace(v)
	}
	if listen == "" {
		log.Info("metrics exposition disabled (O11Y_SCRAPE_LISTEN empty)")
		return
	}

	// Bind BEFORE handing the socket to a goroutine, so an address already in use
	// is reported here as the reason this process has no metrics surface. Serving
	// in the background and logging from there loses the error into a log nobody
	// correlates, and the symptom — an empty store — looks identical to a prober
	// that never ran.
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Warn("metrics exposition not published; the fleet gauge will not reach the metrics store",
			"listen", listen, "err", err)
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", cloud.Metrics())
	srv := &http.Server{
		Handler: mux,
		// A collector that opens a connection and never sends a request would
		// otherwise hold it for the process life.
		ReadHeaderTimeout: 5 * time.Second,
	}
	exposition, expositionAddr = srv, ln.Addr().String()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("metrics exposition stopped", "listen", expositionAddr, "err", err)
		}
	}()

	log.Info("metrics exposition published", "listen", expositionAddr, "path", "/metrics", "metric", "hanzo_service_up")
}

// stopExposition is called from the subsystem's Shutdown.
func stopExposition(ctx context.Context) {
	if exposition == nil {
		return
	}
	_ = exposition.Shutdown(ctx)
	exposition, expositionAddr = nil, ""
}
