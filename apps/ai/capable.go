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

package ai

// capable.go — where the model capability gate (apps/ai/capability,
// hanzo-inc/cloud#801) is asked. The same two doors as the per-minute limit
// (rate.go), each once per request:
//
//   - HTTP (Capable): installed by apps/zen after Paced and ahead of its Claim, so
//     a chat, a message or a response is held whichever of zen or ai answers it;
//   - the plane (buy, chat_rpc.go): a sibling's completion.
//
// The caller's policy is its org's (settings.Privacy): zero data retention, and the
// region its data must stay in. A request may ask for more than its org does
// (X-Hanzo-ZDR: 1; X-Hanzo-Residency when the org names no region), never less.

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/apps/ai/capability"
	"github.com/hanzoai/cloud/apps/settings"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// The headers a request asks for more than its org's policy with.
const (
	headerZDR       = "X-Hanzo-ZDR"
	headerResidency = "X-Hanzo-Residency"
)

// Capable is the capability gate over every model request this process serves on
// HTTP: a route the request's model would refuse, or the caller's policy forbids,
// is refused before any provider is called, and what can be sent without changing
// what was asked is sent on rewritten.
func Capable() zip.Handler {
	return capability.Handler(capability.Config{
		Path: func(c *zip.Ctx) string { return cloud.RoutePath(c.Path()) },
		Need: func(c *zip.Ctx) capability.Need {
			org, _ := principal.Org(c)
			return need(c.Context(), org, c.Header(headerZDR), c.Header(headerResidency))
		},
		Log: func(c *zip.Ctx, d capability.Decision) {
			org, _ := principal.Org(c)
			logCapability("http", org, d)
		},
	})
}

// screen holds a sibling's request at path for the caller ctx states, as Capable
// holds one on HTTP: the request to send on, or the refusal to answer instead.
func screen(ctx context.Context, path string, in *client.ChatIn) (*client.ChatIn, *client.Chat) {
	if in.ContentType != "" { // multipart (a recording): no model body to read
		return in, nil
	}
	org := cloud.Who(ctx).Org
	out, refused := capability.Screen(path, in.Request, need(ctx, org, "", ""), func(d capability.Decision) {
		logCapability("plane", org, d)
	})
	if refused != nil {
		return nil, &client.Chat{Status: refused.Status, Body: refused.Body}
	}
	sent := *in
	sent.Request = out
	return &sent, nil
}

// need is the caller's policy, read from its org's settings at most once and only
// when a route asks.
func need(ctx context.Context, org, zdr, region string) capability.Need {
	read := sync.OnceValues(func() (bool, string) { return settings.Privacy(ctx, org) })
	return capability.Need{
		ZeroRetention: func() bool {
			if h := strings.ToLower(strings.TrimSpace(zdr)); h == "true" || h == "1" {
				return true
			}
			on, _ := read()
			return on
		},
		Residency: func() string {
			if _, r := read(); r != "" {
				return r
			}
			return strings.ToLower(strings.TrimSpace(region))
		},
	}
}

// logCapability writes one decision: which checks passed, rewrote or blocked the
// route, at the door it came through, for the org that asked.
func logCapability(door, org string, d capability.Decision) {
	slog.Info("ai: capability", append([]any{"door", door, "org", org}, d.Log()...)...)
}

// ReadsThinking says whether served reads the thinking blocks pinned wrote, by their
// capability records: "yes", "no", or "unknown" when either has none. A turn that
// moves models logs it (apps/zen), so a fresh reasoning context the provider would
// have forced anyway reads apart from one that lost reasoning the new model could
// have used.
func ReadsThinking(pinned, served string) string {
	w, ok := capability.Lookup(pinned)
	if !ok {
		return "unknown"
	}
	r, ok := capability.Lookup(served)
	if !ok {
		return "unknown"
	}
	if capability.Reads(w, r) {
		return "yes"
	}
	return "no"
}
