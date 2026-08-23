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

// openrouter.go — the endpoint OpenRouter writes its traces through, so what we
// spend there lands in the SAME ledger as everything else we spend.
//
// It is an INTEGRATION, and it lives with the others: a third party posts to us at
// /v1/integrations/<vendor>/<what the vendor calls it>, which is why Slack's is
// /events, Discord's is /interactions and this one is /webhook — OpenRouter's own
// console calls it Observability ▸ New Webhook Destination. That the rows it writes
// are usage rows is the DESTINATION and not the endpoint.
//
// OpenRouter meters KEYS, not orgs, and hanzo.cloud_usage carried no `openrouter`
// provider at all: every money lens over that table — /v1/usage, /v1/event, the
// admin board, the leaderboard — answered "what did we spend" with everything except
// the upstream. A Broadcast destination POSTs one OTLP trace per generation here, and
// each generation span becomes ONE cloud_usage row with provider `openrouter`. No
// second meter, no second table: the same GROUP BY provider that already answers for
// do-ai and zen now answers for the upstream too.
//
// WHICH KEY SPENT IT is the fact only this trace can supply, and it travels in
// `account` — the column that already means "the connection that paid", spelled
// owner/name (platform_fee.go writes provider-owner/provider-name there). The key's
// NAME is a label OpenRouter's own console shows; the secret never appears in the
// trace and is never written or logged.
//
// AUTHENTICATION IS A HANZO KEY, because Broadcast signs nothing. Its only
// authentication is a Headers map the destination sends verbatim (OpenRouter's
// X-OpenRouter-Signature belongs to the per-job video callback, a different
// endpoint, and Broadcast does not emit it). So the credential is one cloud already
// mints, and it is admitted by the ONE sequence every keyed endpoint admits by —
// event.Admit, which asks BOTH issuers: the project store a key minted with a
// project lives in, then IAM. Either names the org every row is filed under, and an
// endpoint that asks one issuer refuses every key the other minted. No key, or a
// key naming no org, is 401 and nothing is read.
//
// Raw, not a typed op, and the reason is the BODY: a typed op publishes its In as THE
// request schema, and this decodes the SUBSET of OpenTelemetry's OTLP/JSON a usage row
// is built from — published as the contract, that subset is a document that lies about
// a wire OpenTelemetry defines. Terminal keeps the 401 intact under the outer /v1
// error filter, exactly as the sibling webhooks' rejects are kept.

package integrations

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/event"
	"github.com/zap-proto/zip"
)

const (
	openrouterPath = "/v1/integrations/openrouter/webhook"

	// openrouter is the row's provider label — the value that folds this spend into
	// the GROUP BY provider every money lens already runs.
	openrouter = "openrouter"

	// maxTrace bounds one delivery. Privacy Mode strips prompts and completions;
	// with it off a batch carries them, so the cap is the forge endpoint's 8 MiB.
	maxTrace = 8 << 20

	// otlpError is OTLP's STATUS_CODE_ERROR. Unset (0) and OK (1) both describe a
	// call that answered, which is what `success` means in this ledger.
	otlpError = 2
)

// receipt is what the endpoint answers.
type receipt struct {
	// Stored is how many usage rows this delivery wrote.
	Stored int `json:"stored"`
	// Dropped is how many spans named no generation to meter.
	Dropped int `json:"dropped"`
}

// otlp is the Broadcast wire: OTLP/JSON. Only what a usage row is built from is
// decoded; the rest of the payload is ignored rather than mirrored.
type otlp struct {
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

// openrouterWebhook admits one Broadcast delivery and files its generations.
func openrouterWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	// THE CREDENTIAL IS READ FIRST, before the body is touched: a caller holding no
	// key never buys a decode.
	at, ok := event.Admit(c.Context(), presented(c))
	if !ok {
		return zip.Errorf(http.StatusUnauthorized,
			"a Hanzo key is required: send it as Authorization: Bearer")
	}
	body := c.Body()
	if len(body) > maxTrace {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	var t otlp
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
				row, ok := usageRow(at.Org, sp, now)
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

// presented reads the key off the request. Broadcast sends a fixed Headers map, so
// there is ONE carrier — the Authorization header every other keyed caller uses.
func presented(c *zip.Ctx) string {
	parts := strings.SplitN(strings.TrimSpace(c.Header("authorization")), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// openrouterInsert is the ONE statement this endpoint writes, and its column order is
// the argument order usageRow binds. It names a SUBSET of hanzo.cloud_usage — the columns
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
