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

// publishable.go — the PUBLISHABLE KEY (pk-…): how a browser beacon carrying no
// bearer still attributes to its tenant, and the error lens that reads those rows
// back.
//
//	GET /v1/errors   recent type:'error' events for the org (read lens)
//
// ONE publishable key, and IAM issues it. pk- is publishable, sk- is secret, and
// there is no third thing. It is minted at POST /v1/keys with
// {"type":"publishable"} — the same resource that mints a secret key, because the
// type is a field on the key, not a second endpoint.
//
// Until that existed, nothing anywhere produced a pk- and nothing here could
// RESOLVE one: OrgForKey sent every prefix to IAM's get-user?accessKey, which
// refuses a publishable key by design. So the credential this whole file is written
// around could be neither obtained nor honored, and every surface configured its
// own thing instead.
//
// It has no INGEST door of its own either: ingestKey below is one of the carriers
// eventTenant consults, so a pk- caller presents it to /v1/event like every other
// credential. There used to be a POST /v1/ingest that existed only to say "pk- goes
// here"; @hanzo/event 0.3.0 repointed onto /v1/event and it was deleted.
//
// This file used to mint and verify its OWN pk_ (underscore) under an
// HMAC of CLOUD_INGEST_KEY_SECRET, with its own mint endpoint — a second
// publishable-key family sitting beside the one IAM already owned. The underscore
// was load-bearing back then: pk_ was deliberately
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
// door funnels through the SAME ingest core (ingestEvents) onto the SAME event
// plane: one ingest path, many front doors.
package analytics

import (
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// publishablePrefix marks a write-only ingest key. Underscore (not the dash of
// the isAPIKey family) is load-bearing: it keeps pk- OUT of the bearer/principal
// path, so a publishable key is structurally read-incapable.
const publishablePrefix = cloud.PublishablePrefix

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

// ── the captured error ───────────────────────────────────────────────────────

// Exception is the captured error carried on a type:'error' event (mirrors
// @hanzo/event's Exception). It becomes event.error's own columns — class, message,
// handled and the frames.* arrays — so a stack frame is QUERYABLE rather than buried
// in an opaque blob ("which file throws most" is a GROUP BY, not a JSON scan).
type Exception struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
	Handled *bool  `json:"handled,omitempty"`
	// Frames is the STRUCTURED stack, when the client can send one — it holds the
	// source map, so its frames beat anything parsed out of the text Stack here.
	// Absent, parseStack (fact.go) reads the text form every browser SDK emits.
	Frames []Frame `json:"frames,omitempty"`
}

// Frame is one structured stack frame on the wire.
type Frame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     uint32 `json:"line"`
	Column   uint32 `json:"column"`
}

// ── handlers ─────────────────────────────────────────────────────────────────
