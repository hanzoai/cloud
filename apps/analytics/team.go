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

// team.go — the Hanzo Team SPA's ingest WIRE, and the team session token as an
// ingest CREDENTIAL. Two independent things, which is why they are two functions:
//
//	POST /v1/event (or the sunsetting caller-owned /v1/event/collect)
//	body: [TeamEvent]   -> {accepted, dropped}
//
// THE WIRE. The team SPA is a PUBLISHED bundle (ghcr.io/hanzoai/front), so its
// emitter is a caller fact we adapt to, not a design we choose. It POSTs a BARE
// JSON ARRAY of {event, properties, timestamp, distinct_id} where `event` is a
// closed 7-member enum, `timestamp` is epoch MILLIS as a NUMBER, and the person id
// is snake_case `distinct_id`. Those two keys are exactly what lets the ONE
// canonical decode dispatch it (isTeamArray, event.go) — the shape IS the wire
// id, so the door needs no path of its own. Left alone, the canonical array
// decode would eat it wrong:
//
//	decodeIngest sees the leading '[' and decodes []Event, whose fields are
//	`distinctId` and `time`. Neither key is present, so DistinctID and Time come
//	back EMPTY and Type is left empty by toCapture. canonicalType("") is "event",
//	which is NOT in publicKinds — so on the anonymous lane admitPublic drops the
//	batch WHOLE and the caller gets 200 {"accepted":0,"dropped":N}.
//
// A silent 200 that stores nothing is strictly worse than a 4xx: the SPA's retry
// loop sees ok and discards, and the error the user hit is gone. decodeTeam exists
// so that cannot happen — it names the kind, so the events survive admission.
//
// THE CREDENTIAL is separate and lives in eventTenant with the other three, NOT on
// this door. Trust level is decided once, in handle, for every door (event.go); a
// door that resolved its own tenant would be the drift that design exists to
// prevent. A team session token is a platform credential, so it works on the
// canonical door too — that is the point, not a side effect.
package analytics

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/zap-proto/zip"
)

// sourceTeam tags every row that arrived on the team SPA's door, so "is the team
// pipe live" is a warehouse query (properties.$source = 'team') and not a guess —
// the same closing signal the sunsetting aliases carry.
const sourceTeam = "team"

// teamEvent is ONE element of the team SPA's wire. Four fields; a retried batch also
// carries a top-level `retryCount`, which is ignored here exactly as encoding/json
// ignores any unknown field — the retry counter is the SPA's bookkeeping, not ours.
type teamEvent struct {
	Event      string         `json:"event"`       // AnalyticEventType, a closed 7-member enum
	Properties map[string]any `json:"properties"`  // event metadata; error_* on an error
	Timestamp  int64          `json:"timestamp"`   // epoch MILLIS (Date.now()), not RFC3339
	DistinctID string         `json:"distinct_id"` // snake_case, unlike the canonical wire
}

// teamKind folds the SPA's event enum onto canonicalType's closed set. The mapping is
// total, so no member is silently dropped, and the two members that decide whether the
// pipe works at all under an anonymous caller — error and navigation — land on the two
// kinds publicKinds admits:
//
//	error       -> error     the window/unhandledrejection capture; the whole reason
//	                         this pipe exists. Kept first-class so /v1/errors sees it.
//	navigation  -> pageview  a route change is a pageview.
//	setUser     -> identify  binds properties to a person …
//	setTag      -> identify  … so does a person property …
//	setAlias    -> identify  … so does binding an anonymous id to that person.
//	setGroup    -> group     binds the person to a workspace.
//	customEvent -> event     the open-ended surface; its name rides in properties.
//
// An enum member a NEWER SPA adds also folds to "event" rather than erroring, and
// keeps its raw name (teamName) — a new event kind lands, tagged as what it called
// itself, instead of 400-ing a whole batch of otherwise-good events.
func teamKind(event string) string {
	switch event {
	case "error":
		return "error"
	case "navigation":
		return "pageview"
	case "setUser", "setTag", "setAlias":
		return "identify"
	case "setGroup":
		return "group"
	default: // customEvent, and any member added after this was written
		return "event"
	}
}

// teamName is the stored event name for kind "event". The SPA puts the human name in
// properties.event and leaves the top-level `event` as the enum tag "customEvent", so
// the name has to be lifted out; an unrecognized enum member has no properties.event
// and keeps its own tag. Every other kind takes its name from resolveEventName
// ($pageview / $identify / $group / $error), which is why this returns "" for them.
func teamName(kind string, e teamEvent) string {
	if kind != "event" {
		return ""
	}
	if s, ok := e.Properties["event"].(string); ok {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return strings.TrimSpace(e.Event)
}

// teamString reads a string property and REMOVES it from the map. Removal is the
// point, not a convenience: the three error_* properties are re-homed onto the typed
// Exception so foldException runs them through scrubException. A copy left behind in
// Properties would be stored raw AND handed raw to the destinations fan-out (forward.go
// sees events before the warehouse scrub) — which is precisely the token/PII leak out
// of a stack frame that the scrubber exists to stop.
func teamString(props map[string]any, key string) string {
	s, _ := props[key].(string)
	delete(props, key)
	return strings.TrimSpace(s)
}

// teamException lifts the SPA's flat error_* properties onto the typed Exception the
// ONE pipeline already understands. ingestDecoded folds it into properties.$exception
// (scrubbed) for every lane, so a team error is stored in the SAME shape as an error
// from @hanzo/event and the /v1/errors lens needs no team-specific branch.
func teamException(props map[string]any) *Exception {
	ex := Exception{
		Message: teamString(props, "error_message"),
		Type:    teamString(props, "error_type"),
		Stack:   teamString(props, "error_stack"),
	}
	if ex.Message == "" && ex.Type == "" && ex.Stack == "" {
		return nil
	}
	if ex.Message == "" {
		ex.Message = "Unknown error"
	}
	return &ex
}

// teamTime converts the SPA's epoch-millis number to the RFC3339 string the write core
// parses. A zero/absent timestamp stays EMPTY so clampTS anchors it to server-now,
// which is the same honest default every other wire gets — never 1970.
func teamTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// decodeTeam is the team SPA's wire decoder: the bare JSON array it POSTs → the
// canonical []CaptureEvent the ONE write core consumes. A non-array body is an error
// rather than a best-effort guess — the SPA emits an array unconditionally, so anything
// else is a misconfigured caller and an honest 400 beats a silent empty receipt. An
// empty/whitespace-only body yields no events (an honest empty receipt, not an error),
// matching decodeIngest.
func decodeTeam(body []byte) ([]CaptureEvent, error) {
	i := firstNonWS(body)
	if i >= len(body) {
		return nil, nil
	}
	var raw []teamEvent
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]CaptureEvent, len(raw))
	for j, e := range raw {
		if e.Properties == nil {
			e.Properties = map[string]any{}
		}
		kind := teamKind(e.Event)
		ev := CaptureEvent{
			Type:        kind,
			Event:       teamName(kind, e),
			Timestamp:   teamTime(e.Timestamp),
			DistinctID:  e.DistinctID,
			AnonymousID: anyString(e.Properties["$anonymous_id"]),
			Properties:  e.Properties,
		}
		if kind == "error" {
			ev.Error = teamException(e.Properties)
		}
		out[j] = ev
	}
	return out, nil
}

// anyString is the nil-safe string read for an untyped property value.
func anyString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// ── the team session token as an ingest credential ───────────────────────────

// teamSecretEnv is the HS256 session-signing key clients/team signs team tokens with.
// ONE env var, ONE secret, read here the SAME way team.go reads it — this package
// verifies what that one signs, it does not own a second key.
const teamSecretEnv = "SERVER_SECRET"

// teamSecret returns the signing key, or "" when there is none to trust. It refuses
// the upstream public default literal for the same reason team.go's resolveSecret
// fail-closes on it: with a known key, ANY caller can mint {extra:{org:"victim"}} and
// write into a tenant it has no claim to. No secret ⇒ no team credential ⇒ the caller
// takes the anonymous lane. Fail-closed, and the closed state is still useful.
func teamSecret() string {
	s := os.Getenv(teamSecretEnv)
	if s == "secret" {
		return ""
	}
	return s
}

// teamTenant resolves a Hanzo Team workspace token to the org it names AND the
// capability it carries. It is the fourth entry in eventTenant's trust order and
// behaves like the other three: verified SERVER-SIDE, fail-closed, and the org comes
// from the SIGNED claim — never the body, never the Host.
//
// token.Decode with verify=true checks the HMAC, exp and nbf, so an expired or forged
// token resolves to nothing (and, because teamPresented names it, is REFUSED rather
// than downgraded).
//
// CAPABILITY comes from the signed extra.role via the ONE predicate that reads it,
// token.Privileged: a member writes unprojected, a guest writes PROJECTED into the
// same org. This replaced a pair of string comparisons against extra.guest /
// extra.readonly that were ported from upstream's hasWorkspaceAccess and were INERT
// here — nothing in this repo has ever minted those claims, because upstream sets them
// on guest-LINK tokens, a path this port does not have. The real reduced principal is
// the workspace role, which selectWorkspace now signs. A guard that cannot fire is
// worse than no guard: it reads as protection while a guest holds an owner-shaped
// token.
func teamTenant(c *zip.Ctx) (admission, bool) {
	t, ok := verifyTeam(c)
	if !ok {
		return admission{}, false
	}
	org := t.Org()
	if org == "" {
		// A verified token with no tenant names nothing to write into. Refused rather
		// than admitted with org="", which normalizeEvent would happily store as the
		// tenant column and fanOut would forward under an empty org.
		return admission{}, false
	}
	// subject is the SIGNED account uuid. On the reduced lane it replaces whatever
	// distinct_id the body carried, so a guest attributes its own activity and cannot
	// attribute it to a colleague.
	return admission{org: org, full: t.Privileged(), subject: t.Account}, true
}

// verifyTeam VERIFIES the request's bearer as a team token and returns it. The one
// place this package validates a team credential, so "verified" cannot drift from
// "used".
func verifyTeam(c *zip.Ctx) (*token.Token, bool) {
	secret := teamSecret()
	if secret == "" {
		return nil, false
	}
	raw := teamBearer(c.Header("Authorization"))
	if raw == "" {
		return nil, false
	}
	t, err := token.Decode(raw, secret, true)
	if err != nil {
		return nil, false
	}
	return t, true
}

// teamPresented reports whether the caller presented something SHAPED like a team
// token, without verifying it and without trusting a byte of it. It is what lets
// presented() (event.go) refuse an expired or forged team token with 403 instead of
// silently filing its rows under $public.
//
// The discriminator is the `account` claim: token.Generate requires it and an IAM
// access token does not carry it, so this identifies the credential FAMILY without
// deciding anything about its validity. Decoding with verify=false is safe precisely
// because the answer is never used as authorization — only to choose between "refuse"
// and "project".
func teamPresented(c *zip.Ctx) bool {
	raw := teamBearer(c.Header("Authorization"))
	if raw == "" {
		return false
	}
	t, err := token.Decode(raw, "", false)
	if err != nil {
		return false
	}
	return strings.TrimSpace(t.Account) != ""
}

// teamBearer extracts the token from an "Authorization: Bearer <t>" header (scheme
// case-insensitive). Empty when absent or not a bearer.
func teamBearer(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
