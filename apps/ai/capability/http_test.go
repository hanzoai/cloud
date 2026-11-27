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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// gated is an app as the model process composes one: the gate over /v1, then a
// stand-in for every model route that records the body it was sent.
func gated(t *testing.T, need Need) (*zip.App, *[]string, *[]Decision) {
	t.Helper()
	app := zip.New(zip.Config{AppName: "capability", DisableStartupMessage: true})
	var logged []Decision
	app.Use(Handler(Config{
		Need: func(*zip.Ctx) Need { return need },
		Log:  func(_ *zip.Ctx, d Decision) { logged = append(logged, d) },
	}))
	var sent []string
	for _, p := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/embeddings"} {
		app.Raw(http.MethodPost, p, func(c *zip.Ctx) error {
			sent = append(sent, string(c.Body()))
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		})
	}
	return app, &sent, &logged
}

func post(t *testing.T, app *zip.App, path, raw string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	return res.StatusCode, out
}

func TestTheGateRefusesAForcedToolChoiceInTheCallersDialect(t *testing.T) {
	app, sent, logged := gated(t, Need{})
	code, out := post(t, app, "/v1/chat/completions", `{"model":"anthropic/claude-fable-5.2","messages":[],"tool_choice":"required"}`)
	if code != http.StatusBadRequest || len(*sent) != 0 {
		t.Fatalf("%d, sent %v", code, *sent)
	}
	e, _ := out["error"].(map[string]any)
	if e["code"] != CodeToolChoice || e["type"] != "invalid_request_error" || e["param"] != "tool_choice" {
		t.Errorf("OpenAI's error shape: %v", out)
	}
	if len(*logged) != 1 || (*logged)[0].Verdict() != Block {
		t.Errorf("logged %v", *logged)
	}

	code, out = post(t, app, "/v1/messages", `{"model":"anthropic/claude-fable-5.2","messages":[],"tool_choice":{"type":"any"}}`)
	if code != http.StatusBadRequest || out["type"] != "error" {
		t.Fatalf("%d %v", code, out)
	}
	if e, _ := out["error"].(map[string]any); e["type"] != "invalid_request_error" || !strings.Contains(e["message"].(string), "auto") {
		t.Errorf("Anthropic's error shape: %v", out)
	}
}

func TestTheGateRefusesFableUnderZeroRetention(t *testing.T) {
	app, sent, _ := gated(t, Need{ZeroRetention: func() bool { return true }})
	code, out := post(t, app, "/v1/responses", `{"model":"~anthropic/claude-fable-latest","input":"hi"}`)
	if code != http.StatusForbidden || len(*sent) != 0 {
		t.Fatalf("%d %v", code, out)
	}
	if e, _ := out["error"].(map[string]any); e["code"] != CodeIneligible || e["type"] != "permission_error" || e["param"] != "model" {
		t.Errorf("%v", out)
	}
}

func TestTheGateSendsTheRewrittenBodyOn(t *testing.T) {
	app, sent, logged := gated(t, Need{})
	code, _ := post(t, app, "/v1/chat/completions", `{"model":"anthropic/claude-fable-5.2","messages":[],"temperature":0.3,"top_p":0.5}`)
	if code != http.StatusOK || len(*sent) != 1 {
		t.Fatalf("%d %v", code, *sent)
	}
	if strings.Contains((*sent)[0], "temperature") || strings.Contains((*sent)[0], "top_p") {
		t.Errorf("sent %s", (*sent)[0])
	}
	if len(*logged) != 1 || (*logged)[0].Verdict() != Rewrite {
		t.Errorf("%v", *logged)
	}
}

func TestTheGateLeavesEverythingElse(t *testing.T) {
	app, sent, logged := gated(t, Need{ZeroRetention: func() bool { return true }})
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"enso-max","messages":[],"tool_choice":"required","temperature":0}`},
		{"/v1/embeddings", `{"model":"anthropic/claude-fable-5.2","input":"x"}`},
		{"/v1/chat/completions", `{"model":"anthropic/claude-sonnet-4.6","messages":[],"temperature":0.2}`},
	} {
		if code, _ := post(t, app, c.path, c.body); code != http.StatusOK {
			t.Fatalf("%s %s: %d", c.path, c.body, code)
		}
	}
	if len(*sent) != 3 || (*sent)[0] != `{"model":"enso-max","messages":[],"tool_choice":"required","temperature":0}` {
		t.Fatalf("%v", *sent)
	}
	// Only a request a record answered for is logged: Sonnet 4.6, all checks passed.
	if len(*logged) != 1 || (*logged)[0].Verdict() != Pass {
		t.Fatalf("%v", *logged)
	}
}

func TestScreenAnswersAPlaneCallAsTheRouteWould(t *testing.T) {
	var logged []Decision
	out, refused := Screen("/v1/chat/completions", []byte(`{"model":"anthropic/claude-fable-5.2","messages":[],"tool_choice":"required"}`),
		Need{}, func(d Decision) { logged = append(logged, d) })
	if refused == nil || refused.Status != http.StatusBadRequest || !strings.Contains(string(refused.Body), CodeToolChoice) || out != nil {
		t.Fatalf("%+v %s", refused, out)
	}
	in := []byte(`{"model":"anthropic/claude-fable-5.2","messages":[],"top_k":3}`)
	out, refused = Screen("/v1/chat/completions", in, Need{}, nil)
	if refused != nil || strings.Contains(string(out), "top_k") {
		t.Fatalf("%+v %s", refused, out)
	}
	in = []byte(`{"model":"zen5","messages":[]}`)
	if out, refused = Screen("/v1/chat/completions", in, Need{}, nil); refused != nil || string(out) != string(in) {
		t.Fatalf("%+v %s", refused, out)
	}
	if out, refused = Screen("/v1/embeddings", []byte(`{"model":"anthropic/claude-fable-5.2"}`), Need{}, nil); refused != nil || out == nil {
		t.Fatal("embeddings are not gated")
	}
	if len(logged) != 1 {
		t.Errorf("%v", logged)
	}
}
