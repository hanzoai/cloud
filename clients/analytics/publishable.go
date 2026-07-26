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

// publishable.go — the write-only PUBLISHABLE-KEY ingest path (a key meant to ship
// in a browser bundle), plus the interim HMAC key's fast direct-verify.
//
//	POST /v1/ingest        body: {batch:[WireEvent]}   auth: pk_ | pk-  -> {accepted,dropped}
//	POST /v1/ingest/keys   DEPRECATED (410) — issuance moved to IAM (see mintKey)
//	GET  /v1/errors        recent type:'error' events for the org (read lens)
//
// TWO publishable-key generations coexist during migration; BOTH are write-only and
// browser-shippable, and both resolve to an org for INGEST only — never a principal:
//
//   - pk- (IAM-issued, the TARGET). IAM — the ONE key authority — mints, scopes
//     (scope=="publish"), lists, and revokes it. cloud resolves it through the ONE key
//     seam (cloud.OrgForKey → IAM resolve-key). It IS in the isAPIKey family, yet its
//     read-incapability is guaranteed at the identity boundary: validatedPrincipal
//     REFUSES a pk- a principal, so it can never mint X-User-Id and can never read.
//   - pk_ (interim HMAC, being DEPRECATED). The org is sealed under
//     HMAC-SHA256(CLOUD_INGEST_KEY_SECRET, org); verification is one HMAC compute —
//     no IAM call, no DB read. The `pk_` (underscore) prefix is deliberately NOT in
//     isAPIKey's set, so the identity boundary and OrgForKey both refuse it — its ONLY
//     door is the verifier below. Write-only is a property of WHICH resolver accepts
//     the value. Its self-mint is retired (issuance moved to IAM); VERIFY stays live
//     so already-issued keys keep working until every site migrates.
//
// For BOTH: the server stamps tenant_id from the RESOLVED/VERIFIED org, never from the
// request body — the same tenant invariant the rest of the plane enforces — and every
// row funnels through the SAME write core (ingestEvents) into the SAME hanzo.events
// table. One write path, many front doors.
//
// SECRET: the interim HMAC secret is CLOUD_INGEST_KEY_SECRET (KMS-injected). Absent ⇒
// verify fails closed (403); a deployment without it never admits an unverifiable pk_.
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

// publishablePrefix marks the INTERIM write-only ingest key (pk_, HMAC-sealed).
// Underscore (not the dash of the isAPIKey family) is load-bearing: it keeps pk_ OUT
// of the bearer/principal path, so this key is structurally read-incapable. It is
// being deprecated in favor of the IAM-issued pk- (iamPublishablePrefix); verify
// stays live for already-issued keys (see verifyPublishableKey).
const publishablePrefix = "pk_"

// iamPublishablePrefix marks the IAM-issued write-only PUBLISHABLE key (pk-, dash).
// IAM — the ONE key authority — mints, scopes (scope=="publish"), lists, and revokes
// it; cloud resolves it to an org through the ONE key seam (cloud.OrgForKey → IAM
// resolve-key) for INGEST only. Read-incapability is enforced at the identity
// boundary (validatedPrincipal refuses a pk- a principal), NOT by the prefix alone —
// so unlike the interim pk_, this key IS in the isAPIKey family yet still cannot read.
const iamPublishablePrefix = "pk-"

// isPublishable reports whether k is a write-only publishable key presented for
// ingest — the interim HMAC key (pk_) OR the IAM-issued write-only key (pk-). Both
// are browser-shippable and resolve to an org for INGEST only, never a principal.
// (hk-/sk- are read-capable and travel the projectKey path instead.)
func isPublishable(k string) bool {
	return strings.HasPrefix(k, publishablePrefix) || strings.HasPrefix(k, iamPublishablePrefix)
}

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
//
// Deprecated: cloud is NOT a key authority — publishable-key ISSUANCE has moved to
// IAM (a write-only pk- minted by the console key manager / IAM POST /v1/iam/key,
// scope=publish). This self-mint is retired: POST /v1/ingest/keys (mintKey) no
// longer calls it and returns 410 pointing at IAM. It is retained ONLY to exercise
// the pk_ verify-compat path (verifyPublishableKey) that keeps already-issued keys
// working until every site migrates; it will be removed post-migration.
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

// ingestKey pulls the presented publishable key (interim pk_ OR IAM pk-), in
// priority order: the Authorization: Bearer header (the common browser-fetch shape,
// the SAME slot @hanzo/event uses for either key generation), the x-hanzo-ingest-key
// header, then the ?ingest_key= query (navigator.sendBeacon cannot set headers). Only
// a publishable-shaped value (pk_/pk-) is returned — an unrelated bearer (a real JWT,
// or a read-capable hk-/sk- key) is ignored here so this door never shadows the
// identity path. "" when none is present.
func ingestKey(c *zip.Ctx) string {
	if auth := strings.TrimSpace(c.Header("authorization")); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); isPublishable(k) {
				return k
			}
		}
	}
	if k := strings.TrimSpace(c.Header("x-hanzo-ingest-key")); isPublishable(k) {
		return k
	}
	if k := strings.TrimSpace(c.Query("ingest_key")); isPublishable(k) {
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
	props["$exception"] = e.Error
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

// iamKeyIssuance is the successor pointer stamped on the deprecated mint endpoint —
// where publishable-key issuance now lives (IAM, the ONE key authority).
const iamKeyIssuance = "IAM key issuance (console key manager / POST /v1/iam/key, scope=publish)"

// mintKey answers POST /v1/ingest/keys — DEPRECATED. cloud is NOT a key authority:
// publishable-key ISSUANCE has moved to IAM, which mints a write-only pk- that is
// org-scoped, listable, and revocable there. So cloud's self-mint (the
// CLOUD_INGEST_KEY_SECRET HMAC pk_) is retired — this returns 410 Gone with a pointer
// to the successor and mints NOTHING new. Already-issued pk_ keep working unchanged:
// the verify path (eventTenant → verifyPublishableKey) is untouched, so no live site
// breaks; only NEW issuance moves to IAM. A validated principal is still required so
// the deprecation notice never leaks to an anonymous caller.
func mintKey(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := tenant(c); !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	deprecated(s, c, iamKeyIssuance)
	c.SetHeader("Deprecation", "true")
	c.SetHeader("Link", "<"+iamKeyIssuance+">; rel=\"successor-version\"")
	return zip.Errorf(http.StatusGone,
		"cloud no longer mints publishable keys — issue a write-only pk- via %s", iamKeyIssuance)
}

// errorsLens answers GET /v1/errors — the error-tracking read view: recent
// type:'error' events for the org, newest first. Tenant-scoped server-side and
// gated on a VALIDATED principal (tenant()), NEVER a publishable key — reads require
// real auth, reinforcing that a publishable key (pk_ / pk-) is write-only: neither
// can satisfy tenant() (the identity boundary refuses both a principal), so a browser
// key can never read this lens. The captured exception is surfaced straight from
// properties.$exception. limit defaults 50, caps 200.
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
