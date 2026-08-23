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

// tag.go — GET /v1/event/tag.js, the hosted tag: the install path for a surface with
// no bundler.
//
//	<script defer src="https://api.hanzo.ai/v1/event/tag.js" data-key="pk-…"></script>
//
// That one line is the whole install, and it is the SAME line for hanzo.team, a
// published site, and a customer's own page. @hanzo/event stays the client for a
// surface that builds; this is the same wire for one that does not, served from
// the origin that eats it so a caller allowlists ONE host.
//
// It is served HERE, beside the door, because a tag that drifts from its wire is
// a tag that 400s: /v1/event/tag.js and POST /v1/event ship in one binary and version
// together.
//
// NO KEY ⇒ INERT, and that is the point of writing a tag at all. The two keyless
// beacons this replaces (analytics/public/hz.js, app wired-injection.ts) named
// their site in a BODY FIELD and sent no credential, so every one of their events
// was accepted 200 into $public — a reserved tenant the owning org cannot read.
// The tenant comes from the publishable key IAM resolves, never from a body, so
// an unkeyed page sends nothing rather than filling a tenant nobody reads.

package event

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/openapi"
)

//go:embed tag.js
var tagJS []byte

// anonJS is the anonymous-identity chain, VENDORED BYTE-FOR-BYTE from
// @hanzo/event (github.com/hanzoai/ui, pkgs/event/src/anon.js). It is the one
// implementation of who a browser is, and the npm client and hz.js run the same
// text: a page carrying any two Hanzo clients has to resolve to ONE person, and
// it did not — this tag read localStorage alone under a key hz.js never wrote.
//
// Vendored rather than restated because a fourth restatement is how the split
// happened in the first place. To resync:
//
//	curl -fsSL https://unpkg.com/@hanzo/event/src/anon.js -o apps/analytics/anon.js
//
// and TestTagServesOneIdentityChain holds the client: this file must carry the
// marked region, and tag.js must not name an identity key of its own.
//
//go:embed anon.js
var anonJS []byte

// tagAsset is what /v1/event/tag.js actually serves: the shared chain, then the tag,
// inside ONE wrapper so neither half leaves a name on the page it is pasted into
// (the chain is written to be spliced into other people's scopes, and hz.js
// splices it into its own). tag.js is itself an IIFE, so it simply nests, and
// document.currentScript still resolves — the script is executing.
var tagAsset = func() []byte {
	const begin, end = "/* ── BEGIN hz anon chain", "/* ── END hz anon chain"
	b := bytes.Index(anonJS, []byte(begin))
	e := bytes.Index(anonJS, []byte(end))
	if b < 0 || e < b {
		// Unreachable with an intact embed, and a silent miss would ship a tag
		// whose every event carries an undefined identity.
		panic(fmt.Sprintf("analytics: anon.js carries no shared chain (begin=%d end=%d)", b, e))
	}
	nl := bytes.IndexByte(anonJS[e:], '\n')
	if nl < 0 {
		panic("analytics: anon.js END marker is not a whole line")
	}
	var out bytes.Buffer
	out.WriteString(";(function () {\n")
	out.Write(anonJS[b : e+nl+1])
	out.WriteByte('\n')
	out.Write(tagJS)
	out.WriteString("\n})()\n")
	return out.Bytes()
}()

// tagETag is the asset's content hash, computed once. A tag is fetched on every
// cold page load in the fleet, so the 304 is the common answer, not the rare one.
var tagETag = func() string {
	sum := sha256.Sum256(tagAsset)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}()

// tagMaxAge bounds how long a browser keeps a tag whose key or wire changed.
// Five minutes is the loader convention: long enough that the tag is not a
// per-navigation fetch, short enough that a fix reaches the fleet within one
// coffee rather than one cache lifetime.
const tagMaxAge = "public, max-age=300"

// tagPath is the tag's one address, shared by the route, the document and the tests.
const tagPath = "/v1/event/tag.js"

// The tag declares itself beside itself: an asset response ([openapi.Bytes]) under
// the media type serveTag actually sets, and the prose a reader needs to install it.
func init() {
	openapi.Register(tagPath, http.MethodGet, nil, openapi.Bytes{Type: "application/javascript"})
	openapi.Describe(tagPath, http.MethodGet,
		"The Hanzo event tag — the one-line install for a surface with no bundler",
		"Serves the browser tag that autocaptures pageviews (initial and SPA) and uncaught "+
			"errors onto the canonical wire at POST /v1/event.\n\n"+
			"Install is one line, and it is the same line for a Hanzo property and for a "+
			"customer's own page:\n\n"+
			"    <script defer src=\"https://api.hanzo.ai/v1/event/tag.js\" data-key=\"pk-…\"></script>\n\n"+
			"`data-key` is the publishable key the project mints; `data-product` optionally names "+
			"the emitting surface. The key may also ride the src as `?key=` for a host that strips "+
			"data attributes.\n\n"+
			"WITHOUT A KEY THE TAG SENDS NOTHING. A keyless beacon is accepted 200 into $public, a "+
			"reserved tenant the owning org cannot read — so silence is the honest failure, and the "+
			"tag picks it rather than reporting success into a tenant nobody reads.")
}

// serveTag writes the tag. Public and unauthenticated by construction — it
// carries no secret (the key is supplied by the PAGE, not by us) and a script a
// browser cannot fetch anonymously is a script that never runs.
func serveTag(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", tagMaxAge)
	w.Header().Set("ETag", tagETag)
	// A tag is loaded cross-origin from every property, so it answers any origin.
	// It is a static asset with no credential and no tenant — there is nothing
	// here to confine.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if match := r.Header.Get("If-None-Match"); match == tagETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(tagAsset)
}
