// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// errorsink.go — Sentinel's consumer of event.error.
//
// /v1/event is the ONE way an error enters. The ingest scrubs it, computes its
// grouping key (`issue`) and publishes it on event.error; the warehouse durable lands
// it in event.fact, which is what sentry.hanzo.ai reads. This file binds a SECOND
// durable on the same subject, beside the insights bridge, and does the one thing a
// fact cannot: it keeps the ISSUE ROW — first seen, count, status, regression —
// through the embedded Sentinel module, and announces a NEW issue once on the alert
// egress chain (alerts.go).
//
// A CONSUMER, NOT A STEP. Ingest and landing never wait on it, so a slow or absent
// Sentinel costs an issue list its freshness and nothing else. A fact the module
// cannot file goes back to the stream after a pause and is filed when it can be.
//
// IT RUNS IN THIS PROCESS because this is the process that holds the runtime
// (embed.go). The ingest runs in the event app's process, which is why the error
// fan-out is a stream and not an in-process sink: a sink registered here was never
// handed a single error.

package o11y

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/o11y/pkg/modules/sentry"
	"github.com/hanzoai/o11y/pkg/types/errortrackingtypes"
)

const (
	// sentryLensEnv turns the consumer OFF when falsey. Default ON.
	sentryLensEnv = "CLOUD_SENTRY_LENS"

	// sentinelDurable is this consumer on the EVENT stream: one copy of every error
	// fact, shared across replicas and kept across restarts.
	sentinelDurable = "event-sentinel"
	// sentinelClient names the connection in the bus's own monitoring.
	sentinelClient = "o11y-sentinel"
	// sentinelRetry is the pause before a fact that could not be filed goes back.
	sentinelRetry = 2 * time.Second
	// sentinelTimeout bounds filing one fact and announcing it.
	sentinelTimeout = 15 * time.Second
	// sentinelAckWait is how long the bus waits on one fact before redelivering it.
	sentinelAckWait = 60 * time.Second

	// sentinelHostEnv is the face a new-issue announcement links to.
	sentinelHostEnv     = "CLOUD_SENTINEL_URL"
	defaultSentinelHost = "https://sentry.hanzo.ai"

	// sentinelAnnounceOrgsEnv is an optional comma-separated allowlist of orgs
	// whose new Sentinel issues are broadcast on the operator alert egress chain
	// ("*" announces every org). Unset, only the operator's alerts org
	// (CLOUD_ALERTS_ORG, default "hanzo") is announced — customer tenants' issues
	// are still ingested into Sentinel without paging Hanzo's internal inbox.
	sentinelAnnounceOrgsEnv = "CLOUD_SENTINEL_ANNOUNCE_ORGS"
)

// sentryLensEnabled reports whether the consumer runs (default ON).
func sentryLensEnabled() bool {
	switch strings.ToLower(env.Or(sentryLensEnv, "")) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// sentinel is the running durable; nil until installErrorSink started it.
var sentinel struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// installErrorSink starts the event.error consumer. It needs the in-process
// runtime's Sentinel module, so mountRuntime calls it only when the embed is up; the
// reverse-proxy fallback files no issues and errors still land in event.fact.
func installErrorSink(log luxlog.Logger) {
	if !sentryLensEnabled() {
		log.Info("sentinel consumer disabled", "flag", sentryLensEnv)
		return
	}
	if embeddedRuntime == nil || embeddedRuntime.Modules.Sentry == nil {
		log.Info("sentinel consumer inactive (no in-process runtime; errors stay on the event plane)")
		return
	}
	module := embeddedRuntime.Modules.Sentry
	sentinel.mu.Lock()
	defer sentinel.mu.Unlock()
	if sentinel.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	sentinel.cancel, sentinel.done = cancel, done
	go func() {
		defer close(done)
		runSentinel(ctx, log, module, bus.URL())
	}()
}

// clearErrorSink stops the consumer and waits for the fact in hand. Idempotent.
func clearErrorSink() {
	sentinel.mu.Lock()
	cancel, done := sentinel.cancel, sentinel.done
	sentinel.cancel, sentinel.done = nil, nil
	sentinel.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// runSentinel connects and consumes until ctx ends, reconnecting after a pause when
// the bus or the durable goes away. It never fails the mount.
func runSentinel(ctx context.Context, log luxlog.Logger, module sentry.Module, url string) {
	for ctx.Err() == nil {
		cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{
			URL: url, Name: sentinelClient, EnableJetStream: true, MaxReconnects: -1,
		})
		if err != nil {
			log.Warn("sentinel: bus connect failed, retrying", "url", bus.Display(), "err", err)
			pause(ctx, 5*time.Second)
			continue
		}
		if err := bindSentinel(ctx, log, module, cl); err != nil && ctx.Err() == nil {
			log.Warn("sentinel: consume ended, rebinding", "err", err)
		}
		_ = cl.Close()
		pause(ctx, 2*time.Second)
	}
}

// bindSentinel creates the durable, filtered to event.error, and consumes it.
func bindSentinel(ctx context.Context, log luxlog.Logger, module sentry.Module, cl *infra.PubSubClient) error {
	if err := event.EnsureEventStream(ctx, cl); err != nil {
		return err
	}
	if _, err := cl.CreateConsumer(ctx, event.EventStream, &infra.ConsumerConfig{
		Name:          sentinelDurable,
		Durable:       sentinelDurable,
		Description:   "file " + event.ErrorSubject + " into Sentinel's issues",
		FilterSubject: event.ErrorSubject,
		DeliverPolicy: infra.DeliverNew,
		AckPolicy:     infra.AckExplicit,
		AckWait:       sentinelAckWait,
	}); err != nil {
		return err
	}
	log.Info("sentinel: consuming", "stream", event.EventStream, "subject", event.ErrorSubject, "durable", sentinelDurable)
	return cl.ConsumeMessages(ctx, event.EventStream, sentinelDurable, func(sm *infra.StreamMessage) error {
		return fileFault(ctx, log, module, sm.Data)
	})
}

// fileFault files ONE message. nil acks it; an error sends it back to the stream after
// sentinelRetry. A message that is not an error fact is acked unread, and so is one
// that panics — redelivering a panic only repeats it.
func fileFault(ctx context.Context, log luxlog.Logger, module sentry.Module, data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn("sentinel: fact panicked, dropped", "err", r)
			err = nil
		}
	}()
	f, ok := event.DecodeFault(data)
	if !ok || f.Issue == "" {
		return nil
	}
	fctx, cancel := context.WithTimeout(ctx, sentinelTimeout)
	defer cancel()
	created, err := module.Ingest(fctx, f.Org, f.Product, []*errortrackingtypes.Occurrence{occurrence(f)})
	if err != nil {
		log.Warn("sentinel: filing failed, will retry", "org", f.Org, "issue", f.Issue, "err", err)
		pause(ctx, sentinelRetry)
		return err
	}
	for _, issue := range created {
		announceIssue(fctx, log, f, issue)
	}
	return nil
}

// occurrence is a Fault in the issue lifecycle's vocabulary. The fingerprint is the
// fact's own `issue`, so the issue row and the facts behind it share one key and
// the face reads an issue's occurrences as `WHERE issue = <fingerprint>`.
func occurrence(f event.Fault) *errortrackingtypes.Occurrence {
	occ := &errortrackingtypes.Occurrence{
		EventID:     f.ID,
		Fingerprint: f.Issue,
		Type:        cmp.Or(f.Class, "Error"),
		Value:       f.Message,
		Culprit:     cmp.Or(f.Origin, f.Path),
		Level:       cmp.Or(f.Level, "error"),
		Timestamp:   f.Time,
		Environment: f.Env,
		Release:     f.Release,
		ServiceName: cmp.Or(f.Service, f.Product),
		Transaction: f.Path,
		TraceID:     f.Trace,
		SpanID:      f.Span,
	}
	for _, fr := range f.Frames {
		occ.Frames = append(occ.Frames, errortrackingtypes.Frame{
			Function: fr.Function, Filename: fr.File, Lineno: int(fr.Line), Colno: int(fr.Column), InApp: fr.Own,
		})
	}
	if f.Person != "" {
		occ.User = &errortrackingtypes.EventUser{ID: f.Person}
	}
	return occ
}

// shouldAnnounceOrg reports whether a new Sentinel issue in org should be
// broadcast on the operator alert egress chain. Every org's faults are still
// ingested into Sentinel; only first-party/configured orgs page the operator.
func shouldAnnounceOrg(org string) bool {
	org = strings.TrimSpace(org)
	if org == "" {
		return false
	}
	if raw := strings.TrimSpace(env.Or(sentinelAnnounceOrgsEnv, "")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			p := strings.TrimSpace(part)
			if p == "*" || strings.EqualFold(p, org) {
				return true
			}
		}
		return false
	}
	want := strings.TrimSpace(env.Or(alertsOrgEnv, ""))
	if want == "" {
		want = defaultAlertsOrg
	}
	return strings.EqualFold(org, want)
}

// announceIssue tells a human about a NEW issue, once, on the same egress chain an
// alert takes. A failed announcement is logged and does not refile the fact: the
// issue row is already written, and filing twice would count the failure twice.
func announceIssue(ctx context.Context, log luxlog.Logger, f event.Fault, issue *errortrackingtypes.Issue) {
	if !shouldAnnounceOrg(f.Org) {
		return
	}
	text := newIssueText(f, issue, env.Or(sentinelHostEnv, defaultSentinelHost))
	via, failures := announce(ctx, text)
	if via == "" {
		log.Warn("sentinel: new issue not announced", "org", f.Org, "issue", f.Issue, "reason", reason(failures))
		return
	}
	log.Info("sentinel: new issue announced", "org", f.Org, "issue", f.Issue, "via", via)
}

// newIssueText is the announcement: what failed, where, in which build, and where to
// look. Pure, so the words are asserted in a test.
func newIssueText(f event.Fault, issue *errortrackingtypes.Issue, host string) string {
	title := issue.Type
	if issue.Value != "" {
		title += ": " + issue.Value
	}
	where := f.Org
	if f.Product != "" {
		where += "/" + f.Product
	}
	var b strings.Builder
	fmt.Fprintf(&b, "New error in %s — %s", where, capText(title, 300))
	if f.Env != "" || f.Release != "" {
		fmt.Fprintf(&b, "\n%s %s", cmp.Or(f.Env, "-"), cmp.Or(f.Release, "-"))
	}
	if issue.Culprit != "" {
		fmt.Fprintf(&b, "\nat %s", capText(issue.Culprit, 200))
	}
	fmt.Fprintf(&b, "\n%s", strings.TrimRight(host, "/"))
	return b.String()
}

// capText bounds one line of the announcement.
func capText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// pause waits d or until ctx ends.
func pause(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
