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
//
// AND IT PAGES. A receipt nobody reads is not an alert, and until this landed
// nothing reached a human: Alertmanager's slack_configs pointed at a secret
// holding the receipt sink's own URL, so 439 "slack" notifications were
// delivered into a log. The fix is not a second Slack credential — an incoming
// webhook would be a second secret outside KMS and a second egress beside the
// one the product already uses. It is the app that is already installed: this
// receiver forwards each firing alert through integrations.SendSlack, which
// posts with the org's KMS-custodied bot token (the ONE Slack egress, shared
// with channels and automations). One credential, one egress, one receipt.

package o11y

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/openapi"
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

// The two alert routes are text/plain and deliberately un-typed (the POST accepts
// a body that will not parse; typing it would answer JSON and reject that body —
// typed_wire_test.go's untypedByDesign pins both). zipdoc has no doc comment to
// lift from a raw handler, so their prose is declared here, beside the wire fact.
func init() {
	openapi.Describe("/v1/o11y/alerts/last", http.MethodGet,
		"Replay the page-delivery receipts this process took",
		"Answers the most recent Alertmanager deliveries THIS process received, as plain "+
			"text — one greppable `PAGE-DELIVERED` line per alert, newest last, so piping to "+
			"`tail` reads in arrival order. `(none)` when nothing has landed.\n\n"+
			"It answers the question Alertmanager cannot: Alertmanager can tell you it "+
			"dispatched a notification, never that anything received it. This ring is the far "+
			"side of that hop, and it is the only record that a page actually arrived.\n\n"+
			"The ring is PROCESS-LOCAL and bounded to the last 200 lines. Both are the point: a "+
			"receipt that outlived the process that took the call would be a claim about "+
			"something nobody observed, and an unbounded receipt log is a memory leak with a "+
			"nice name. A restart empties it.")
	openapi.Describe("/v1/o11y/alerts/:receiver", http.MethodPost,
		"Take an Alertmanager notification and page Slack",
		"Records one Alertmanager webhook delivery and pages the on-call. Each alert in the "+
			"payload prints a `PAGE-DELIVERED` line to the process log and joins the ring the "+
			"receipt replay serves, then the batch is posted to Slack with the org's "+
			"KMS-custodied bot token — the ONE Slack egress the product already has, not a "+
			"second webhook credential. Resolved notifications page too: \"it recovered\" is the "+
			"half of an incident people are actually waiting for.\n\n"+
			"It ALWAYS answers 200 with the body `ok`, and a body that will not parse is "+
			"recorded with empty fields rather than rejected. Alertmanager retries on any other "+
			"status, so a receipt that pushes back changes the thing it is measuring, and a 400 "+
			"on a malformed payload would make it retry forever — the delivery still happened, "+
			"which is the fact being recorded.\n\n"+
			"The receiver segment is Alertmanager's own receiver name, a parameter rather than a "+
			"hand-listed route because the receiver set is config, not code. Paging is detached "+
			"and fail-soft: with no channel configured nothing is posted and the receipt still "+
			"lands, and a Slack failure prints its own line instead of failing the request.")
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

// receive records one Alertmanager notification and pages Slack. Always 200
// with body "ok": Alertmanager retries on any other status, and a receipt that
// pushes back is a receipt that changes the thing it is measuring.
func receive(c *zip.Ctx) error {
	var p webhook
	// A body that will not parse still proves delivery, so it is logged with
	// empty fields rather than rejected.
	_ = json.Unmarshal(c.Body(), &p)

	as := alerts(&p)
	for _, a := range as {
		line := receipt(c.Path(), &p, a)
		fmt.Println(line)
		recent.add(line)
	}
	page(&p, as)
	return c.String(http.StatusOK, "ok")
}

// Slack paging knobs. The channel is the only thing that MUST be configured —
// no channel, no paging (and the receipt still lands, so silence here is never
// silence everywhere). The org owns the Slack connection whose bot token KMS
// custodies; it defaults to the platform tenant.
const (
	alertsSlackChannelEnv = "CLOUD_ALERTS_SLACK_CHANNEL"
	alertsSlackOrgEnv     = "CLOUD_ALERTS_SLACK_ORG"
	defaultAlertsOrg      = "hanzo"
	slackPageTimeout      = 10 * time.Second
)

// page posts the notification to Slack through the ONE egress. It is
// DETACHED and fail-soft by construction: Alertmanager is waiting on this
// request, and an alert path that can block or fail on a third party is an
// alert path that goes quiet exactly when the third party is having the
// outage. Resolved notifications page too — "it recovered" is the half of an
// incident people actually wait for.
func page(p *webhook, as []alert) {
	channel := strings.TrimSpace(os.Getenv(alertsSlackChannelEnv))
	if channel == "" || len(as) == 0 {
		return
	}
	org := strings.TrimSpace(os.Getenv(alertsSlackOrgEnv))
	if org == "" {
		org = defaultAlertsOrg
	}
	text := slackText(p, as)
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), slackPageTimeout)
		defer cancel()
		if err := integrations.SendSlack(ctx, org, channel, "", text); err != nil {
			// One line, in the same log as the receipts: a page that could not
			// be sent is itself an operational fact, and the receipt above
			// already proved the alert arrived.
			fmt.Printf("PAGE-SLACK-FAILED org=%s channel=%s receiver=%s err=%v\n",
				org, channel, or(p.Receiver, "?"), err)
		}
	}()
}

// slackText renders the notification as one Slack message: a firing/resolved
// headline, then one line per alert carrying the fields an on-call reads first
// (name, severity, instance, summary). Bounded — a storm must not post a
// thousand-line wall — with the overflow counted rather than dropped silently.
func slackText(p *webhook, as []alert) string {
	const maxLines = 20
	icon, verb := ":rotating_light:", "FIRING"
	if strings.EqualFold(p.Status, "resolved") {
		icon, verb = ":white_check_mark:", "RESOLVED"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s *%s* — %d alert(s) via `%s`\n", icon, verb, len(as), or(p.Receiver, "?"))
	for i, a := range as {
		if i == maxLines {
			fmt.Fprintf(&b, "_… and %d more_\n", len(as)-maxLines)
			break
		}
		fmt.Fprintf(&b, "• *%s* [%s] %s%s\n",
			or(a.Labels["alertname"], "?"),
			or(a.Labels["severity"], "?"),
			or(a.Labels["instance"], or(a.Labels["network"], "-")),
			annotation(a))
	}
	return b.String()
}

// annotation appends the human sentence an alert carries, if it has one.
func annotation(a alert) string {
	s := strings.TrimSpace(or(a.Annotations["summary"], a.Annotations["description"]))
	if s == "" {
		return ""
	}
	return " — " + s
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
