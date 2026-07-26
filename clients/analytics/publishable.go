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

// publishable.go — the public capture path: a PUBLISHABLE KEY (pk-…) attributes
// a browser beacon to its tenant and writes straight to the datastore.
//
//	POST /v1/ingest   body: {batch:[WireEvent]}   auth: pk-…   -> {accepted,dropped}
//	GET  /v1/errors   recent type:'error' events for the org (read lens)
//
// ONE publishable key, and IAM issues it. pk- is publishable, sk- is secret, and
// there is no third thing.
//
// This file used to mint and verify its OWN pk_ (underscore) under an
// HMAC of CLOUD_INGEST_KEY_SECRET, with its own mint endpoint at
// /v1/ingest/keys — a second publishable-key family sitting beside the one IAM
// already owned. The underscore was load-bearing back then: pk_ was deliberately
// kept OUT of isAPIKey's set, because anything isAPIKey resolved into "the same
// principal a JWT yields", and a key meant for a browser bundle must not read.
//
// That is fixed at the boundary instead of routed around: IdentityFromRequest now
// refuses a pk- outright (cloud.IsPublishableKey), so publishable means
// publishable no matter which door it arrives at. A pk- stays inside
// APIKeyPrefixes on purpose — OrgForKey must resolve it to learn which tenant a
// beacon belongs to. Resolvable, not authenticating.
//
// The tenant is whatever IAM resolves the key to, never a body or header claim,
// so the tenant invariant the rest of the plane enforces holds here too. Every
// door funnels through the SAME write core (ingestEvents) into the SAME
// hanzo.events table: one write path, many front doors.
package analytics

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// ingestKeySecretEnv names the KMS-injected HMAC secret that seals a publishable

// publishablePrefix marks a write-only ingest key. Underscore (not the dash of
// the isAPIKey family) is load-bearing: it keeps pk- OUT of the bearer/principal
// path, so a publishable key is structurally read-incapable.
const publishablePrefix = cloud.PublishablePrefix

// sourceIngest tags rows that arrived via the publishable-key direct ingest, so
// the ONE hanzo.events table stays honest about origin (queryable as
// properties.$source) without a second table — same mechanism as the other
// adapters (sourceEvent/sourcePostHog/sourceCapture).
const sourceIngest = "ingest"

// sigBytes is the HMAC truncation length (128 bits) — ample against forgery while

// ── key codec (pure) ─────────────────────────────────────────────────────────

// maxIngestOrgLen bounds a decoded org (it becomes a warehouse partition key),
// mirroring the cap OrgForKey applies to an IAM-resolved owner.
const maxIngestOrgLen = 128

// ── request key extraction ───────────────────────────────────────────────────

// ingestKey pulls the presented publishable key, in priority order: the
// Authorization: Bearer header (the common browser-fetch shape), the
// x-hanzo-ingest-key header, then the ?ingest_key= query (navigator.sendBeacon
// cannot set headers). Only a pk--prefixed value is returned — an unrelated
// bearer (a real JWT/IAM key) is ignored here so this door never shadows the
// identity path. "" when none is present.
func ingestKey(c *zip.Ctx) string {
	if auth := strings.TrimSpace(c.Header("authorization")); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); strings.HasPrefix(k, publishablePrefix) {
				return k
			}
		}
	}
	if k := strings.TrimSpace(c.Header("x-hanzo-ingest-key")); strings.HasPrefix(k, publishablePrefix) {
		return k
	}
	if k := strings.TrimSpace(c.Query("ingest_key")); strings.HasPrefix(k, publishablePrefix) {
		return k
	}
	return ""
}

// ── error (exception) folding ────────────────────────────────────────────────

// Exception is the captured error carried on a type:'error' WireEvent (mirrors
// @hanzo/event's Exception). The ingest folds it into properties.$exception so
// the ONE events schema needs no new columns and the /v1/errors lens can surface
// it straight from the properties JSON.
type Exception struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
	Handled *bool  `json:"handled,omitempty"`
}

// foldException normalizes a type:'error' event so the write core stores it as a
// first-class error: it defaults the type to "error", and lifts the top-level
// `error` object into properties.$exception (never mutating the caller's map).
// A non-error event passes through unchanged.
func foldException(e CaptureEvent) CaptureEvent {
	if e.Error == nil {
		return e
	}
	if strings.TrimSpace(e.Type) == "" {
		e.Type = "error"
	}
	props := make(map[string]any, len(e.Properties)+1)
	for k, v := range e.Properties {
		props[k] = v
	}
	// Redact the exception's free-text (message/stack) at the fold point so the
	// stored row AND the raw destinations fan-out (forward.go, which sees events
	// BEFORE the warehouse scrub) both carry a clean $exception — never a token,
	// query secret, or PII lifted from a stack frame.
	props["$exception"] = scrubException(e.Error)
	e.Properties = props
	e.Error = nil
	return e
}

// ── handlers ─────────────────────────────────────────────────────────────────

// ingest answers POST /v1/ingest — a THIN DEPRECATED ALIAS of the canonical door.
// Since /v1/event now natively accepts the publishable key (pk-…, via eventTenant)
// AND the {batch:[…]} wire (via decodeIngest), /v1/ingest is redundant: it delegates
// to the EXACT canonical handler logic (eventHandle) — the SAME pluggable auth,
// tolerant decode, error-fold, and ONE write core — differing only in a one-shot
// deprecation log and the $source=ingest origin tag for the migration signal.
// Existing pk- callers keep working unchanged; there is ONE implementation.
func ingest(s *cloud.Service[state], c *zip.Ctx) error {
	deprecated(s, c, "/v1/event")
	return eventHandle(c, sourceIngest)
}

// errorsLens answers GET /v1/errors — the error-tracking read view: recent
// type:'error' events for the org, newest first. Tenant-scoped server-side and
// gated on a VALIDATED principal (tenant()), NOT the publishable key — reads
// require real auth, reinforcing that pk- is write-only. The captured exception
// is surfaced straight from properties.$exception. limit defaults 50, caps 200.
func errorsLens(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := aiobject.DatastoreQuery(c.Context(), `
		SELECT id, timestamp, event, distinct_id, session_id, product, url, path,
		       library, library_version, properties
		FROM hanzo.events
		WHERE tenant_id = ? AND event_type = 'error'
		ORDER BY timestamp DESC
		LIMIT ?`, org, limit)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	type errEvent struct {
		ID         string          `json:"id"`
		Timestamp  string          `json:"timestamp"`
		Event      string          `json:"event"`
		DistinctID string          `json:"distinctId,omitempty"`
		SessionID  string          `json:"sessionId,omitempty"`
		Product    string          `json:"product,omitempty"`
		URL        string          `json:"url,omitempty"`
		Path       string          `json:"path,omitempty"`
		Library    string          `json:"library,omitempty"`
		LibraryVer string          `json:"libraryVersion,omitempty"`
		Exception  json.RawMessage `json:"exception,omitempty"`
		Properties json.RawMessage `json:"properties,omitempty"`
	}
	out := make([]errEvent, 0, len(rows))
	for _, r := range rows {
		e := errEvent{
			ID: asStr(r["id"]), Timestamp: asStr(r["timestamp"]), Event: asStr(r["event"]),
			DistinctID: asStr(r["distinct_id"]), SessionID: asStr(r["session_id"]),
			Product: asStr(r["product"]), URL: asStr(r["url"]), Path: asStr(r["path"]),
			Library: asStr(r["library"]), LibraryVer: asStr(r["library_version"]),
		}
		if p := asStr(r["properties"]); p != "" && json.Valid([]byte(p)) {
			e.Properties = json.RawMessage(p)
			e.Exception = extractException(p)
		}
		out = append(out, e)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// extractException pulls the $exception object out of a properties JSON blob so
// the errors lens surfaces it as a first-class field. "" (nil) when absent.
func extractException(props string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(props), &m) != nil {
		return nil
	}
	if ex, ok := m["$exception"]; ok {
		return ex
	}
	return nil
}
