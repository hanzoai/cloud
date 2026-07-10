// /v1 HTTP surface consumed by arcd-tray and any other local UI.
//
// Endpoints bind to the same listener as /healthz (HealthAddr from Config),
// which defaults to 127.0.0.1:7777 — reachability from THIS machine is the auth
// model. localGuard makes that model hold against the web even on loopback: a
// Host allowlist defeats DNS-rebinding and an Origin check defeats CSRF, so a
// website the operator visits cannot pause/resume the runner or read its job
// list. A real local client (the CLI/tray) sends no Origin and passes. Binding
// HealthAddr to 0.0.0.0 re-exposes the surface to the LAN — don't.
package runner

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

// localGuard enforces that a /v1 request originates from this machine. It backs
// the loopback listener with two browser-bypass defenses:
//   - Host must be loopback → defeats DNS-rebinding (a rebound page carries its
//     own Host header, not 127.0.0.1).
//   - Any Origin header must be a localhost origin → defeats CSRF; a CLI/tray
//     sends no Origin, only a cross-site browser request does.
//
// Returns false (and writes 403) when the request must be refused.
func localGuard(w http.ResponseWriter, r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
	default:
		http.Error(w, "forbidden: non-local host", http.StatusForbidden)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && !isLocalOrigin(o) {
		http.Error(w, "forbidden: cross-origin", http.StatusForbidden)
		return false
	}
	return true
}

func isLocalOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

type v1Status struct {
	Host       string      `json:"host"`
	Version    string      `json:"version"`
	GOOS       string      `json:"goos"`
	GOARCH     string      `json:"goarch"`
	Orgs       []string    `json:"orgs"`
	Labels     []string    `json:"labels"`
	InFlight   int64       `json:"in_flight"`
	Paused     bool        `json:"paused"`
	IdleSince  time.Time   `json:"idle_since"`
	Active     []JobRecord `json:"active"`
	RecentJobs []JobRecord `json:"recent_jobs"`
}

func (d *JITDaemon) registerV1(mux *http.ServeMux) {
	mux.HandleFunc("/v1/status", d.handleV1Status)
	mux.HandleFunc("/v1/jobs", d.handleV1Jobs)
	mux.HandleFunc("/v1/pause", d.handleV1Pause)
	mux.HandleFunc("/v1/resume", d.handleV1Resume)
}

func (d *JITDaemon) handleV1Status(w http.ResponseWriter, r *http.Request) {
	if !localGuard(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := v1Status{
		Host:       d.cfg.HostName,
		Version:    Version,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Orgs:       d.cfg.Orgs,
		Labels:     d.cfg.Labels,
		InFlight:   d.inFlight.Load(),
		Paused:     d.state.IsPaused(),
		IdleSince:  d.state.IdleSince(),
		Active:     d.state.SnapshotActive(),
		RecentJobs: d.state.SnapshotRecent(10),
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *JITDaemon) handleV1Jobs(w http.ResponseWriter, r *http.Request) {
	if !localGuard(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	active := d.state.SnapshotActive()
	recent := d.state.SnapshotRecent(0)
	all := make([]JobRecord, 0, len(active)+len(recent))
	all = append(all, active...)
	all = append(all, recent...)
	writeJSON(w, http.StatusOK, all)
}

func (d *JITDaemon) handleV1Pause(w http.ResponseWriter, r *http.Request) {
	if !localGuard(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.state.SetPaused(true)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"paused":    true,
		"in_flight": d.inFlight.Load(),
	})
}

func (d *JITDaemon) handleV1Resume(w http.ResponseWriter, r *http.Request) {
	if !localGuard(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.state.SetPaused(false)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"paused": false,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
