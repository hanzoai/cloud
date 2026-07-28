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

// Alertmanager webhook receiver — the page-delivery receipt.
//
// Alertmanager can tell you it dispatched a notification. It cannot tell you
// anything landed. This endpoint is the far side of that hop: every delivery
// prints one PAGE-DELIVERED line to the process log and joins a bounded ring
// that GET /v1/o11y/alerts/last replays. When somebody asks "did the page
// actually fire?", that ring is the answer, and it is an answer no amount of
// reading Alertmanager's own state can produce.
//
// This is the whole of the former standalone `alert-sink` Deployment (a
// stdlib-only Python script in a ConfigMap on a stock python:3.12-alpine
// image). It is 30 lines of behaviour that needed a pod, a Service, a
// ConfigMap and an operator CR to exist. It belongs on the observability
// plane that already runs, so it lives here.

package o11y

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

// recentMax bounds the replay ring. Matches the receiver this replaced, and the
// bound is the point: an unbounded receipt log is a memory leak with a nice name.
const recentMax = 200

// alertRing is the process-local delivery ring. Process-local is correct for a
// receipt — it answers "did THIS process take the call", and a receipt that
// survived its process would be a claim about something nobody observed.
type alertRing struct {
	mu    sync.Mutex
	lines []string
}

func (r *alertRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if n := len(r.lines) - recentMax; n > 0 {
		r.lines = append(r.lines[:0], r.lines[n:]...)
	}
}

func (r *alertRing) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

var recent alertRing

// webhook is the subset of Alertmanager's v4 notification payload a receipt
// needs. Unknown fields are ignored: this is a receipt, not a schema police.
type webhook struct {
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	Alerts            []alert           `json:"alerts"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
}

type alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// mountAlerts registers the receiver. Called from MountO11y BEFORE the
// hanzoai/o11y wildcard so these specific routes win the in-order match.
//
// One route per method, and the receiver name is a path PARAMETER rather than
// four hand-listed paths: Alertmanager's receiver set is config, not code, so
// a new receiver must not need a deploy.
func mountAlerts(a cloud.Router) {
	g := a.Group("/v1/o11y/alerts")
	g.Get("/last", replay)
	g.Post("/:receiver", receive)
}

// receive records one Alertmanager notification. Always 200 with body "ok":
// Alertmanager retries on any other status, and a receipt that pushes back is
// a receipt that changes the thing it is measuring.
func receive(c *zip.Ctx) error {
	var p webhook
	// A body that will not parse still proves delivery, so it is logged with
	// empty fields rather than rejected.
	_ = json.Unmarshal(c.Body(), &p)

	for _, a := range alerts(&p) {
		line := receipt(c.Path(), &p, a)
		fmt.Println(line)
		recent.add(line)
	}
	return c.String(http.StatusOK, "ok")
}

// alerts returns the alerts to record. Alertmanager sends a populated list; a
// payload without one still gets a single receipt built from the common
// labels, because a delivery that carried no per-alert detail is still a
// delivery and losing it would put a hole in the exact record this exists for.
func alerts(p *webhook) []alert {
	if len(p.Alerts) > 0 {
		return p.Alerts
	}
	return []alert{{Labels: p.CommonLabels, Annotations: p.CommonAnnotations}}
}

// receipt renders one delivery as a single greppable line. The format is the
// interface — it is what an operator greps out of the log — so it is fixed.
func receipt(path string, p *webhook, a alert) string {
	return fmt.Sprintf(
		"PAGE-DELIVERED path=%s receiver=%s status=%s alert=%s severity=%s "+
			"page=%s network=%s instance=%s :: %s",
		path,
		or(p.Receiver, "?"),
		or(a.Status, or(p.Status, "?")),
		or(a.Labels["alertname"], "?"),
		or(a.Labels["severity"], "?"),
		or(a.Labels["page"], "-"),
		or(a.Labels["network"], "-"),
		or(a.Labels["instance"], "-"),
		a.Annotations["summary"],
	)
}

// replay serves the ring as plain text, newest last, so `curl … | tail` reads
// in the order the pages arrived.
func replay(c *zip.Ctx) error {
	lines := recent.snapshot()
	if len(lines) == 0 {
		return c.String(http.StatusOK, "(none)")
	}
	return c.String(http.StatusOK, strings.Join(lines, "\n"))
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
