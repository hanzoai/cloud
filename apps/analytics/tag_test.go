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
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	if i := strings.Index(string(tagAsset), "pk-live-"); i >= 0 {
		t.Fatalf("tag embeds a literal publishable key at offset %d", i)
	}
}

// Who a browser is has ONE implementation, and this tag is not allowed to be a
// second one. It used to resolve the anonymous id itself, out of localStorage
// alone, so an origin carrying only this tag never saw the cookie the other two
// Hanzo clients share and never adopted the id hz.js had already left there —
// one visitor, counted as two people, decided by which snippet a page loaded.
func TestTagServesOneIdentityChain(t *testing.T) {
	if !bytes.Contains(anonJS, []byte("BEGIN hz anon chain")) {
		t.Fatal("anon.js carries no shared chain: resync it from @hanzo/event")
	}
	// The composition is the asset. Neither half is served alone, so the tag can
	// call a function it does not define — but only because this holds.
	if n := bytes.Count(tagAsset, []byte("function hzAnonId(")); n != 1 {
		t.Errorf("served asset defines hzAnonId %d times, want exactly 1", n)
	}
	if !bytes.Contains(tagAsset, []byte("hzAnonId()")) {
		t.Error("served asset never calls the shared chain")
	}
	// A snippet that spells a key has an opinion about identity, and there is one
	// opinion now. Both names may appear only inside the vendored chain.
	for _, key := range []string{"'iam-anon-id'", "'hz_anon_id'", "'hz_id'"} {
		if bytes.Contains(tagJS, []byte(key)) {
			t.Errorf("tag.js names %s: identity belongs to anon.js alone", key)
		}
	}
}

// TestTagBehavior runs the tag itself (tag_test.js). Its invariants — above all
// "no key ⇒ inert" — are behavior, and asserting on source text would prove only
// that the source contains a string.
//
// It runs the COMPOSED asset, not tag.js: the identity chain is half of what
// ships, and a harness that loaded only the other half would test a file that no
// browser ever receives (and, since tag.js alone cannot resolve an id, would not
// even run).
func TestTagBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; tag behavior unverified in this environment")
	}
	asset := filepath.Join(t.TempDir(), "tag.js")
	if err := os.WriteFile(asset, tagAsset, 0o600); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	out, err := exec.Command(node, "tag_test.js", asset).CombinedOutput()
	if err != nil {
		t.Fatalf("tag behavior: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
