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

package cloud

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// A RETURNED error and a WRITTEN response reach the tracing middleware in
// different states. A written response holds its real status by the time the
// middleware reads it; a returned error has not been rendered yet, because fiber
// unwinds the chain and calls ErrorHandler afterwards — so the response still
// carries its default 200 and the caller's status lives only in the error.
//
// Every case below is a status a handler RETURNS rather than writes.
func TestSpanRecordsTheStatusTheCallerGot(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
		err  error
	}{
		{"refused", http.StatusForbidden, zip.Errorf(http.StatusForbidden, "a validated principal is required")},
		{"unauthenticated", http.StatusUnauthorized, zip.Errorf(http.StatusUnauthorized, "no credential")},
		{"absent", http.StatusNotFound, zip.Errorf(http.StatusNotFound, "no such session")},
		{"unavailable", http.StatusServiceUnavailable, zip.Errorf(http.StatusServiceUnavailable, "warehouse unreachable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := newRecordingTracer(t)

			app := zip.New(zip.Config{ErrorHandler: ErrorHandler})
			app.Use(TracingMiddleware())
			app.Patch("/v1/agents/sessions/:id", func(c *zip.Ctx) error { return tc.err })

			resp, err := app.Test(httptest.NewRequest("PATCH", "/v1/agents/sessions/sess_x", nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}

			// The wire is the reference: whatever the caller got is what the span
			// must say. Asserting the span against a constant instead would pass
			// just as well if BOTH went wrong together.
			if resp.StatusCode != tc.want {
				t.Fatalf("wire status = %d, want %d", resp.StatusCode, tc.want)
			}

			spans := sr.Ended()
			if len(spans) != 1 {
				t.Fatalf("recorded %d spans, want 1", len(spans))
			}
			v, ok := attrOf(spans[0], "http.response.status_code")
			if !ok {
				t.Fatal("span carries no http.response.status_code")
			}
			if got := int(v.AsInt64()); got != resp.StatusCode {
				t.Errorf("span recorded status_code %d, caller got %d — the span disagrees with the wire", got, resp.StatusCode)
			}
		})
	}
}

// The other arm, so the fix is not "report the error's status for everything":
// a handler that WRITES its status and returns nil is already correct and must
// stay untouched. Without this, replacing the read with the error's status
// unconditionally would still pass the test above.
func TestSpanStillRecordsAWrittenStatus(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{ErrorHandler: ErrorHandler})
	app.Use(TracingMiddleware())
	app.Get("/v1/models", func(c *zip.Ctx) error {
		return c.JSON(http.StatusCreated, map[string]string{"ok": "yes"})
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/models", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("wire status = %d, want 201", resp.StatusCode)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if v, ok := attrOf(spans[0], "http.response.status_code"); !ok || v.AsInt64() != http.StatusCreated {
		t.Errorf("span recorded status_code %d (present=%v), caller got 201", v.AsInt64(), ok)
	}
}
