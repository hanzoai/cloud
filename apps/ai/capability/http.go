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

	"github.com/zap-proto/zip"
)

// DialectOf is the dialect of a model route the gate holds, false for any other
// path. path is the route's normalized spelling.
func DialectOf(path string) (Dialect, bool) {
	switch strings.ToLower(strings.TrimRight(path, "/")) {
	case "/v1/chat/completions", "/v1/responses":
		return OpenAI, true
	case "/v1/messages":
		return Anthropic, true
	}
	return OpenAI, false
}

// Refusal is a blocked request's answer: its status and the body in the caller's
// dialect.
type Refusal struct {
	Status int
	Body   []byte
}

// Screen holds one request to path before dispatch: the body to send on, or the
// refusal to answer instead. log is told every decision a record answered for. A
// path the gate does not hold, or a body it has no say over, passes untouched.
func Screen(path string, body []byte, need Need, log func(Decision)) ([]byte, *Refusal) {
	dialect, ok := DialectOf(path)
	if !ok {
		return body, nil
	}
	out, d, ok := Gate(body, dialect, need)
	if !ok {
		return body, nil
	}
	if log != nil {
		log(d)
	}
	if c, blocked := d.Blocked(); blocked {
		return nil, refusal(dialect, c)
	}
	return out, nil
}

// Config is what the HTTP gate asks of its host.
type Config struct {
	// Path is a request's route spelling, normalized as the host routes it; nil
	// reads the path as sent.
	Path func(*zip.Ctx) string
	// Need is the caller's policy for one request.
	Need func(*zip.Ctx) Need
	// Log is told every decision a record answered for.
	Log func(*zip.Ctx, Decision)
}

// Handler is the gate over the model routes of one process: a POST to a route it
// holds is screened, refused in the caller's dialect, or sent on with the body the
// route can take.
func Handler(cfg Config) zip.Handler {
	return func(c *zip.Ctx) error {
		if c.Method() != http.MethodPost {
			return c.Continue()
		}
		path := c.Path()
		if cfg.Path != nil {
			path = cfg.Path(c)
		}
		if _, ok := DialectOf(path); !ok {
			return c.Continue()
		}
		var need Need
		if cfg.Need != nil {
			need = cfg.Need(c)
		}
		in := c.Body()
		out, refused := Screen(path, in, need, func(d Decision) {
			if cfg.Log != nil {
				cfg.Log(c, d)
			}
		})
		if refused != nil {
			c.SetHeader("Content-Type", "application/json")
			return c.Bytes(refused.Status, refused.Body)
		}
		if !bytes.Equal(out, in) {
			req := c.Fiber().Request()
			req.Header.Del("Content-Encoding") // the body sent on is the decoded one, rewritten
			req.SetBody(out)
		}
		return c.Continue()
	}
}

// refusal is c's refusal in the dialect's error shape: OpenAI's {"error": {message,
// type, code, param}}, Anthropic's {"type": "error", "error": {type, message}}.
func refusal(d Dialect, c Check) *Refusal {
	kind := "invalid_request_error"
	if c.Status == http.StatusForbidden {
		kind = "permission_error"
	}
	var body any
	if d == Anthropic {
		body = map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": c.Why}}
	} else {
		body = map[string]any{"error": map[string]any{"message": c.Why, "type": kind, "code": c.Code, "param": c.Param}}
	}
	raw, _ := json.Marshal(body)
	return &Refusal{Status: c.Status, Body: raw}
}
