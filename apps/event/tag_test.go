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

package event

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// fetchTag drives the LIVE router — the real Mount the binary runs — rather than
// handing the handler a recorder. A recorder is evidence about a FUNCTION; the tag
// is a served ADDRESS, and the route is the only place the two meet. (http_test.go's
// `do` cannot serve here: the 304 case needs a request HEADER, which it does not take.)
func fetchTag(t *testing.T, app *zip.App, ifNoneMatch string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, tagPath, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", tagPath, err)
	}
	return resp
}

func TestServeTag(t *testing.T) {
	resp := fetchTag(t, mountApp(t), "")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type = %q, want application/javascript", ct)
	}
	// A tag a browser cannot fetch cross-origin is a tag that never runs.
	if ao := resp.Header.Get("Access-Control-Allow-Origin"); ao != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", ao)
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("no ETag: every cold page load in the fleet would re-download the tag")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != tagMaxAge {
		t.Errorf("Cache-Control = %q, want %q", cc, tagMaxAge)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Contains(body, []byte("/v1/event")) {
		t.Error("tag does not name the endpoint it feeds")
	}
	// The WHOLE asset, not a prefix of it. The served body is the one thing the
	// framing change could have moved — a truncated tag is a tag that does not run,
	// and it would still contain the endpoint's address.
	if !bytes.Equal(body, tagAsset) {
		t.Errorf("served %d bytes, want the whole %d-byte asset", len(body), len(tagAsset))
	}
}

// The tag is fetched on every cold page load, so the 304 is the common answer.
func TestServeTagNotModified(t *testing.T) {
	resp := fetchTag(t, mountApp(t), tagETag)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("304 carried %d bytes of body", len(body))
	}
	// The validator rides the 304 as well: a client handed no ETag back has nothing
	// to re-present, so the next load is a full download and the 304 buys nothing.
	if et := resp.Header.Get("ETag"); et != tagETag {
		t.Errorf("304 ETag = %q, want %q", et, tagETag)
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
