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

// publishable.go — the FASTEST capture path: a write-only PUBLISHABLE KEY (pk_…)
// that authenticates a direct-to-datastore ingest with ZERO network hop.
//
//	POST /v1/ingest        body: {batch:[WireEvent]}   auth: pk_…   -> {accepted,dropped}
//	POST /v1/ingest/keys   mint a pk_ for the caller's org (validated principal)
//	GET  /v1/errors        recent type:'error' events for the org (read lens)
//
// WHY a distinct key from the IAM hk-/sk-/pk- family: those resolve through IAM
// (get-user?accessKey — a network round-trip) and mint a FULL principal that can
// READ. A publishable key is meant to ship in a browser bundle, so it must be
// write-only and cheap to verify. This key is:
//
//   - INGEST-ONLY BY CONSTRUCTION. The `pk_` (underscore) prefix is deliberately
//     NOT in isAPIKey's set (hk-/sk-/pk-/fw_/hz_, all dash/`fw_`/`hz_`), so the
//     identity boundary (SanitizeIdentity) and OrgForKey both REFUSE it — it can
//     never become a bearer principal, so it can never read. Its only door is the
//     ingest verifier below. Write-only is a property of WHICH resolver accepts
//     the value, not a flag on a row.
//   - ORG-SCOPED, SIGNED, NON-FORGEABLE. The org is carried in the key but sealed
//     under HMAC-SHA256(secret, org): a client cannot flip the org without the
//     secret. The server stamps tenant_id from the VERIFIED org, never from the
//     request body — the same tenant invariant the rest of the plane enforces.
//   - LOWEST LATENCY. Verification is one HMAC compute — no IAM call, no keys
//     table, no DB read. This is the no-Kafka, no-bridge, direct-to-Datastore
//     path; it funnels through the SAME write core (ingestEvents) into the SAME
//     hanzo.events table as every other adapter. One write path, many front doors.
//
// SECRET: the HMAC secret is CLOUD_INGEST_KEY_SECRET (KMS-injected by the
// operator). Absent ⇒ mint and verify BOTH fail closed (503 / 403) — a deployment
// without the secret never mints a forgeable key nor admits an unverifiable one.
package analytics

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// ingestKeySecretEnv names the KMS-injected HMAC secret that seals a publishable
// key's org. Absent ⇒ the publishable-key path is disabled (fails closed).
const ingestKeySecretEnv = "CLOUD_INGEST_KEY_SECRET"

// publishablePrefix marks a write-only ingest key. Underscore (not the dash of
// the isAPIKey family) is load-bearing: it keeps pk_ OUT of the bearer/principal
// path, so a publishable key is structurally read-incapable.
const publishablePrefix = "pk_"

// sourceIngest tags rows that arrived via the publishable-key direct ingest, so
// the ONE hanzo.events table stays honest about origin (queryable as
// properties.$source) without a second table — same mechanism as the other
// adapters (sourceEvent/sourcePostHog/sourceCapture).
const sourceIngest = "ingest"

// sigBytes is the HMAC truncation length (128 bits) — ample against forgery while
// keeping the key short enough to embed in a bundle.
const sigBytes = 16

// ── key codec (pure) ─────────────────────────────────────────────────────────

// ingestSecret returns the configured HMAC secret, or "" when unset (path off).
func ingestSecret() string { return strings.TrimSpace(os.Getenv(ingestKeySecretEnv)) }

// keySig computes the org signature under the secret: HMAC-SHA256(secret, org),
// truncated to sigBytes. The org is the only signed input — the tenant a key can
// ever write into is fixed at mint time and cannot be shifted without the secret.
func keySig(secret, org string) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(org))
	return m.Sum(nil)[:sigBytes]
}

// mintPublishableKey mints "pk_<b64url(org)>.<b64url(sig)>" for org under secret.
// '.' is the delimiter because it is OUTSIDE the base64url alphabet (which uses
// '-' and '_'), so the two segments split unambiguously. Returns ("",false) when
// the secret is unconfigured (fail closed) or org is empty.
func mintPublishableKey(secret, org string) (string, bool) {
	org = strings.TrimSpace(org)
	if secret == "" || org == "" {
		return "", false
	}
	b64 := base64.RawURLEncoding
	return publishablePrefix + b64.EncodeToString([]byte(org)) + "." + b64.EncodeToString(keySig(secret, org)), true
}

// verifyPublishableKey resolves a presented key to its org, or ("",false) if the
// key is not a well-formed, correctly-signed publishable key under the configured
// secret. FAILS CLOSED: unconfigured secret, wrong prefix, malformed segments, or
// a signature mismatch all return not-ok. Constant-time signature compare. Pure:
// no I/O, so tests drive it directly.
func verifyPublishableKey(secret, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if secret == "" || !strings.HasPrefix(key, publishablePrefix) {
		return "", false
	}
	body := key[len(publishablePrefix):]
	dot := strings.IndexByte(body, '.')
	if dot <= 0 || dot == len(body)-1 {
		return "", false
	}
	b64 := base64.RawURLEncoding
	orgBytes, err := b64.DecodeString(body[:dot])
	if err != nil {
		return "", false
	}
	sig, err := b64.DecodeString(body[dot+1:])
	if err != nil {
		return "", false
	}
	org := string(orgBytes)
	if org == "" || len(org) > maxIngestOrgLen {
		return "", false
	}
	if subtle.ConstantTimeCompare(sig, keySig(secret, org)) != 1 {
		return "", false
	}
	return org, true
}

// maxIngestOrgLen bounds a decoded org (it becomes a warehouse partition key),
// mirroring the cap OrgForKey applies to an IAM-resolved owner.
const maxIngestOrgLen = 128

// ── request key extraction ───────────────────────────────────────────────────

// ingestKey pulls the presented publishable key, in priority order: the
// Authorization: Bearer header (the common browser-fetch shape), the
// x-hanzo-ingest-key header, then the ?ingest_key= query (navigator.sendBeacon
// cannot set headers). Only a pk_-prefixed value is returned — an unrelated
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
// Since /v1/event now natively accepts the publishable key (pk_…, via eventTenant)
// AND the {batch:[…]} wire (via decodeIngest), /v1/ingest is redundant: it delegates
// to the EXACT canonical handler logic (eventHandle) — the SAME pluggable auth,
// tolerant decode, error-fold, and ONE write core — differing only in a one-shot
// deprecation log and the $source=ingest origin tag for the migration signal.
// Existing pk_ callers keep working unchanged; there is ONE implementation.
func ingest(s *cloud.Service[state], c *zip.Ctx) error {
	deprecated(s, c, "/v1/event")
	return eventHandle(c, sourceIngest)
}

// mintKey answers POST /v1/ingest/keys — an org owner (VALIDATED principal) mints
// a publishable key for its OWN org. The key is org-scoped to the caller's tenant
// (never a body-supplied org), so a caller can only ever mint a key that writes
// into its own partition. Fails closed (503) when the secret is unconfigured.
func mintKey(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	secret := ingestSecret()
	if secret == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "publishable keys unavailable: ingest key secret not configured")
	}
	key, ok := mintPublishableKey(secret, org)
	if !ok {
		return zip.Errorf(http.StatusServiceUnavailable, "could not mint publishable key")
	}
	return c.JSON(http.StatusOK, map[string]any{"key": key, "org": org, "scope": "ingest"})
}

// errorsLens answers GET /v1/errors — the error-tracking read view: recent
// type:'error' events for the org, newest first. Tenant-scoped server-side and
// gated on a VALIDATED principal (tenant()), NOT the publishable key — reads
// require real auth, reinforcing that pk_ is write-only. The captured exception
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
