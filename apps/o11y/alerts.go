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

// Alertmanager webhook receiver — arrival and delivery, told apart.
//
// An alert carries two distinct facts and this file used to braid them into
// one word. A notification ARRIVED here (Alertmanager made the call), and a
// notification was DELIVERED to a human (something carried it out of this
// process). They are not the same fact, they fail independently, and for
// months this endpoint reported the first while everyone read it as the
// second:
//
//	[o11y] PAGE-DELIVERED  … alert=NodeMemoryCommittedCritical   ← arrival
//	[o11y] PAGE-SLACK-FAILED … err=slack not connected for org   ← the truth
//
// Alertmanager logged `Notify success`, this endpoint answered 200 `ok`, and
// the page reached nobody. Three green lights over a silent pager, because the
// egress ran DETACHED in a goroutine the response never waited for. The
// request was answered before the send was attempted, so the answer could not
// possibly have been about it.
//
// So the two facts now have two names and two records:
//
//	ALERT-RECEIVED    — this process took the call. Always true, always logged,
//	                    joins the replay ring. It is a receipt, nothing more.
//	ALERT-DELIVERED   — an egress accepted it, and which one.
//	ALERT-UNDELIVERED — no egress accepted it, and why. Answered 503.
//
// and THE STATUS CODE REPORTS DELIVERY, NOT ARRIVAL. Nothing delivered means
// non-2xx, which is the only sentence Alertmanager understands: it retries,
// and it counts the failure in alertmanager_notifications_failed_total, which
// is itself alertable. An alert path that cannot reach a human must fail
// loudly at the protocol level, because the one thing it must never do is look
// identical to a working one.
//
// EGRESS IS A CHAIN, not a call. Ways out are tried in order, first success
// wins:
//
//  1. slack   — the org's KMS-custodied bot token via the integrations peer.
//     The ONE product Slack egress (shared with channels and automations), so
//     no second credential. It requires the workspace to be CONNECTED, which
//     is an owner action and therefore something the alert path must never
//     assume.
//  2. email   — one message to CLOUD_ALERTS_EMAIL_TO through the notify peer,
//     which sends on the org's own KMS-held email credential. It needs no
//     connected Slack workspace.
//  3. webhook — a plain POST of {"text": …} to CLOUD_ALERTS_WEBHOOK_URL. No
//     integrations peer, no org, no connected workspace, no KMS: it works
//     precisely in the state that silenced everything above. That is its job.
//
// Degraded is not healthy: when Slack fails and the webhook carries it, the
// delivery is real (200) but the Slack failure is still recorded and still
// counted, so a broken egress cannot hide behind a working one.
//
// ⚠️ The chain runs INSIDE cloud, so an alert about cloud being down routes
// through the thing that is down — observed twice as `dial tcp
// <pod ip>:8000: connect: connection refused`, recovering on retry 8. That
// hop cannot be fixed from in here; it is fixed in Alertmanager, which posts
// every Hanzo receiver to BOTH this endpoint and the same webhook URL directly
// (universe: infra/k8s/monitoring/alertmanager-config.yaml). This process is
// the rich path; the direct one is the path that survives this process.

package o11y

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	notifypeer "github.com/hanzoai/cloud/client/notify"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
)

// recentMax bounds the replay ring. Matches the receiver this replaced, and the
// bound is the point: an unbounded receipt log is a memory leak with a nice name.
const recentMax = 200

// alertRing is the process-local record ring. Process-local is correct for a
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
	spec.Describe("/v1/o11y/alerts/last", http.MethodGet,
		"Replay the alert records this process took",
		"Answers the most recent Alertmanager deliveries THIS process received, as plain "+
			"text — one greppable `ALERT-RECEIVED` line per alert, followed by the "+
			"`ALERT-DELIVERED` / `ALERT-UNDELIVERED` outcome of carrying it out of the "+
			"process, newest last, so piping to `tail` reads in arrival order. `(none)` when "+
			"nothing has landed.\n\n"+
			"Arrival and delivery are separate lines because they are separate facts that "+
			"fail independently. Alertmanager can tell you it dispatched a notification, never "+
			"that anything received it; this process taking the call says nothing about whether "+
			"a human was reached. Reading only the first as if it were the second is how a "+
			"pager stays silent for months behind a log where everything looks fine.\n\n"+
			"The ring is PROCESS-LOCAL and bounded to the last 200 lines. Both are the point: a "+
			"record that outlived the process that took the call would be a claim about "+
			"something nobody observed, and an unbounded log is a memory leak with a nice "+
			"name. A restart empties it.")
	spec.Describe("/v1/o11y/alerts/:receiver", http.MethodPost,
		"Take an Alertmanager notification and page a human",
		"Records one Alertmanager webhook delivery and pages the on-call. Each alert prints an "+
			"`ALERT-RECEIVED` line and joins the replay ring, then the batch is carried out of "+
			"the process by the egress chain: the org's KMS-custodied Slack bot token first "+
			"(the ONE product Slack egress, not a second webhook credential), then an email to "+
			"`CLOUD_ALERTS_EMAIL_TO` on the org's own notify credential, then a plain POST to "+
			"`CLOUD_ALERTS_WEBHOOK_URL` — which needs no Slack connection and so works in "+
			"exactly the state that silences the first. Resolved notifications page "+
			"too: \"it recovered\" is the half of an incident people are actually waiting for. "+
			"An alert labelled `watchdog=\"true\"` is the path's heartbeat (see "+
			"`GET /v1/o11y/alerts/heartbeat`) and pages nobody.\n\n"+
			"THE STATUS CODE REPORTS DELIVERY, NOT ARRIVAL. 200 `ok` means an egress accepted "+
			"the batch. If none did — including when none is configured at all — it answers "+
			"**503** naming the failure, so Alertmanager retries and counts it in "+
			"`alertmanager_notifications_failed_total`. An alert nobody could be told about "+
			"must never answer the same way as one that was delivered.\n\n"+
			"A body that will not parse is still recorded (with empty fields) rather than "+
			"rejected: the delivery happened, which is the fact being recorded, and a 400 would "+
			"make Alertmanager retry a malformed payload forever.\n\n"+
			"The receiver segment is Alertmanager's own receiver name, a parameter rather than a "+
			"hand-listed route because the receiver set is config, not code.")
}

// mountAlerts registers the receiver. Called from Mount BEFORE the
// hanzoai/o11y wildcard so these specific routes win the in-order match.
//
// One route per method, and the receiver name is a path PARAMETER rather than
// four hand-listed paths: Alertmanager's receiver set is config, not code, so
// a new receiver must not need a deploy.
func mountAlerts(a *zip.App) {
	g := a.Group("/v1/o11y/alerts")
	// Raw: the replay serves text/plain lines for an operator's tail, not JSON.
	g.Raw(http.MethodGet, "/last", replay)
	g.Get("/heartbeat", handleHeartbeat)
	// Raw: this speaks Alertmanager's webhook protocol, whose unparseable body
	// must reach the handler rather than be refused — a typed op would decode
	// first and answer 400, while receive records the raw payload and answers
	// for the CARRYING (503 when no egress delivers, even unparsed).
	g.Raw(http.MethodPost, "/:receiver", receive)
}

// receive records one Alertmanager notification, carries it to a human, and
// answers with the result of the CARRYING — not of the recording.
//
// Delivery is SYNCHRONOUS. The previous version sent in a detached goroutine,
// which made 200 structurally incapable of meaning anything: the response was
// written before the send was tried. A bounded wait is what makes the status
// code a fact rather than a hope.
func receive(c *zip.Ctx) error {
	var p webhook
	// A body that will not parse still proves delivery, so it is recorded with
	// empty fields rather than rejected.
	_ = json.Unmarshal(c.Body(), &p)

	// A watchdog alert is the alert path proving it is alive. It is recorded as a
	// heartbeat and goes to no human: an always-firing alert on a person's pager
	// is how the alerts that matter get ignored.
	var as []alert
	for _, a := range alerts(&p) {
		if a.Labels["watchdog"] == "true" {
			beat(c.Path(), &p, a)
			continue
		}
		record(receipt(c.Path(), &p, a))
		as = append(as, a)
	}
	if len(as) == 0 {
		return c.String(http.StatusOK, "ok")
	}

	ctx, cancel := context.WithTimeout(c.Context(), egressBudget)
	defer cancel()
	via, failures := deliver(ctx, &p, as)

	// A failure is recorded even when a later egress succeeded: an egress that
	// hides behind a working one is the failure mode this file exists to end.
	for _, f := range failures {
		host.ObserveAlertDelivery(f.egress, "failed")
		record(fmt.Sprintf("ALERT-EGRESS-FAILED egress=%s receiver=%s err=%v",
			f.egress, or(p.Receiver, "?"), f.err))
	}

	if via == "" {
		why := reason(failures)
		record(fmt.Sprintf("ALERT-UNDELIVERED receiver=%s alerts=%d :: %s",
			or(p.Receiver, "?"), len(as), why))
		// 503, not 200. Alertmanager retries this and counts it, which is the
		// only way the outside world can learn that paging is broken.
		return c.String(http.StatusServiceUnavailable, "undelivered: "+why)
	}
	host.ObserveAlertDelivery(via, "delivered")
	record(fmt.Sprintf("ALERT-DELIVERED via=%s receiver=%s alerts=%d",
		via, or(p.Receiver, "?"), len(as)))
	return c.String(http.StatusOK, "ok")
}

// heartbeatWindow is how long the path may go without a heartbeat before the
// switch reports it dead. The watchdog renotifies every 5 minutes, so three
// missed beats in a row.
const heartbeatWindow = 15 * time.Minute

// lastBeat is when this process last heard the watchdog fire, starting at boot so
// a restart is not reported as a dead path before the first beat can arrive.
var lastBeat = struct {
	sync.Mutex
	at time.Time
}{at: time.Now()}

// beat records one watchdog notification. Only a firing one is a heartbeat: the
// watchdog resolving means it stopped, which is the opposite of a beat.
func beat(path string, p *webhook, a alert) {
	status := or(a.Status, or(p.Status, "?"))
	now := time.Now()
	if strings.EqualFold(status, "firing") {
		lastBeat.Lock()
		lastBeat.at = now
		lastBeat.Unlock()
	}
	record(fmt.Sprintf("ALERT-HEARTBEAT path=%s receiver=%s status=%s alert=%s at=%s",
		path, or(p.Receiver, "?"), status, or(a.Labels["alertname"], "?"), now.UTC().Format(time.RFC3339)))
}

// Heartbeat is the alert path's pulse.
type Heartbeat struct {
	// AgeSeconds is how long ago the last heartbeat arrived — or this process
	// started, before its first.
	AgeSeconds int64 `json:"ageSeconds"`
	// LimitSeconds is how old the heartbeat may get before this answers 503.
	LimitSeconds int64 `json:"limitSeconds"`
}

// handleHeartbeat reports whether the alert path's heartbeat is still arriving:
// the dead-man's switch for paging. The o11y ruler fires one alert forever,
// labelled watchdog="true", and every notification of it that reaches this
// process is a heartbeat — recorded as ALERT-HEARTBEAT, never sent to a human.
//
// It answers 200 with the heartbeat's age while one arrived within the last 15
// minutes, and 503 once none has. A 503 means the path from the ruler to this
// receiver stopped — the ruler, its Alertmanager, or this process — which nothing
// inside the cluster can be trusted to say, so something OUTSIDE it polls this and
// pages on the 503. Process-local, like the replay ring: a restart starts the
// clock again rather than answering 503 before the first beat can arrive.
//
// Example: {}
func handleHeartbeat(_ context.Context, _ *cloud.Unit) (*Heartbeat, error) {
	lastBeat.Lock()
	at := lastBeat.at
	lastBeat.Unlock()
	age := time.Since(at).Round(time.Second)
	if age > heartbeatWindow {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"no heartbeat for %s (limit %s): the alert path has stopped", age, heartbeatWindow)
	}
	return &Heartbeat{AgeSeconds: int64(age / time.Second), LimitSeconds: int64(heartbeatWindow / time.Second)}, nil
}

// record prints one line to the process log and joins the replay ring. Both or
// neither — a line an operator can grep but not replay (or the reverse) is a
// record that disagrees with itself.
func record(line string) {
	fmt.Println(line)
	recent.add(line)
}

// Egress knobs.
const (
	alertsSlackChannelEnv = "CLOUD_ALERTS_SLACK_CHANNEL"
	// alertsEmailEnv is a comma list of addresses; each is mailed every page.
	alertsEmailEnv = "CLOUD_ALERTS_EMAIL_TO"
	// alertsOrgEnv is the org the egresses act as: its Slack bot token and its
	// notify email credential.
	alertsOrgEnv = "CLOUD_ALERTS_ORG"
	// alertsWebhookEnv is the fallback egress: any URL that accepts a POST of
	// {"text": …}. It deliberately has NO dependency on the integrations peer,
	// an org, or a connected workspace — it is the egress for the state where
	// those are the problem.
	alertsWebhookEnv = "CLOUD_ALERTS_WEBHOOK_URL"
	defaultAlertsOrg = "hanzo"
	// egressBudget bounds the whole chain, not one hop. Alertmanager is waiting
	// on this request; a page that has not left in eight seconds is better
	// reported as undelivered (and retried) than waited on.
	egressBudget = 8 * time.Second
)

// egress is one way out of this process to a human.
//
// A chain of these — rather than one hard-wired call — is what makes "Slack is
// not connected" a DEGRADED state instead of a silent one. Adding a way out is
// adding an element; the delivery contract above it does not change.
type egress struct {
	name string
	send func(ctx context.Context, text string) error
	// always is a copy every page gets, whether or not an earlier egress
	// carried it: a mailbox that must see every error, not only the ones the
	// channel missed.
	always bool
}

// failure is one egress's refusal, kept with its name so the record says which
// way out was tried and the metric can be labelled by it.
type failure struct {
	egress string
	err    error
}

// egressChain is what deliver walks. A var so tests can substitute a
// deterministic chain; production rebuilds it from the environment on every
// call, so connecting an egress does not need a restart.
var egressChain = configuredEgresses

// configuredEgresses returns the ways out, in preference order. Each is present
// only when it is configured — an egress that cannot be attempted must not be
// counted as one that was.
func configuredEgresses() []egress {
	var out []egress
	org := env.Or(alertsOrgEnv, "")
	if org == "" {
		org = defaultAlertsOrg
	}
	if channel := env.Or(alertsSlackChannelEnv, ""); channel != "" {
		out = append(out, egress{name: "slack", send: func(ctx context.Context, text string) error {
			return slackSend(ctx, org, channel, text)
		}})
	}
	// A comma list: each address is mailed every page, beside Slack.
	for _, to := range strings.Split(env.Or(alertsEmailEnv, ""), ",") {
		if to = strings.TrimSpace(to); to == "" {
			continue
		}
		out = append(out, egress{name: "email", always: true, send: func(ctx context.Context, text string) error {
			return emailSend(ctx, org, to, text)
		}})
	}
	if url := env.Or(alertsWebhookEnv, ""); url != "" {
		out = append(out, egress{name: "webhook", send: func(ctx context.Context, text string) error {
			return webhookSend(ctx, url, text)
		}})
	}
	return out
}

// deliver walks the chain and stops at the first egress that accepts the batch.
// It returns the name of the one that carried it ("" if none did) and every
// failure along the way.
//
// No egress configured is a FAILURE, not a no-op. "Nobody can be told" is the
// state this whole file exists to make loud, and returning quietly when nothing
// was configured is how it stayed quiet.
func deliver(ctx context.Context, p *webhook, as []alert) (via string, failures []failure) {
	chain := egressChain()
	if len(chain) == 0 {
		return "", []failure{{egress: "none", err: errors.New("no alert egress configured: set " +
			alertsSlackChannelEnv + ", " + alertsEmailEnv + " or " + alertsWebhookEnv)}}
	}
	return walk(ctx, chain, slackText(p, as))
}

// announce carries one text out on the same chain an alert takes — Sentinel's
// new-issue line uses it, so an error and an alert reach the same humans the same way.
func announce(ctx context.Context, text string) (via string, failures []failure) {
	chain := egressChain()
	if len(chain) == 0 {
		return "", []failure{{egress: "none", err: errors.New("no alert egress configured: set " +
			alertsSlackChannelEnv + ", " + alertsEmailEnv + " or " + alertsWebhookEnv)}}
	}
	ctx, cancel := context.WithTimeout(ctx, egressBudget)
	defer cancel()
	return walk(ctx, chain, text)
}

// walk tries each egress in order. A fallback egress is skipped once one has
// carried the text; an always egress is sent regardless. via names the first
// that carried it.
func walk(ctx context.Context, chain []egress, text string) (via string, failures []failure) {
	for _, e := range chain {
		if via != "" && !e.always {
			continue
		}
		if err := e.send(ctx, text); err != nil {
			failures = append(failures, failure{egress: e.name, err: err})
			continue
		}
		if via == "" {
			via = e.name
		}
	}
	return via, failures
}

// reason renders the failures as one sentence for the 503 body and the
// undelivered line — whoever reads either must not need a second lookup to
// learn which way out broke, and how.
func reason(failures []failure) string {
	if len(failures) == 0 {
		return "no egress attempted"
	}
	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		parts = append(parts, fmt.Sprintf("%s: %v", f.egress, f.err))
	}
	return strings.Join(parts, "; ")
}

// slackSend posts through the ONE product Slack egress: ZAP over the unix
// socket to the integrations PROCESS, which owns the org's bot token (a plugin
// is a process; a package global here reads a nil peer copy — the
// "integrations: not mounted" failure). cloud.Ask dials the socket and wakes
// the provider app if it is asleep, exactly as x402 asks commerce to move money.
// cloud.For stamps the org so provider's handler reads it as the caller's,
// never an argument.
func slackSend(ctx context.Context, org, channel, text string) error {
	// Join: a page must not die on the app not yet being in the ops channel.
	_, err := providerpeer.IntegrationsSlackSend(cloud.For(ctx, org),
		&client.SlackSendIn{Channel: channel, Text: text, Join: true})
	return err
}

// notifySend is the notify peer's send op. A var so tests can stand in for the
// peer.
var notifySend = notifypeer.NotifySend

// emailSend mails the page to one address through the notify peer, which picks
// the provider from the org's own KMS-held email credential. The first line of
// the page, without its Slack markup, is the subject.
//
// notify sends Body as HTML and Text as the plain-text part beside it, so the
// page goes as both: the Slack page handed over verbatim read as one run-on line
// of markup in an HTML client, and as "No Content" in a client that shows the
// plain-text part, which was empty (cloud#770).
func emailSend(ctx context.Context, org, to, text string) error {
	plain := plainPage(text)
	subject, _, _ := strings.Cut(plain, "\n")
	sent, err := notifySend(cloud.For(ctx, org), &client.Send{
		Org: org, Channel: "email", To: to, Subject: strings.TrimSpace(subject),
		Body: htmlPage(plain), Text: plain,
	})
	if err != nil {
		return err
	}
	if sent == nil || sent.Provider == "" {
		return errors.New("notify answered without naming the provider that sent it")
	}
	return nil
}

// slackMarkup is what slackText adds for Slack and an email must not show.
var slackMarkup = strings.NewReplacer(":rotating_light: ", "", ":white_check_mark: ", "", ":rotating_light:", "", ":white_check_mark:", "", "*", "", "`", "")

// plainPage is the page without its Slack markup, line for line.
func plainPage(text string) string {
	lines := strings.Split(slackMarkup.Replace(text), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "_… and ") && strings.HasSuffix(l, "_") {
			lines[i] = strings.TrimSuffix(strings.TrimPrefix(l, "_"), "_")
		}
	}
	return strings.Join(lines, "\n")
}

// htmlPage renders a plain page as HTML: escaped, one line per alert, the
// headline and each alert's name in bold.
func htmlPage(plain string) string {
	var b strings.Builder
	b.WriteString("<div style=\"font-family:monospace\">\n")
	for i, l := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		switch {
		case i == 0:
			fmt.Fprintf(&b, "<b>%s</b><br>\n", html.EscapeString(l))
		case strings.HasPrefix(l, "• "):
			name, rest, _ := strings.Cut(strings.TrimPrefix(l, "• "), " [")
			if rest != "" {
				rest = " [" + rest
			}
			fmt.Fprintf(&b, "• <b>%s</b>%s<br>\n", html.EscapeString(name), html.EscapeString(rest))
		default:
			fmt.Fprintf(&b, "%s<br>\n", html.EscapeString(l))
		}
	}
	b.WriteString("</div>\n")
	return b.String()
}

// webhookClient is shared: one connection pool for the fallback egress. Its
// timeout is a backstop only — the caller's context carries the real budget.
var webhookClient = &http.Client{Timeout: egressBudget}

// webhookSend posts the page as {"text": …} — the shape a Slack incoming
// webhook takes, which is also the shape most generic receivers take, so the
// URL can be whatever the owner actually has without this code learning a
// second format.
//
// A non-2xx is an error. This is the FALLBACK: if it quietly swallowed a
// failure there would be nothing left underneath to notice.
func webhookSend(ctx context.Context, url, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}

// slackText renders the notification as one page: a firing/resolved headline,
// then one line per alert carrying the fields an on-call reads first (name,
// severity, instance, summary). Bounded — a storm must not post a
// thousand-line wall — with the overflow counted rather than dropped silently.
func slackText(p *webhook, as []alert) string {
	const maxLines = 20
	icon, verb := ":rotating_light:", "FIRING"
	if strings.EqualFold(p.Status, "resolved") {
		icon, verb = ":white_check_mark:", "RESOLVED"
	}
	// ONE LINE PER ALERT AS IT READS. Two series of one fact — the same gauge
	// pushed by two pods, an old pod's last sample beside its replacement's —
	// are two alerts to Alertmanager, told apart by a label this page does not
	// print, so the page said the same line up to four times
	// (hanzo-inc/cloud#828). A line said more than once is said once, counted.
	var lines []string
	said := map[string]int{}
	for _, a := range as {
		l := fmt.Sprintf("• *%s* [%s] %s%s",
			or(a.Labels["alertname"], "?"),
			or(a.Labels["severity"], "?"),
			or(a.Labels["instance"], or(a.Labels["network"], "-")),
			annotation(a))
		if said[l] == 0 {
			lines = append(lines, l)
		}
		said[l]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s *%s* — %d alert(s) via `%s`\n", icon, verb, len(lines), or(p.Receiver, "?"))
	for i, l := range lines {
		if i == maxLines {
			fmt.Fprintf(&b, "_… and %d more_\n", len(lines)-maxLines)
			break
		}
		if n := said[l]; n > 1 {
			l += fmt.Sprintf(" (×%d)", n)
		}
		b.WriteString(l + "\n")
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

// receipt renders one ARRIVAL as a single greppable line. The format is the
// interface — it is what an operator greps out of the log — so it is fixed.
//
// It says RECEIVED, not DELIVERED. The old wording sat exactly where a person
// looks for proof that a page landed, and answered a different question.
func receipt(path string, p *webhook, a alert) string {
	return fmt.Sprintf(
		"ALERT-RECEIVED path=%s receiver=%s status=%s alert=%s severity=%s "+
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
// in the order the records were made.
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
