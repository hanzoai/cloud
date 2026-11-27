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

package capability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Dialect is the request shape a body is in.
type Dialect int

const (
	// OpenAI is /v1/chat/completions and /v1/responses.
	OpenAI Dialect = iota
	// Anthropic is /v1/messages.
	Anthropic
)

// Verdict is what one check decided.
type Verdict string

const (
	// Pass: the route takes the request as it is.
	Pass Verdict = "pass"
	// Rewrite: the request is sent on without what the route would refuse, and
	// still asks for what it asked for.
	Rewrite Verdict = "rewrite"
	// Block: the request is not dispatched to the route.
	Block Verdict = "block"
)

// The codes a refusal carries.
const (
	// CodeIneligible: the caller's policy forbids the route (retention, residency).
	CodeIneligible = "model_ineligible"
	// CodeToolChoice: the model answers 400 to the forced tool choice asked.
	CodeToolChoice = "tool_choice_unsupported"
)

// Need is what the caller's policy requires of a route. Each is asked at most once
// per request, and only of a route the answer could refuse, so a request to a model
// that keeps nothing reads no policy. A nil field requires nothing.
type Need struct {
	// ZeroRetention reports whether the caller requires zero data retention.
	ZeroRetention func() bool
	// Residency is the region the caller requires its data to stay in ("eu", "us"),
	// "" for none.
	Residency func() string
}

func (n Need) zero() bool { return n.ZeroRetention != nil && n.ZeroRetention() }

func (n Need) region() string {
	if n.Residency == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(n.Residency()))
}

// Check is one check's outcome. A block carries the status, code and parameter its
// refusal answers with.
type Check struct {
	Name    string
	Verdict Verdict
	Why     string
	Status  int
	Code    string
	Param   string
}

// Decision is every check one request met on its way to a route.
type Decision struct {
	// Model is the id the request named.
	Model string
	// Record is the model it resolved to; Route the account that serves it.
	Record, Route string
	// Checks run in one order, every one of them, so a log names each check that
	// would have refused and not only the first.
	Checks []Check
}

// Blocked is the first check that refused the route.
func (d Decision) Blocked() (Check, bool) {
	for _, c := range d.Checks {
		if c.Verdict == Block {
			return c, true
		}
	}
	return Check{}, false
}

// Rewrote reports whether the request is sent on changed.
func (d Decision) Rewrote() bool {
	_, blocked := d.Blocked()
	return !blocked && slices.ContainsFunc(d.Checks, func(c Check) bool { return c.Verdict == Rewrite })
}

// Verdict is the decision as a whole: block over rewrite over pass.
func (d Decision) Verdict() Verdict {
	if _, blocked := d.Blocked(); blocked {
		return Block
	}
	if d.Rewrote() {
		return Rewrite
	}
	return Pass
}

// Summary is each check and its verdict, in the order they ran.
func (d Decision) Summary() string {
	parts := make([]string, 0, len(d.Checks))
	for _, c := range d.Checks {
		parts = append(parts, c.Name+"="+string(c.Verdict))
	}
	return strings.Join(parts, " ")
}

// Log is the decision as a log record's key-value pairs: the model asked, the record
// and route it resolved to, the verdict, every check, and why each that did not pass.
func (d Decision) Log() []any {
	kv := []any{"model", d.Model, "record", d.Record, "route", d.Route, "verdict", string(d.Verdict()), "checks", d.Summary()}
	var why []string
	for _, c := range d.Checks {
		if c.Verdict != Pass {
			why = append(why, c.Name+": "+c.Why)
		}
	}
	if len(why) > 0 {
		kv = append(kv, "why", strings.Join(why, "; "))
	}
	return kv
}

// Gate holds one request body against the record of the model it names and the
// caller's need. out is the body to send on: the same bytes unless a check rewrote
// it, and the same bytes when a check blocked. ok is false for a body the gate has
// no say over (unreadable, naming no model, or a model with no record); it is then
// sent on untouched and the route answers it as before.
func Gate(body []byte, dialect Dialect, need Need) (out []byte, d Decision, ok bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return body, Decision{}, false
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return body, Decision{}, false
	}
	r, found := Lookup(model)
	if !found {
		return body, Decision{Model: model}, false
	}
	d = Decision{Model: model, Record: r.Model, Route: r.Route}
	changed := false

	d.Checks = append(d.Checks, retention(model, r, need))
	d.Checks = append(d.Checks, residency(model, r, need))
	d.Checks = append(d.Checks, toolChoice(model, r, fields["tool_choice"]))

	c := Check{Name: "sampling", Verdict: Pass, Why: "no sampling parameter it refuses"}
	var dropped []string
	for _, p := range r.Sampling.Rejects {
		if _, sent := fields[p]; sent {
			dropped = append(dropped, p)
			delete(fields, p)
		}
	}
	if len(dropped) > 0 {
		changed = true
		c.Verdict = Rewrite
		c.Why = fmt.Sprintf("dropped %s: %s answers 400 to them and runs its default without them", strings.Join(dropped, ", "), model)
	}
	d.Checks = append(d.Checks, c)

	c = Check{Name: "thinking", Verdict: Pass, Why: "thinking as asked"}
	var thinking struct {
		Type string `json:"type"`
	}
	if raw, sent := fields["thinking"]; sent && json.Unmarshal(raw, &thinking) == nil {
		switch strings.ToLower(thinking.Type) {
		case "disabled":
			if !r.Thinking.Off {
				delete(fields, "thinking")
				changed = true
				c.Verdict = Rewrite
				c.Why = fmt.Sprintf("thinking {type: disabled} dropped: %s always thinks and answers 400 to it, so it runs adaptive", model)
			}
		case "enabled":
			if !r.Thinking.Budget {
				fields["thinking"] = json.RawMessage(`{"type":"adaptive"}`)
				changed = true
				c.Verdict = Rewrite
				c.Why = fmt.Sprintf("a thinking budget became adaptive thinking: %s answers 400 to budget_tokens", model)
			}
		}
	}
	d.Checks = append(d.Checks, c)

	if _, blocked := d.Blocked(); blocked || !changed {
		return body, d, true
	}
	return encode(fields, body), d, true
}

// retention refuses a route that keeps the request when the caller keeps nothing.
func retention(model string, r Record, need Need) Check {
	c := Check{Name: "retention", Verdict: Pass}
	switch {
	case r.Retention.Days == 0:
		c.Why = "serves under zero data retention"
	case r.Retention.ZeroAuthorized:
		c.Why = "zero data retention authorized for this account"
	case need.zero():
		c.Verdict, c.Status, c.Code, c.Param = Block, http.StatusForbidden, CodeIneligible, "model"
		c.Why = fmt.Sprintf("%s keeps every request %d days at its provider (%s), and zero data retention is required here, so it is not served under that policy; choose a model that serves under zero data retention",
			model, r.Retention.Days, r.Residency.Account)
	default:
		c.Why = fmt.Sprintf("keeps %d days; zero data retention not required", r.Retention.Days)
	}
	return c
}

// residency refuses a route that does not keep data where the caller requires.
func residency(model string, r Record, need Need) Check {
	c := Check{Name: "residency", Verdict: Pass, Why: "no residency required"}
	region := need.region()
	switch {
	case region == "":
	case slices.Contains(r.Residency.Regions, region):
		c.Why = "keeps data in " + region
	default:
		c.Verdict, c.Status, c.Code, c.Param = Block, http.StatusForbidden, CodeIneligible, "model"
		where := "states no region it keeps data in"
		if len(r.Residency.Regions) > 0 {
			where = "keeps data in " + strings.Join(r.Residency.Regions, ", ")
		}
		c.Why = fmt.Sprintf("%s is served through %s, which %s, and %s residency is required here, so it is not served under that policy",
			model, r.Residency.Account, where, region)
	}
	return c
}

// toolChoice refuses a forced tool choice the model answers 400 to.
func toolChoice(model string, r Record, raw json.RawMessage) Check {
	c := Check{Name: "tool_choice", Verdict: Pass, Why: "no forced tool choice"}
	if !forced(raw) {
		return c
	}
	if r.Tools.Forced {
		c.Why = "takes a forced tool choice"
		return c
	}
	c.Verdict, c.Status, c.Code, c.Param = Block, http.StatusBadRequest, CodeToolChoice, "tool_choice"
	c.Why = fmt.Sprintf(`%s answers 400 to a forced tool choice ("required", "any" or one named tool); send tool_choice "auto" and name the tool in the prompt`, model)
	return c
}

// forced reports whether a tool_choice forces a call, in either dialect: OpenAI's
// "required", a named function or allowed_tools in mode "required"; Anthropic's
// {"type":"any"} or {"type":"tool"}. "auto" and "none" force nothing.
func forced(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "required" || s == "any"
	}
	var o struct {
		Type string `json:"type"`
		Mode string `json:"mode"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return false
	}
	switch strings.ToLower(o.Type) {
	case "", "auto", "none":
		return false
	case "allowed_tools":
		return strings.EqualFold(o.Mode, "required")
	}
	return true
}

// encode is fields as JSON, each value as it came and text unescaped, so a rewrite
// changes only what it removed. A body that will not encode is sent as it came.
func encode(fields map[string]json.RawMessage, body []byte) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(fields) != nil {
		return body
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}
