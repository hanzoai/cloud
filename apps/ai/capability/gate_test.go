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
	"net/http"
	"strings"
	"testing"
)

const fable = "anthropic/claude-fable-5.2"

// body is fields as a client sends them: text unescaped, so a test sees whether a
// rewrite escapes what the client did not.
func body(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fields); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

func chat(model string, extra map[string]any) map[string]any {
	m := map[string]any{"model": model, "max_tokens": 1024, "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "hello <world> & you"}}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func check(t *testing.T, d Decision, name string) Check {
	t.Helper()
	for _, c := range d.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s check in %s", name, d.Summary())
	return Check{}
}

func TestFableIsNotDispatchedUnderZeroRetention(t *testing.T) {
	in := body(t, chat(fable, nil))
	out, d, ok := Gate(in, OpenAI, Need{ZeroRetention: func() bool { return true }})
	if !ok {
		t.Fatal("Fable has a record")
	}
	c, blocked := d.Blocked()
	if !blocked || c.Name != "retention" || c.Status != http.StatusForbidden || c.Code != CodeIneligible {
		t.Fatalf("blocked %v by %+v; %s", blocked, c, d.Summary())
	}
	if !strings.Contains(c.Why, "30 days") || !strings.Contains(c.Why, "zero data retention") {
		t.Errorf("the refusal says why: %q", c.Why)
	}
	if string(out) != string(in) {
		t.Error("a blocked request is not rewritten")
	}
}

func TestFableIsDispatchedWhereRetentionIsAllowed(t *testing.T) {
	_, d, _ := Gate(body(t, chat(fable, nil)), OpenAI, Need{ZeroRetention: func() bool { return false }})
	if _, blocked := d.Blocked(); blocked {
		t.Fatal(d.Summary())
	}
	if c := check(t, d, "retention"); c.Verdict != Pass {
		t.Fatalf("%+v", c)
	}
}

func TestZeroRetentionIsAskedOnlyOfARouteThatRetains(t *testing.T) {
	asked := 0
	need := Need{ZeroRetention: func() bool { asked++; return true }}
	_, d, _ := Gate(body(t, chat("anthropic/claude-opus-5.5", nil)), OpenAI, need)
	if _, blocked := d.Blocked(); blocked || asked != 0 {
		t.Fatalf("Opus 5.5 keeps nothing; asked %d; %s", asked, d.Summary())
	}
	Gate(body(t, chat(fable, nil)), OpenAI, need)
	if asked != 1 {
		t.Fatalf("asked %d", asked)
	}
}

func TestFableIsNotDispatchedOntoAnIncompatibleResidency(t *testing.T) {
	_, d, _ := Gate(body(t, chat(fable, nil)), OpenAI, Need{Residency: func() string { return "eu" }})
	c, blocked := d.Blocked()
	if !blocked || c.Name != "residency" || c.Status != http.StatusForbidden || c.Code != CodeIneligible {
		t.Fatalf("%+v; %s", c, d.Summary())
	}
	if !strings.Contains(c.Why, "eu") || !strings.Contains(c.Why, "openrouter") {
		t.Errorf("%q", c.Why)
	}
	_, d, _ = Gate(body(t, chat(fable, nil)), OpenAI, Need{Residency: func() string { return "" }})
	if c := check(t, d, "residency"); c.Verdict != Pass {
		t.Fatalf("no residency asked: %+v", c)
	}
}

func TestAForcedToolChoiceIsRefusedBeforeTheProvider(t *testing.T) {
	forced := []struct {
		dialect Dialect
		choice  any
	}{
		{OpenAI, "required"},
		{OpenAI, map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
		{OpenAI, map[string]any{"type": "allowed_tools", "mode": "required", "tools": []any{}}},
		{Anthropic, map[string]any{"type": "any"}},
		{Anthropic, map[string]any{"type": "tool", "name": "lookup"}},
	}
	for _, f := range forced {
		in := body(t, chat(fable, map[string]any{"tools": []any{}, "tool_choice": f.choice}))
		_, d, _ := Gate(in, f.dialect, Need{})
		c, blocked := d.Blocked()
		if !blocked || c.Name != "tool_choice" || c.Status != http.StatusBadRequest || c.Code != CodeToolChoice || c.Param != "tool_choice" {
			t.Errorf("%v: %+v; %s", f.choice, c, d.Summary())
		}
	}
	free := []any{"auto", "none", map[string]any{"type": "auto"}, map[string]any{"type": "none"},
		map[string]any{"type": "allowed_tools", "mode": "auto", "tools": []any{}}}
	for _, choice := range free {
		_, d, _ := Gate(body(t, chat(fable, map[string]any{"tool_choice": choice})), OpenAI, Need{})
		if c := check(t, d, "tool_choice"); c.Verdict != Pass {
			t.Errorf("%v is not forced: %+v", choice, c)
		}
	}
	// A model that takes a forced choice is sent it.
	_, d, _ := Gate(body(t, chat("anthropic/claude-opus-5", map[string]any{"tool_choice": "required"})), OpenAI, Need{})
	if c := check(t, d, "tool_choice"); c.Verdict != Pass {
		t.Errorf("Opus 5 takes a forced choice: %+v", c)
	}
}

func TestRejectedSamplingParametersAreDroppedAndTheRestKept(t *testing.T) {
	in := body(t, chat(fable, map[string]any{"temperature": 0.2, "top_p": 0.9, "top_k": 5, "metadata": map[string]any{"a": "b"}}))
	out, d, _ := Gate(in, OpenAI, Need{})
	if _, blocked := d.Blocked(); blocked {
		t.Fatal(d.Summary())
	}
	c := check(t, d, "sampling")
	if c.Verdict != Rewrite || !strings.Contains(c.Why, "temperature, top_p, top_k") {
		t.Fatalf("%+v", c)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"temperature", "top_p", "top_k"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s still sent", p)
		}
	}
	var want map[string]json.RawMessage
	_ = json.Unmarshal(in, &want)
	for _, k := range []string{"model", "max_tokens", "stream", "messages", "metadata"} {
		if string(got[k]) != string(want[k]) {
			t.Errorf("%s: %s, want %s", k, got[k], want[k])
		}
	}
	if !strings.Contains(string(out), "<world> & you") {
		t.Errorf("the text crosses as written: %s", out)
	}
	if !d.Rewrote() {
		t.Error("Rewrote")
	}
}

func TestSamplingAModelTakesIsLeftAlone(t *testing.T) {
	in := body(t, chat("anthropic/claude-sonnet-4.6", map[string]any{"temperature": 0.2}))
	out, d, _ := Gate(in, OpenAI, Need{})
	if string(out) != string(in) || d.Rewrote() {
		t.Fatalf("%s → %s; %s", in, out, d.Summary())
	}
}

func TestThinkingAModelCannotTakeIsRewrittenToWhatItRuns(t *testing.T) {
	cases := []struct {
		model    string
		thinking map[string]any
		want     string // the thinking sent on, "" for none
		verdict  Verdict
	}{
		{fable, map[string]any{"type": "disabled"}, "", Rewrite},
		{fable, map[string]any{"type": "enabled", "budget_tokens": 2048}, `{"type":"adaptive"}`, Rewrite},
		{fable, map[string]any{"type": "adaptive", "display": "summarized"}, `{"display":"summarized","type":"adaptive"}`, Pass},
		{"anthropic/claude-sonnet-5.5", map[string]any{"type": "between_tools"}, `{"type":"between_tools"}`, Pass},
		{"anthropic/claude-opus-4.8", map[string]any{"type": "disabled"}, `{"type":"disabled"}`, Pass},
		{"anthropic/claude-opus-4.6", map[string]any{"type": "enabled", "budget_tokens": 2048}, `{"budget_tokens":2048,"type":"enabled"}`, Pass},
	}
	for _, c := range cases {
		out, d, _ := Gate(body(t, chat(c.model, map[string]any{"thinking": c.thinking})), Anthropic, Need{})
		if got := check(t, d, "thinking"); got.Verdict != c.verdict {
			t.Errorf("%s %v: %+v", c.model, c.thinking, got)
		}
		var sent map[string]json.RawMessage
		_ = json.Unmarshal(out, &sent)
		if string(sent["thinking"]) != c.want {
			t.Errorf("%s %v: sent %s, want %s", c.model, c.thinking, sent["thinking"], c.want)
		}
	}
}

func TestARequestWithoutARecordIsNotTouched(t *testing.T) {
	for _, in := range [][]byte{
		body(t, chat("enso-max", map[string]any{"tool_choice": "required", "temperature": 0})),
		[]byte(`{"model": "anthropic/claude-fable-5.2",`), // unreadable: the route answers its own 400
		[]byte(`{"messages": []}`),
	} {
		out, d, ok := Gate(in, OpenAI, Need{ZeroRetention: func() bool { t.Fatal("asked"); return true }})
		if ok || string(out) != string(in) || len(d.Checks) != 0 {
			t.Errorf("%s: ok %v, %s", in, ok, d.Summary())
		}
	}
}

func TestTheDecisionSaysEveryCheck(t *testing.T) {
	in := body(t, chat(fable, map[string]any{"temperature": 1, "tool_choice": "auto"}))
	_, d, _ := Gate(in, OpenAI, Need{})
	want := "retention=pass residency=pass tool_choice=pass sampling=rewrite thinking=pass"
	if d.Summary() != want {
		t.Fatalf("%q, want %q", d.Summary(), want)
	}
	if d.Verdict() != Rewrite {
		t.Errorf("verdict %s", d.Verdict())
	}
	kv := d.Log()
	if len(kv)%2 != 0 || kv[0] != "model" || kv[1] != fable {
		t.Errorf("%v", kv)
	}
	_, d, _ = Gate(body(t, chat(fable, map[string]any{"tool_choice": "required"})), OpenAI, Need{ZeroRetention: func() bool { return true }})
	// Every check runs, so the log names each one that would have refused.
	if d.Summary() != "retention=block residency=pass tool_choice=block sampling=pass thinking=pass" || d.Verdict() != Block {
		t.Fatalf("%q", d.Summary())
	}
	if c, _ := d.Blocked(); c.Name != "retention" {
		t.Errorf("the first block answers: %+v", c)
	}
}
