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

// rate.go — where the per-minute limit (limits.Pace) is asked. Two doors reach a
// model in this process, and each asks it once per request:
//
//   - HTTP (Paced): every model request on /v1 — the API, the chat apps, a Zen SKU
//     (zen mounts it ahead of its Claim), and the MCP door's aiChat and aiDecide,
//     which re-enter this router with the real route (calls.go);
//   - the plane (buy, chat_rpc.go): a sibling's call — coding runs, bots, agents.
//
// Both count the person the boundary attested, client.Person: "<home org>/<name>",
// the key the free allowance counts by, so a browser session, an API key and a run
// the person started spend one window. A caller the boundary cannot place in an org
// is counted by its user id, and one it cannot name at all by its address; the two
// public routes, which ask for no account, by address alone (pacer). Nobody is
// admitted uncounted.
//
// Retrieval is not counted (an embedding or a rerank is what the platform runs many
// of to answer one question, the rule the free allowance keeps), nor a push to or
// the close of a live transcript, which its open was counted as.
//
// Every model response carries where the caller stands, in OpenAI's headers, from
// the count the limit decided by and before the first byte, a stream's included:
// x-ratelimit-limit-requests, x-ratelimit-remaining-requests and
// x-ratelimit-reset-requests (a duration, "1s", "1m0s"). A refusal is 429 with
// Retry-After in whole seconds and the same three.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/principal"
	aictl "github.com/hanzoai/ai/controllers"
	"github.com/zap-proto/zip"
)

// The headers a model response says where its caller stands in, OpenAI's names.
const (
	headerRateLimit     = "x-ratelimit-limit-requests"
	headerRateRemaining = "x-ratelimit-remaining-requests"
	headerRateReset     = "x-ratelimit-reset-requests"
)

// Paced is the per-minute limit over every model request this process serves on
// HTTP. It is installed first, ahead of zen's Claim and every route ai mounts, so a
// request is counted once whichever answers it.
func Paced(lim *limits.Limiter) zip.Handler {
	return func(c *zip.Ctx) error {
		path := cloud.RoutePath(c.Path())
		open := public(path)
		if c.Method() != http.MethodPost || !(open || paced(path)) {
			return c.Continue()
		}
		if _, ok := aictl.TranscriptModel(c.Method(), path, c.Header("Authorization")); ok {
			return c.Continue() // a live transcript's push or close: its open was counted
		}
		org, _ := principal.Org(c)
		p := lim.Pace(c.Context(), pacer(zip.CallerOf(c.Forward()), geo.IP(c), open), org)
		if p.Limit == 0 {
			return c.Continue()
		}
		stand(c.SetHeader, p)
		if !p.Admitted {
			c.SetHeader("Retry-After", strconv.Itoa(wait(p.Reset)))
			c.SetHeader("Content-Type", "application/json")
			return c.Bytes(http.StatusTooManyRequests, refusal(p))
		}
		return c.Continue()
	}
}

// pace asks the limit for a sibling's call at path, for the caller ctx states, and
// answers the refusal when it is past it; nil when the call goes on.
func pace(ctx context.Context, lim *limits.Limiter, path string) *client.Chat {
	if lim == nil || !paced(path) {
		return nil
	}
	who := cloud.Who(ctx)
	if p := lim.Pace(ctx, pacer(who, who.IP, false), who.Org); !p.Admitted {
		return &client.Chat{Status: http.StatusTooManyRequests, Body: refusal(p)}
	}
	return nil
}

// public reports whether path is a model route that asks for no account: a visitor's
// chat and a visitor's minute of audio, each bounded further in its handler by a
// daily allowance per visitor.
func public(path string) bool {
	switch strings.ToLower(strings.TrimRight(path, "/")) {
	case "/v1/chat/public", "/v1/audio/transcriptions/public":
		return true
	}
	return false
}

// pacer is whose window a request spends. The person the boundary attested; for a
// caller it placed in no org, its user id; for one it could not name, the address the
// request came from. A route that asks for no account is counted by address alone,
// whoever is signed in: the visitor is the address. A person is "<org>/<name>", an
// org is a slug with no colon, so the prefixed keys are no person's.
func pacer(who zip.Caller, ip string, open bool) string {
	if open {
		return addr(ip)
	}
	if person := client.Person(who); person != "" {
		return person
	}
	if user := strings.TrimSpace(who.User); user != "" {
		return "user:" + user
	}
	return addr(ip)
}

// addr is the key an address is counted by, empty when there is none.
func addr(ip string) string {
	if ip = strings.TrimSpace(ip); ip == "" {
		return ""
	}
	return "ip:" + ip
}

// paced reports whether a request at path asks a model to answer: a priced
// endpoint the usage policy decides (hanzoai/ai controllers.Entitled), retrieval
// aside.
func paced(path string) bool {
	switch strings.ToLower(strings.TrimRight(path, "/")) {
	case "/v1/embeddings", "/v1/rerank":
		return false
	}
	return aictl.Entitled(path)
}

// stand writes where the caller stands against the limit.
func stand(set func(k, v string), p limits.Pace) {
	set(headerRateLimit, strconv.Itoa(p.Limit))
	set(headerRateRemaining, strconv.Itoa(p.Remaining))
	set(headerRateReset, (time.Duration(wait(p.Reset)) * time.Second).String())
}

// wait is a window's time to its turn in whole seconds, rounded up and never zero,
// so a client that waits for it never arrives early.
func wait(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

// refusal is the 429's body, OpenAI's shape: the code clients switch on and when to
// come back. It states no figure; the headers carry the limit.
func refusal(p limits.Pace) []byte {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	body.Error.Message = fmt.Sprintf("You have made as many model requests as one person may in a minute. Retry after %ds.", wait(p.Reset))
	body.Error.Type = "rate_limit_error"
	body.Error.Code = limits.CodeRate
	raw, _ := json.Marshal(body)
	return raw
}
