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

// openrouter.go — the door OpenRouter writes its traces through, so what we spend
// there lands in the SAME ledger as everything else we spend.
//
// OpenRouter meters KEYS, not orgs, and hanzo.cloud_usage carried no `openrouter`
// provider at all: every money lens over that table — the summary here, /v1/analytics,
// the admin board, the leaderboard — answered "what did we spend" with everything
// except the upstream. A Broadcast destination (openrouter.ai Settings ▸ Observability)
// POSTs one OTLP trace per generation to this door, and each generation span becomes
// ONE cloud_usage row with provider `openrouter`. No second meter, no second table:
// the same GROUP BY provider that already answers for do-ai and zen now answers for
// the upstream too.
//
// WHICH KEY SPENT IT is the fact only this trace can supply, and it travels in
// `account` — the column that already means "the connection that paid", spelled
// owner/name (platform_fee.go writes provider-owner/provider-name there). The key's
// NAME is a label OpenRouter's own console shows; the secret never appears in the
// trace and is never written or logged.
//
// AUTHENTICATION IS THE INGEST KEY, because Broadcast signs nothing. Its only
// authentication is a Headers map the destination sends verbatim (OpenRouter's
// X-OpenRouter-Signature belongs to the per-job video callback, a different door,
// and Broadcast does not emit it). So the credential is the one cloud already mints:
// an IAM publishable key, resolved through cloud.OrgForKey — the SAME seam /v1/event
// resolves a beacon's key through. A pk- key cannot read anything, which is exactly
// what a third party's destination config should hold. No key, or a key naming no
// org, is 401 and nothing is written.
//
// Raw, not a typed op: the credential is a HEADER and a typed op holds a context, not
// a request (apps/principal). Terminal keeps its 401 intact under the outer /v1 error
// filter, exactly as the forge's push door does.

package usage

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

const (
	openrouterPath = "/v1/usage/openrouter"

	// openrouter is the row's provider label — the value that folds this spend into
	// the GROUP BY provider every money lens already runs.
	openrouter = "openrouter"

	// maxTrace bounds one delivery. Privacy Mode strips prompts and completions;
	// with it off a batch carries them, so the cap is the forge door's 8 MiB.
	maxTrace = 8 << 20

	// otlpError is OTLP's STATUS_CODE_ERROR. Unset (0) and OK (1) both describe a
	// call that answered, which is what `success` means in this ledger.
	otlpError = 2
)

// orgForKey resolves a presented ingest key to its org through the ONE key seam —
// the same binding apps/analytics makes for /v1/event. A value, so a test drives the
// door without IAM.
var orgForKey = cloud.OrgForKey

// init declares the operation. Both bodies are declared as free-form objects and
// their shape is stated in the PROSE, because neither shape is this package's to
// publish as a schema: the request is OpenTelemetry's OTLP/JSON, of which the types
// below decode the subset a usage row is built from, and a partial view published as
// "the" schema is a document that lies. The prose is the whole declaration, the same
// trade apps/destinations makes for the body whose keys the platform picks.
func init() {
	openapi.Register(openrouterPath, "POST", map[string]any{}, map[string]any{})
	openapi.Describe(openrouterPath, "POST",
		"Receive OpenRouter Broadcast traces as usage rows",
		"OpenRouter's spend is invisible to every Hanzo money lens because those lenses read "+
			"hanzo.cloud_usage and OpenRouter meters keys of its own. Point a Broadcast destination "+
			"(Settings ▸ Observability ▸ Webhook) at this door and each generation span becomes ONE "+
			"row in that same ledger with provider `openrouter`, so one query answers what we spend "+
			"everywhere. Enable the Cost and Identity field categories: cost is the money and identity "+
			"carries `openrouter.api_key_name`, which is what says WHICH key spent it — it lands in "+
			"`account` as openrouter/<key name>.\n\n"+
			"AUTHENTICATION IS AN INGEST KEY. Broadcast signs nothing; its only authentication is the "+
			"destination's Headers map, so send a Hanzo publishable key as `Authorization: Bearer pk-…` "+
			"and it resolves through the same seam /v1/event resolves a beacon's key through. That key "+
			"names the org every row is filed under; it can write and cannot read. No key, or a key that "+
			"names no org, is 401 and nothing is stored.\n\n"+
			"The body is OTLP/JSON — `{resourceSpans:[{scopeSpans:[{spans:[…]}]}]}` — exactly as "+
			"OpenTelemetry defines it; the model, tokens and cost are read from each span's `gen_ai.*` "+
			"attributes and the key name from `openrouter.api_key_name`. The answer is "+
			"`{stored, dropped}`: how many generations became rows, and how many spans named no model. "+
			"Those are OpenRouter's trace and span parents — they carry no cost to meter. An empty "+
			"payload stores nothing and answers 200, which is what makes Test Connection pass. A "+
			"warehouse that cannot take the rows answers 503 so the delivery shows red and can be "+
			"replayed: a row is keyed by its span id, so a redelivery collapses rather than "+
			"double-counting.")
}

// receipt is what the door answers.
type receipt struct {
	// Stored is how many usage rows this delivery wrote.
	Stored int `json:"stored"`
	// Dropped is how many spans named no generation to meter.
	Dropped int `json:"dropped"`
}

// trace is the Broadcast wire: OTLP/JSON. Only what a usage row is built from is
// decoded; the rest of the payload is ignored rather than mirrored.
type trace struct {
	// ResourceSpans groups spans by the resource that produced them. OpenRouter
	// sends one, naming itself in service.name.
	ResourceSpans []struct {
		ScopeSpans []struct {
			Spans []span `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

// span is one OTLP span. A generation names a model; the parents OpenRouter wraps it
// in do not, which is how usageRow tells them apart.
type span struct {
	// SpanID identifies this generation, and becomes both the row's id and its
	// request id — so a redelivery replaces the row instead of doubling the spend.
	SpanID string `json:"spanId"`
	// Start is the generation's own instant, nanoseconds since the epoch, which the
	// row is filed under. A span that names none is filed at receipt.
	Start num `json:"startTimeUnixNano"`
	// Attributes carry the whole of the row: gen_ai.* for the model, tokens and
	// cost, openrouter.* for the key that spent it.
	Attributes []attr `json:"attributes"`
	// Status is how the call ended. Only OTLP's error code makes the row an error;
	// unset and ok both describe a call that answered.
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

// attr is one OTLP key/value.
type attr struct {
	// Key is the attribute's name, e.g. gen_ai.usage.total_cost.
	Key string `json:"key"`
	// Value is the attribute's value in OTLP's tagged form. An integer arrives as a
	// JSON string per the spec, or as a number; both are read.
	Value struct {
		Text *string  `json:"stringValue"`
		Int  *num     `json:"intValue"`
		Real *float64 `json:"doubleValue"`
	} `json:"value"`
}

// num is an OTLP integer. The spec encodes int64 as a JSON STRING and senders also
// emit it as a number; both spell the same integer, so both decode.
type num int64

func (n *num) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	if err != nil {
		return err
	}
	*n = num(v)
	return nil
}

// receive verifies one Broadcast delivery and files its generations.
func receive(s *cloud.Service[state], c *zip.Ctx) error {
	// THE CREDENTIAL IS READ FIRST, before the body is touched: a caller holding no
	// key never buys a decode.
	org, ok := orgForKey(c.Context(), bearer(c))
	if !ok {
		return zip.Errorf(http.StatusUnauthorized,
			"an ingest key is required: send it as Authorization: Bearer")
	}
	body := c.Body()
	if len(body) > maxTrace {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	var t trace
	// Test Connection posts an empty payload; nothing to store is not a failure.
	if len(body) > 0 {
		if err := json.Unmarshal(body, &t); err != nil {
			return zip.ErrBadRequest("invalid OTLP payload")
		}
	}
	var rows [][]any
	dropped := 0
	now := time.Now()
	for _, rs := range t.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				row, ok := usageRow(org, sp, now)
				if !ok {
					dropped++
					continue
				}
				rows = append(rows, row)
			}
		}
	}
	if len(rows) > 0 {
		if err := datastore.EnsureCloudUsage(c.Context()); err != nil {
			s.Log.Warn("openrouter usage: no warehouse", "err", err)
			return zip.Errorf(http.StatusServiceUnavailable, "warehouse unavailable")
		}
		for i, row := range rows {
			if err := datastore.Exec(c.Context(), openrouterInsert, row...); err != nil {
				s.Log.Warn("openrouter usage insert", "err", err, "stored", i, "offered", len(rows))
				return zip.Errorf(http.StatusServiceUnavailable, "warehouse unavailable")
			}
		}
	}
	return c.JSON(http.StatusOK, receipt{Stored: len(rows), Dropped: dropped})
}

// bearer reads the ingest key. Broadcast sends a fixed Headers map, so there is ONE
// carrier — the Authorization header every other keyed caller already uses.
func bearer(c *zip.Ctx) string {
	parts := strings.SplitN(strings.TrimSpace(c.Header("authorization")), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// openrouterInsert is the ONE statement this door writes, and its column order is the
// argument order usageRow binds. It names a SUBSET of hanzo.cloud_usage — the columns
// an upstream trace can honestly fill — and leaves the rest at the table's own
// defaults, the way the DO invoice rows are written.
const openrouterInsert = `INSERT INTO hanzo.cloud_usage (
  id, timestamp, owner, user_id, organization, model, provider, request_id,
  prompt_tokens, completion_tokens, total_tokens, cache_read_tokens,
  cost_cents, cost_nano, billed_nano, currency, status, error_msg, account
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// usageRow renders ONE generation span as openrouterInsert's bound arguments. Pure,
// so the whole mapping is tested without a warehouse.
//
// A span naming no model is not a generation — OpenRouter wraps generations in trace
// and span parents that carry no model, no tokens and no cost — and is dropped rather
// than stored as an empty row.
func usageRow(org string, s span, now time.Time) ([]any, bool) {
	m := values(s.Attributes)
	model := text(m, "gen_ai.response.model", "gen_ai.request.model")
	if model == "" {
		return nil, false
	}
	in := tokens(m, "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens")
	out := tokens(m, "gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens")
	total := tokens(m, "gen_ai.usage.total_tokens")
	if total == 0 {
		total = in + out
	}
	// Nano-USD is the money of record — 1 USD = 1e9 nano — and cost_cents is this ONE
	// row rendered in cents by the SAME rounding datastore.Spend applies to a set, so
	// a row and a sum agree. billed == cost and margin stays 0: this is money we spent
	// upstream and passed through, not resold, exactly like the DO invoice rows.
	nano := int64(math.Round(money(m, "gen_ai.usage.total_cost", "gen_ai.usage.cost") * 1e9))
	if nano < 0 {
		nano = 0
	}
	at := now.UTC()
	if s.Start > 0 {
		at = time.Unix(0, int64(s.Start)).UTC()
	}
	status, msg := "success", ""
	if s.Status.Code == otlpError {
		status, msg = "error", s.Status.Message
	}
	return []any{
		s.SpanID, at,
		org, text(m, "user.id", "openrouter.user_id"), org,
		model, openrouter, s.SpanID,
		uint32(in), uint32(out), uint32(total),
		uint32(tokens(m, "gen_ai.usage.input_tokens.cached")),
		uint64(cents(nano)), nano, nano, "usd",
		status, msg,
		account(text(m, "openrouter.api_key_name")),
	}, true
}

// account names WHICH key spent the money — the whole point of the identity category,
// and the one fact nothing else in the estate can supply. owner/name is the grammar
// `account` already carries.
func account(key string) string {
	if key == "" {
		return openrouter
	}
	return openrouter + "/" + key
}

// cents renders nano-USD as whole cents by the SAME integer rounding datastore.Spend
// applies over a set — half a cent up, never float — so one row and a sum of rows
// cannot disagree.
func cents(nano int64) int64 { return (nano + 5_000_000) / 10_000_000 }

// values folds a span's attributes into one lookup. OTLP carries them as a list and
// every field below is read by name, so the fold happens once per span.
func values(as []attr) map[string]attr {
	m := make(map[string]attr, len(as))
	for _, a := range as {
		m[a.Key] = a
	}
	return m
}

// text reads the first named string attribute. The names are alternatives for ONE
// fact — OpenRouter spells the served model gen_ai.response.model and the asked-for
// one gen_ai.request.model — so the fallback is a list, not a chain of ifs.
func text(m map[string]attr, keys ...string) string {
	for _, k := range keys {
		if a, ok := m[k]; ok && a.Value.Text != nil {
			return strings.TrimSpace(*a.Value.Text)
		}
	}
	return ""
}

// tokens reads the first named integer attribute, accepting the double a sender may
// use for the same count. Negative is not a count: a warehouse column is unsigned and
// wrapping one would corrupt every total that reads it.
func tokens(m map[string]attr, keys ...string) int64 {
	for _, k := range keys {
		a, ok := m[k]
		if !ok {
			continue
		}
		switch {
		case a.Value.Int != nil:
			return max(int64(*a.Value.Int), 0)
		case a.Value.Real != nil:
			return max(int64(math.Round(*a.Value.Real)), 0)
		}
	}
	return 0
}

// money reads the first named USD amount. OpenRouter sends cost as a double; a sender
// rounding it to an integer means the same dollars.
func money(m map[string]attr, keys ...string) float64 {
	for _, k := range keys {
		a, ok := m[k]
		if !ok {
			continue
		}
		switch {
		case a.Value.Real != nil:
			return *a.Value.Real
		case a.Value.Int != nil:
			return float64(*a.Value.Int)
		}
	}
	return 0
}
