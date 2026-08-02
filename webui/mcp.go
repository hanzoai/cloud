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
	"net/http"

	"github.com/hanzoai/cloud/manifest"
)

// mcpDoor answers the two MCP addresses when — and only when — no route claimed
// them, and reports whether it wrote the response.
//
// It lives in the console package because the console is the TERMINAL handler:
// the one thing that sees a request nobody else would. That position is the
// whole reason /mcp lied. The SPA fallback is right for a client-side route and
// catastrophic for a machine door, and until now the handler could not tell them
// apart — so an agent POSTing JSON-RPC at the framework's default path was
// answered in the console's voice and, on GET, with 200 text/html.
//
// Neither branch SERVES MCP. The door is zip's, projected from the typed-op
// registry at manifest.MCPPath; these are the two things the front door owes a
// caller who did not find it — where it is, and that it exists but not for this
// method. A signpost is not a second surface: nothing here has a tool list.
func mcpDoor(w http.ResponseWriter, r *http.Request, upath string) bool {
	switch upath {
	case manifest.FrameworkMCPPath:
		// zip's default, unclaimed in this process => the door was moved. 308
		// preserves method AND body, so a POSTed initialize or tools/list arrives at
		// the real door instead of being answered with the SPA shell. The body is
		// JSON for the same reason the redirect exists at all: a caller who reads
		// bytes rather than following the hop must never get HTML here.
		w.Header().Set("Location", manifest.MCPPath)
		writeJSON(w, http.StatusPermanentRedirect,
			`{"error":"the MCP door moved","door":"`+manifest.MCPPath+`"}`)
		return true

	case manifest.MCPPath:
		// The door IS here, but zip registers only the JSON-RPC POST — there is no
		// server→client SSE stream on it. MCP Streamable HTTP says that is 405 with
		// Allow, and a client's optional GET treats 405 as "no stream, carry on".
		// A 404 would say the door is absent: the same lie as the 200 of HTML,
		// pointing the other way. A POST reaching this terminal handler means the
		// door genuinely is not mounted in this process (a plugin, whose own door
		// is FrameworkMCPPath) — that falls through to the ordinary /v1/ 404.
		if r.Method == http.MethodPost {
			return false
		}
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed,
			`{"error":"the MCP door speaks JSON-RPC over POST","door":"`+manifest.MCPPath+`"}`)
		return true
	}
	return false
}

// writeJSON writes one short literal body with its content type — the machine
// answers above are fixed strings, so there is nothing to encode.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
