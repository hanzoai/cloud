package cloudflare

// ai.go — Cloudflare Workers AI, WIRED. POST /v1/cloudflare/ai/run/{model} runs a
// CF-hosted model with the org's OWN token and relays the result. Unlike the pure
// passthrough of every other resource here, a run is INFERENCE, so it is priced:
//
//   - USAGE SPINE (one, shared): it meters through cloud.ResourceMeter under provider
//     cloud.AIMeterProvider ("ai") — the SAME product axis and per-scope caps as every
//     LLM call — never a Cloudflare-specific usage label. The org's own token already
//     paid Cloudflare for the compute, so Hanzo debits only the thin BYO routing fee
//     (cloud.BYOInferenceFeeMicros), never the full inference cost (that double-bills).
//   - O11Y (one, shared): it emits ONE gen_ai client span through clients.StartGenAISpan
//     (gen_ai.system = "cloudflare"), the same span plane every LLM/embedding call uses.
//   - PAYER: the HOME org (principal.HomeOrg) is billed, so a SuperAdmin acting in
//     another org spends from the admin ledger — the token, though, is the EFFECTIVE
//     org's. Same split the LLM meter enforces.
//
// A run needs only a validated org (authClient), not org admin: it is gated by BALANCE
// like every model call, not by the admin bit that guards destructive verbs.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// maxAIBody bounds the inbound run body (a prompt, chat messages, or a base64 audio
// clip for ASR). It matches the edge body limit so a request reaching this handler is
// already within it; the explicit cap is defense-in-depth at the subsystem boundary.
const maxAIBody = 16 << 20

// aiModelRE bounds a Workers AI model id. CF ids look like @cf/meta/llama-3.1-8b-instruct
// or @hf/thebloke/… : an optional "@" vendor prefix then "/"-separated segments of
// url-safe chars. Anchored + charset-limited (no %, ?, #, space, control) so a hostile
// value cannot smuggle a scheme, query, fragment, host, or path structure into the
// upstream /accounts/{id}/ai/run/{model} URL.
var aiModelRE = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// aiModel validates the wildcard-captured model and returns it ready to concatenate
// into the CF path. The charset is already url-safe AND the model legitimately
// contains "/" separators, so it is placed RAW (url.PathEscape would corrupt them);
// safety comes from the anchored charset PLUS an explicit reject of any empty / "." /
// ".." segment, which is the only way "/"-joined safe chars could still traverse.
func aiModel(raw string) (string, error) {
	m := strings.TrimSpace(raw)
	if !aiModelRE.MatchString(m) {
		return "", zip.ErrBadRequest("model is invalid")
	}
	for _, seg := range strings.Split(m, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", zip.ErrBadRequest("model is invalid")
		}
	}
	return m, nil
}

// aiUsage is the token accounting a Workers AI text model reports in its result. A
// non-text model (image, embeddings) reports none — the fields stay zero and the
// debit falls back to the pre-call estimate (which is itself zero for non-text input).
type aiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// aiRun runs a Workers AI model and relays the result, metering the BYO fee + emitting
// a gen_ai span. See the file header for the usage/o11y/payer contract.
func aiRun(s *cloud.Service[state], c *zip.Ctx) error {
	cl, org, err := authClient(s, c)
	if err != nil {
		return err
	}
	// Validate the model FIRST (pure, no I/O) so a hostile model fails fast.
	model, err := aiModel(c.Param("*"))
	if err != nil {
		return err
	}
	body := c.Body()
	if len(body) == 0 {
		return zip.ErrBadRequest("a request body is required")
	}
	if len(body) > maxAIBody {
		return zip.ErrBadRequest("request body too large")
	}

	// Billing PAYER = the HOME org (X-User-Owner; falls back to the effective org for a
	// normal caller). Project narrows the scope + its validated cap, exactly as the
	// edge/LLM meters thread it.
	payer := principal.HomeOrg(c)
	project, projectValidated := principal.ValidatedProject(c)

	// BALANCE/FREEZE gate BEFORE any Cloudflare contact. The BYO fee is FLOORED
	// (BYOInferenceFeeMicros), so gateCents is ALWAYS ≥ 1 and the gate ALWAYS runs — even
	// for a non-text modality (audio/image) whose token estimate is 0. A frozen / broke /
	// over-cap org is refused HERE and never proxied to Cloudflare (no discovery, no run).
	// The estimate prices the reservation; the exact debit lands after the call.
	estTokens := cloud.EstTokens(aiPromptText(body))
	gateCents := cloud.MicrosToGateCents(cloud.BYOInferenceFeeMicros(estTokens))
	if err := s.State.aiBill.Gate(c.Context(), payer, project, projectValidated, cloud.AIMeterProvider, gateCents); err != nil {
		return cloud.DenyResource(c, err)
	}

	// Only once the gate passes: resolve the account (may discover) and run.
	acct, err := cl.resolveAccount(c.Context(), org, c)
	if err != nil {
		return err
	}

	// ONE gen_ai span, same plane as every model call. system = "cloudflare" (the
	// serving provider); the model attr carries the specific CF model.
	ctx, span := clients.StartGenAISpan(c.Context(), "cloudflare", "chat", model, org, project)
	defer span.End()

	out, ctype, usage, err := cl.runAI(ctx, acct, model, body)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "workers ai run failed")
		return cfErr(err)
	}

	// Debit the BYO fee on the EXACT tokens the model reported, falling back to the
	// pre-call estimate when it reports none. A non-text modality reports 0 and estimates
	// 0, but BYOInferenceFeeMicros is FLOORED, so the debit is still ≥ the floor — every
	// /ai/run leaves a usage row, never a silent proxied call. MeterUsage forces User/Org
	// to the payer and defaults Provider/Service to "ai", so Workers AI spend sums with
	// LLM spend on the (project, "ai") axis; Model preserves per-model attribution.
	billTokens := usage.TotalTokens
	if billTokens <= 0 {
		billTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if billTokens <= 0 {
		billTokens = estTokens
	}
	span.SetAttributes(
		attribute.String("gen_ai.response.model", model),
		attribute.Int("gen_ai.usage.input_tokens", usage.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", usage.CompletionTokens),
	)
	s.State.aiBill.MeterUsage(payer, "workers-ai", metering.Usage{
		AmountMicros:     cloud.BYOInferenceFeeMicros(billTokens),
		Model:            model,
		Project:          project,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      billTokens,
		RequestID:        c.RequestID(),
		ClientIP:         cloud.ClientIP(c),
	})

	c.SetHeader("Content-Type", ctype)
	return c.Bytes(http.StatusOK, out)
}

// runAI POSTs the run to /accounts/{acct}/ai/run/{model} and returns the response for
// verbatim relay plus any token usage. A text model answers the {success,errors,result}
// envelope (result carries usage) — the result is relayed and usage extracted; a
// non-text model answers raw bytes with its own content type — relayed as-is. Fails
// closed on transport error or an error envelope, token-free.
func (cl *client) runAI(ctx context.Context, acct, model string, body []byte) ([]byte, string, aiUsage, error) {
	var usage aiUsage
	status, ct, data, err := cl.send(ctx, http.MethodPost, "/accounts/"+acct+"/ai/run/"+model, "application/json", bytes.NewReader(body), timeoutAI, maxAIBody)
	if err != nil {
		return nil, "", usage, err
	}

	// A JSON content type is the enveloped text-model path: fail closed, then unwrap
	// result + usage.
	if strings.Contains(ct, "application/json") {
		if e := envErr(status, data); e != nil {
			return nil, "", usage, e
		}
		var wrap struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(data, &wrap) == nil && len(wrap.Result) > 0 {
			var r struct {
				Usage aiUsage `json:"usage"`
			}
			_ = json.Unmarshal(wrap.Result, &r)
			return wrap.Result, "application/json; charset=utf-8", r.Usage, nil
		}
		return data, "application/json; charset=utf-8", usage, nil
	}

	// Non-JSON (image/audio output): relay on 2xx; a non-2xx is an error.
	if status/100 != 2 {
		return nil, "", usage, &cfError{upstream: status, msg: fmt.Sprintf("cloudflare AI error (status %d)", status)}
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	return data, ct, usage, nil
}

// aiPromptText best-effort extracts the input text from a Workers AI run body for the
// PRE-CALL token estimate — the common text shapes ({prompt}, {messages:[{content}]},
// {text} as a string or []string). A body with no recognizable text field (an image or
// audio input) yields "" → a zero estimate → no pre-gate; the post-call debit then uses
// the model's own reported usage. It deliberately does NOT fall back to the raw body
// length: a multi-MB base64 media input would inflate the estimate and could wrongly
// deny a legitimate run.
func aiPromptText(body []byte) string {
	var in struct {
		Prompt   string `json:"prompt"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal(body, &in) != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(in.Prompt)
	for _, m := range in.Messages {
		b.WriteString(m.Content)
	}
	if len(in.Text) > 0 {
		var s string
		if json.Unmarshal(in.Text, &s) == nil {
			b.WriteString(s)
		} else {
			var arr []string
			if json.Unmarshal(in.Text, &arr) == nil {
				for _, t := range arr {
					b.WriteString(t)
				}
			}
		}
	}
	return b.String()
}
