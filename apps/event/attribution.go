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

// attribution.go — what a publishable key names, and the client that answers
// which one.
//
// A key is minted with a project (apps/projects) and this is where a beacon
// carrying it is turned back into (org, project). The projects app owns the row,
// the ingest endpoint reads it, and they are not the same process in production —
// the pod boots one process per app — so this is the same two-resolver client
// sites.SetResolver already uses: in-process when the store is here, over the
// plane when it is not.

package event

import (
	"context"
	"sync"
)

// Attribution is what a publishable key resolves to: the org that owns the rows, and
// the project that emitted them.
//
// Project is the SERVER's answer to a question the wire also asks — an event
// carries a `product` field naming its emitting surface, and that field is the
// caller's to set. When a key names a project the server's answer wins (see
// attributeProject), which is the difference between a label and an attribution.
type Attribution struct {
	Org     string
	Project string
}

// KeyResolver maps a publishable ingest key to the scope it names.
//
// found=false ⇒ no project holds this key: the honest refusal, and the whole of
// "if the site is missing it stops recording". err ⇒ a real store or transport
// failure, which is NOT a refusal and must not be collapsed into one — a
// transient failure of the owning app would otherwise read exactly like every
// customer's site being deleted at once.
type KeyResolver interface {
	Resolve(ctx context.Context, key string) (Attribution, bool, error)
}

var (
	keyMu       sync.RWMutex
	keyResolver KeyResolver
	// keyFallback is the cross-process answer, and it is a DEFAULT rather than a line
	// some Mount runs: a key names the same org in every process. While build()
	// owned that line, only the analytics binary could turn a project key into an
	// org — every other binary that admits a key (the OpenRouter webhook serves from
	// apps/integrations) resolved it to nothing, and no test in this package could
	// see that, because the package is correct either way.
	keyFallback KeyResolver = planeKeys{}
)

// SetKeyResolver installs the in-process resolver. projects.Mount calls it with
// its store — the no-hop answer when ingest and the project store share a process.
func SetKeyResolver(r KeyResolver) {
	keyMu.Lock()
	keyResolver = r
	keyMu.Unlock()
}

// SetFallbackKeyResolver replaces the cross-process resolver. Nothing has to call
// it to be correct — planeKeys is the default — so it is what a caller reaches for
// to substitute one, which in practice is a test with no fleet to ask.
func SetFallbackKeyResolver(r KeyResolver) {
	keyMu.Lock()
	keyFallback = r
	keyMu.Unlock()
}

func currentKeyResolver() KeyResolver {
	keyMu.RLock()
	r, fb := keyResolver, keyFallback
	keyMu.RUnlock()
	if r != nil {
		return r
	}
	return fb
}

// resolveAttribution answers which project a key names. It reports only found/not —
// a store failure is logged by the resolver and read here as "not resolved",
// because this endpoint's caller is a browser that can do nothing with the
// difference. What it must never do is answer with an org and no project: that
// is the silent misfiling this whole change removes.
func resolveAttribution(ctx context.Context, key string) (Attribution, bool) {
	r := currentKeyResolver()
	if r == nil || key == "" {
		return Attribution{}, false
	}
	at, ok, err := r.Resolve(ctx, key)
	if err != nil || !ok || at.Org == "" {
		return Attribution{}, false
	}
	return at, true
}

// Admit answers what a presented key names — the org whose rows it writes, and the
// project that minted it when a project did. It is the ONE sequence any endpoint
// admits a key by: /v1/event through keyAdmission (event.go), which adds the
// capability an event write also needs, and the OpenRouter webhook
// (apps/integrations) on its own, because a webhook has nothing to grant.
//
// TWO ISSUERS, disjoint rather than a fallback chain: a project key exists only in
// the project store and an IAM key only in IAM, so a lookup in one can never shadow
// the other and the order costs nothing but a miss. Projects are asked first because
// they answer the strictly narrower question — org AND project, where IAM can only
// ever say org, having no project to scope to. An endpoint that asks one issuer
// refuses every key the other minted.
func Admit(ctx context.Context, key string) (Attribution, bool) {
	if at, ok := resolveAttribution(ctx, key); ok {
		return at, true
	}
	if org, ok := resolveKeyOrg(ctx, key); ok {
		return Attribution{Org: org}, true
	}
	return Attribution{}, false
}

// attributeProject stamps the resolved project onto every event, replacing
// whatever the caller put in `product`. The key is the evidence and the body is
// not: a page that ships one project's key cannot file its rows under another's
// name. The pure twin of attribute (public.go), which does the same for identity
// on the reduced lane.
func attributeProject(evs []CaptureEvent, project string) []CaptureEvent {
	if project == "" {
		return evs
	}
	for i := range evs {
		evs[i].Product = project
	}
	return evs
}
