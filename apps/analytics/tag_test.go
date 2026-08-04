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

package analytics

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestServeTag(t *testing.T) {
	rec := httptest.NewRecorder()
	serveTag(rec, httptest.NewRequest(http.MethodGet, "/v1/event.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type = %q, want application/javascript", ct)
	}
	// A tag a browser cannot fetch cross-origin is a tag that never runs.
	if ao := rec.Header().Get("Access-Control-Allow-Origin"); ao != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", ao)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag: every cold page load in the fleet would re-download the tag")
	}
	if body := rec.Body.String(); !strings.Contains(body, "/v1/event") {
		t.Error("tag does not name the door it feeds")
	}
}

// The tag is fetched on every cold page load, so the 304 is the common answer.
func TestServeTagNotModified(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/event.js", nil)
	r.Header.Set("If-None-Match", tagETag)
	rec := httptest.NewRecorder()
	serveTag(rec, r)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes of body", rec.Body.Len())
	}
}

// The tag carries no secret of ours: the key belongs to the PAGE. A literal key
// baked into the served asset would ship one tenant's credential to every other.
func TestTagCarriesNoKey(t *testing.T) {
	if i := strings.Index(string(tagJS), "pk-live-"); i >= 0 {
		t.Fatalf("tag embeds a literal publishable key at offset %d", i)
	}
}

// TestTagBehavior runs the tag itself (tag_test.js). Its invariants — above all
// "no key ⇒ inert" — are behavior, and asserting on source text would prove only
// that the source contains a string.
func TestTagBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; tag behavior unverified in this environment")
	}
	out, err := exec.Command(node, "tag_test.js", "tag.js").CombinedOutput()
	if err != nil {
		t.Fatalf("tag behavior: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
