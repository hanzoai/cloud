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

package webui

import (
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud/manifest"
	zapmcp "github.com/zap-proto/mcp"
)

// serveMCP answers the two MCP addresses when — and only when — no route claimed
// them, and reports whether it wrote the response.
//
// It lives in the console package because the console is the TERMINAL handler:
// the one thing that sees a request nobody else would. That position is the
// whole reason /mcp lied. The SPA fallback is right for a client-side route and
// catastrophic for a machine endpoint, and until now the handler could not tell
// them apart — so an agent POSTing JSON-RPC at the framework's default path was
// answered in the console's voice and, on GET, with 200 text/html.
//
// It used to answer the framework default with a REDIRECT to manifest.MCPPath,
// on the reasoning that a plugin serving its own MCP server there matched a real
// route and never reached here. That was false, and it is the defect this file
// now exists to close: zip mounts the /mcp route only when the app has something
// to expose, so a plugin with no typed ops of its own — kms, whose four secret
// ops are on the internal plane by design — reached here AT ITS OWN MCP ADDRESS
// and was sent to an address only a host serves. The signpost is the host's, and
// it lives with the host's MCP server now (client.Use).
func (h *consoleHandler) serveMCP(w http.ResponseWriter, r *http.Request, upath string) bool {
	switch upath {
	case manifest.FrameworkMCPPath:
		// THIS PROCESS'S OWN MCP SERVER. Reaching a terminal handler here means
		// zip registered no route for it, never that the MCP server is elsewhere —
		// and the MCP server is not the route: it is [zip.App.MCP], a frame in and
		// a frame out, which exists whether or not anything was mounted over it. So
		// answer it. Serving zip's own MCP server is not a second surface;
		// re-implementing one, or redirecting to an MCP server this process does
		// not have, would be.
		if r.Method != http.MethodPost {
			// The optional server→client SSE stream, which zip's MCP server
			// does not have either. Same answer as below, for the same reason.
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed,
				`{"error":"the MCP server speaks JSON-RPC over POST","door":"`+manifest.FrameworkMCPPath+`"}`)
			return true
		}
		h.serveEndpoint(w, r)
		return true

	case manifest.MCPPath:
		// The MCP server IS here, but zip registers only the JSON-RPC POST — there
		// is no server→client SSE stream on it. MCP Streamable HTTP says that is 405
		// with Allow, and a client's optional GET treats 405 as "no stream, carry
		// on". A 404 would say the MCP server is absent: the same lie as the 200 of
		// HTML, pointing the other way. A POST reaching this terminal handler means
		// the MCP server genuinely is not mounted in this process (a plugin, whose
		// own MCP address is FrameworkMCPPath) — that falls through to the ordinary
		// /v1/ 404.
		if r.Method == http.MethodPost {
			return false
		}
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed,
			`{"error":"the MCP server speaks JSON-RPC over POST","door":"`+manifest.MCPPath+`"}`)
		return true
	}
	return false
}

// serveEndpoint is HTTP over the MCP server, not an MCP server of its own: read the
// JSON-RPC body into a frame, hand it to [zip.App.MCP], write the answer back.
// zip's own /mcp route is the identical adapter over the identical value — one
// MCP server, and the transport is a choice, which is the whole point of the
// frame-in/frame-out signature. What is NOT duplicated is any tool: this file
// knows no tool names, no schemas and no dispatch.
func (h *consoleHandler) serveEndpoint(w http.ResponseWriter, r *http.Request) {
	var f zapmcp.Frame
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		writeFrame(w, &zapmcp.Frame{Kind: zapmcp.Response,
			Err: &zapmcp.Error{Code: zapmcp.CodeParse, Message: "parse error"}})
		return
	}
	ans := h.mcp(r.Context(), &f)
	if ans == nil {
		// A notification: nothing to say, and 202 says exactly that.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeFrame(w, ans)
}

// writeFrame writes one frame as the JSON-RPC message an HTTP client reads. The
// rendering is [zapmcp.Frame]'s own, so the bytes an agent reads here and the
// bytes it reads from zip's route describe the same value and cannot drift.
//
// A JSON-RPC error is still a 200: the transport delivered it. Only the envelope
// says no.
func writeFrame(w http.ResponseWriter, f *zapmcp.Frame) {
	b, err := json.Marshal(f)
	if err != nil {
		http.Error(w, "the MCP server could not render its answer", http.StatusInternalServerError)
		return
	}
	// zip's media type exactly, not this file's usual `; charset=utf-8`. An agent
	// must not be able to tell which adapter carried the answer — that difference
	// would be the second MCP server reappearing as a header.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// writeJSON writes one short literal body with its content type — the machine
// answers above are fixed strings, so there is nothing to encode.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
