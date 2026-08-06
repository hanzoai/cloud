// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// spansink.go is the LLM-observability projection of the canonical event plane: it
// consumes the analytics span fan-out (analytics.AddSpanSink) and lands each LLM-shaped
// span-signal fact on event.span, so a /v1/event span surfaces in the LLM views —
// GET /v1/o11y/llm/{observations,traces,sessions,users} and the eval board's per-model
// rollup — beside the gen_ai spans the ai emit path sends over the ZAP wire. It is the
// exact sibling of errorsink.go, one signal over.
//
// WHAT THE READ SIDE ACTUALLY IS, and why this writes a ROW rather than calling a module:
// hanzoai/o11y's llmobs.Module has NO ingest surface — its ten methods are four List
// views plus score/annotation CRUD. Its own words: "observations/traces/sessions/users
// are VIEWS OVER gen_ai SPANS, while scores and annotations are CRUD over two net-new
// tables". Every one of those four views is a querier read of SignalTraces — event.span
// (telemetrytraces.DBName) — filtered by `gen_ai.system EXISTS AND
// gen_ai.hanzo.org_id = <caller's org>` (impllmobs.genAIFilter). So the ONLY way a span
// becomes an observation is to be a row of event.span carrying those attributes. There
// is no Ingest to call, and inventing a second store for LLM spans is the writer this
// platform already deleted (see datastoreSink's note in planesink.go).
//
// WHY ONLY gen_ai SPANS: `gen_ai.system EXISTS` is the read side's own canonical marker
// (llmobstypes.GenAISystem — "the canonical marker that a span is an LLM call"), so this
// lens filters on exactly the predicate the reader filters on. A span without it is
// invisible to every LLM view by construction, so projecting one would be write
// amplification that no read can ever return — and it would put a second copy of a
// non-LLM span on the trace plane, which the ZAP door already owns. One marker, decided
// once, on both sides.
//
// TENANCY (fail-closed, no state): gen_ai.hanzo.org_id is the ONLY org discriminator the
// span views have, and it is the tenant SLUG — the handler sets ViewQuery.OrgSlug from
// the validated X-Org-Id (the JWT `owner`) and binds it as an equality. That is the same
// value analytics resolved from the principal, so it is stamped here verbatim, LAST and
// unconditionally, and no derivation is involved.
// This lens deliberately does NOT use deriveOrgUUID or a canonical project: those are
// the SENTRY module's contract (Ingest takes an org UUID and a project UUID); the span
// views have neither concept, and a UUID in either place would be read by nobody — the
// reader binds the slug, so a UUID-stamped row is a row every LLM view returns zero of.
// TestSpanTenantIsTheSlugNotTheUUID pins that against the errorsink formula next door.
//
// ONE WRITE PATH: rows go through (*planeSink).insertSpans, never to the datastore
// directly, because that function owes event.span AND the event.trace partial it
// implies. A span written without its partial is a span the trace list cannot find:
// the querier resolves every trace_id predicate against the summary first and
// SHORT-CIRCUITS TO EMPTY when it finds no row. This lens is the third producer on that
// one path, beside the ZAP receiver and the in-process SDK sink.
//
// FAIL-SOFT (additive projection): the fact publish already committed and the honest
// receipt already returned before this runs, on a detached goroutine. Every failure —
// no datastore, saturated, insert error — is logged and swallowed; it can never fail or
// slow an ingest.
package o11y

import (
	"context"
	"os"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/hanzoai/o11y/pkg/types/llmobstypes"
)

const (
	// llmLensEnv turns the LLM span projection OFF when falsey. Default ON, the same
	// posture as the Sentry error lens next door: a live plane sink projects every
	// org's /v1/event gen_ai spans onto event.span.
	llmLensEnv = "CLOUD_LLM_LENS"

	// maxSpanFanout bounds concurrent projection work so a span burst cannot spawn
	// unbounded datastore writes; excess batches are dropped (fail-soft), mirroring
	// the error lens.
	maxSpanFanout = 32
	// spanSinkTimeout bounds one batch's insert.
	spanSinkTimeout = 15 * time.Second
)

// spanSinkSem bounds concurrent projection work (drop-on-saturation, fail-soft).
var spanSinkSem = make(chan struct{}, maxSpanFanout)

// llmLensEnabled reports whether the span projection is on (default ON).
func llmLensEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(llmLensEnv))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// installSpanSink wires the analytics span fan-out to the event.span writer. It is a
// no-op (and installs NO sink) unless the plane sink is up — without a datastore
// connection there is nothing to write to, so spans then simply stay in event.fact
// (honest degradation, never a failure).
//
// It installs from mountPlaneIngest, where the error lens installs from mountRuntime,
// and that is the SAME rule read against a different dependency: a lens is installed
// where the thing it writes through becomes available. The Sentry lens calls
// Modules.Sentry, which the embedded runtime owns; this one calls insertSpans, which the
// plane sink owns and which mountPlaneIngest sets AFTER mountRuntime has already run.
// Installing this from mountRuntime would read a nil embeddedPlaneSink every time and
// log "inactive" on a healthy process. It is also the honest dependency: the LLM views
// read event.span whether this binary serves them in-process or the standalone runtime
// does, so the projection is worth writing on both backings.
func installSpanSink(log luxlog.Logger) {
	if !llmLensEnabled() {
		log.Info("llm span lens disabled", "flag", llmLensEnv)
		return
	}
	if embeddedPlaneSink == nil {
		log.Info("llm span lens inactive (no plane sink; spans stay on the event plane)")
		return
	}
	removeSpanSink = analytics.AddSpanSink(func(org string, spans []analytics.SpanEvent) { consumeSpans(log, org, spans) })
	log.Info("llm span lens installed", "sink", planeSpanTable)
}

// removeSpanSink is the installed sink's remover — nil until installSpanSink ran.
var removeSpanSink func()

// clearSpanSink detaches the span fan-out. Idempotent; safe when never installed.
func clearSpanSink() {
	if removeSpanSink != nil {
		removeSpanSink()
		removeSpanSink = nil
	}
}

// consumeSpans is the installed span-sink handler. It runs on the goroutine analytics
// detached, so it may do bounded synchronous work here. Every path is fail-soft: it
// NEVER returns to a caller and NEVER propagates an error to the ingest.
func consumeSpans(log luxlog.Logger, org string, spans []analytics.SpanEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn("llm span-sink panic", "org", org, "err", r)
		}
	}()
	ps := embeddedPlaneSink
	if ps == nil || len(spans) == 0 || org == "" {
		return
	}

	// Only the LLM-shaped spans become rows, so a batch of ordinary spans costs one
	// pass and no write at all.
	rows := make([][]any, 0, len(spans))
	for _, s := range spans {
		if row, ok := spanRow(org, s); ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return
	}

	// Bound concurrent projection; drop (fail-soft) when saturated.
	select {
	case spanSinkSem <- struct{}{}:
		defer func() { <-spanSinkSem }()
	default:
		log.Warn("llm span-sink saturated; dropping batch", "org", org, "spans", len(rows))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), spanSinkTimeout)
	defer cancel()
	if err := ps.insertSpans(ctx, log, rows); err != nil {
		log.Warn("llm span-sink insert failed", "org", org, "spans", len(rows), "err", err)
	}
}

// spanRow renders one analytics.SpanEvent as an event.span row in planeSpanColumns
// order, or (nil,false) when the span is not an LLM call. Pure — no I/O — so the
// mapping is unit-tested, exactly as buildSentryEvent is.
func spanRow(org string, s analytics.SpanEvent) ([]any, bool) {
	attrs, ok := genAIAttributes(org, s)
	if !ok {
		return nil, false
	}
	// event.span's identity is (org, trace_id, time, id) and id IS the span id, so a
	// span that named none takes the message id — which the fan-out already minted when
	// the wire carried no idempotency key. Without that fallback every unnamed span in
	// an org would share the identity "" and the ReplacingMergeTree would collapse them
	// all into one row.
	spanID := firstNonEmpty(s.SpanID, s.MessageID)
	// A span that names no trace is its OWN single-span trace, not a member of the
	// anonymous "" trace: the traces view groups by trace_id, so sharing the empty
	// string would fold every orphan LLM call in the org into one bogus trace.
	traceID := firstNonEmpty(s.TraceID, spanID)
	return []any{
		org,
		s.Time.UTC(),
		spanID,
		s.Name,
		planeSpanKind(s.Kind),
		// The emitting workload, then the surface — the SAME precedence the error lens
		// resolves service_name with, so one span and one error from one caller name
		// their origin identically.
		firstNonEmpty(s.Service, s.Product),
		traceID,
		spanID,
		s.Parent,
		s.Duration,
		planeStatus(s.Status),
		attrs,
	}, true
}

// genAIAttributes renders the span's attribute map and decides whether the span is an
// LLM call at all. The properties travel verbatim — gen_ai.request.model,
// gen_ai.usage.input_tokens, _o11y.gen_ai.total_cost and the rest are read straight out
// of this map by the views, so the projection must not rename them — through the same
// value renderer the ZAP path uses, which keeps a JSON whole number reading as "123"
// rather than "123.0" where the views parse it back as a float. They arrive already
// scrubbed: the plane's ONE scrubber ran on the analytics side, where the write core's
// own storage rule lives.
//
// An attribute that renders empty is dropped rather than stored blank, which is also
// what makes the marker test below agree with the reader: a Map(String,String) returns
// the empty string for an absent key, so a key stored empty is a key that satisfies
// `EXISTS` while naming no provider — a row every view returns and none can explain.
func genAIAttributes(org string, s analytics.SpanEvent) (map[string]string, bool) {
	attrs := make(map[string]string, len(s.Properties)+6)
	for k, v := range s.Properties {
		if str := attrString(v); str != "" {
			attrs[k] = str
		}
	}
	// THE MARKER, and it is the reader's own: llmobstypes.GenAISystem is what
	// `gen_ai.system EXISTS` tests, so a span this lens admits is exactly a span the
	// LLM views return.
	if attrs[llmobstypes.GenAISystem] == "" {
		return nil, false
	}

	// The qualifiers event.span has no column for, under their OTel spellings so a
	// projected span reads like every other span on the plane.
	fillAttr(attrs, "service.version", s.Release)
	fillAttr(attrs, "deployment.environment", s.Environment)
	fillAttr(attrs, "site", s.Site)
	// The two keys the sessions and users views GROUP BY. The SPAN'S OWN statement is
	// authoritative here — session.id/user.id are the read side's canonical vocabulary,
	// not a legacy $ spelling the envelope supersedes — so the envelope's identity only
	// fills them when the span named none.
	fillAttr(attrs, llmobstypes.SessionID, s.SessionID)
	fillAttr(attrs, llmobstypes.UserID, s.DistinctID)

	// THE TENANT, STAMPED LAST AND UNCONDITIONALLY. This one key is the whole tenant
	// boundary of every /v1/evals read, so it is never taken from the wire: a
	// client-supplied value would let one org write rows another org reads. org is the
	// server-resolved analytics tenant and it overwrites whatever the properties held.
	attrs[llmobstypes.GenAIHanzoOrgID] = org
	return attrs, true
}

// fillAttr sets a descriptive attribute the envelope knows, without ever overwriting
// what the span itself stated.
func fillAttr(attrs map[string]string, key, value string) {
	if value != "" && attrs[key] == "" {
		attrs[key] = value
	}
}
