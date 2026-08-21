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

package manifest

import "github.com/hanzoai/cloud/manifest/door"

// The fleet's agent door, as ONE fact — for the same reason PrefixesFor exists.
// The addresses themselves live in the leaf package door, which openapi reads
// too (neither package may import the other); the doctrine lives here.
//
// MCP is not a subsystem and not a service: it is the THIRD projection of the
// same typed-op registry that yields the REST route and the OpenAPI operation
// (zip installs it in prepare(), from a.registry). So there is nothing to mount
// and nothing to write down except WHERE the projection answers — and that
// address was being written down twice, in two files, with nothing making them
// agree:
//
//	cmd/cloud/main.go   zip.MCPConfig{Path: "/v1/mcp"}   <- the host, a literal
//	webui/console.go    apiPrefixes{...}                 <- did not list it at all
//
// The consequence is the failure this file exists to make unrepresentable. The
// host MOVED zip's door off the framework default, so on the host NOTHING claims
// FrameworkMCPPath — and because the console's terminal catch-all did not know
// that path was a machine door, it answered it with the SPA shell:
//
//	GET  https://api.hanzo.ai/mcp  ->  200 text/html  <!DOCTYPE html>
//
// An MCP client checking a status code reads that as a healthy door. It is a
// green surface over a mechanism that was never there. Registration ORDER is not
// what did it — the zap-proto/fiber fork matches by specificity, so a static
// /mcp beats the /* catch-all no matter which registered first; the path simply
// had no route, in this process, at all.
//
// Both halves now read these two constants, so the address the app SERVES and
// the address the front door REFUSES TO ANSWER WITH HTML cannot drift.
const (
	// MCPPath is the fleet's one agent door: POST /v1/mcp, on the host that
	// fronts api.hanzo.ai. /v1/<thing> is the house address rule (never /api/),
	// and being under /v1/ is also what puts it inside the console's API
	// namespace, where an unmatched sibling is a real 404 and never HTML.
	//
	// It is deliberately NOT /v1/ai/mcp. The door serves the UNION of every
	// mounted subsystem's build-time catalogue — o11y's tools, iam's, commerce's
	// — so scoping it under one subsystem's prefix would either shrink it to that
	// subsystem or file a fleet-wide surface under a name that does not own it.
	// One fleet, one door.
	MCPPath = door.Path

	// FrameworkMCPPath is zip's built-in default — where a plugin serves its OWN
	// door and, therefore, where a host FORWARDS a composed tools/call (zip's
	// Plugin.mcpPath). cloud.Serve leaves it alone on purpose: a child's door is
	// an internal address on a private ZAP socket, not an edge address, and
	// moving it would break the host's forward.
	//
	// On the HOST the door is not here, so the host CLAIMS this path anyway and
	// signposts it — registered by fleet.Mount, the one call that also registers
	// the target, so a hop can never name an address the process does not serve.
	//
	// It used to be signposted from the console's terminal handler instead, on the
	// reasoning that a plugin serving its own door here matched a real route and
	// never reached it. That was the defect: zip mounts this route only when the
	// app has something to expose, so a plugin whose typed ops live on the internal
	// plane — kms's four secret ops, on cloud.Plane() by design — had no route at
	// its own door, fell through, and was told its door was at MCPPath, which no
	// plugin serves. POST /mcp -> 308, POST /v1/mcp -> 404, and the fleet reads
	// that non-2xx as an outage for a child that is serving fine. A plugin now
	// answers here with its OWN door (zip.App.MCP) whether or not a route was
	// mounted over it, and an empty tool list is a 200.
	FrameworkMCPPath = door.Framework

	// ResourceMetadataPath is where the host publishes RFC 9728 metadata for the
	// door: which authorization server mints the bearer a tools/call needs. A
	// client that reaches the door with no credential is sent here by the
	// WWW-Authenticate header the door answers with (fleet/mcp.go), and the host
	// serves it (cmd/cloud/oauth.go) — one name, two readers, so the challenge
	// can never point at an address the host does not answer.
	ResourceMetadataPath = door.Metadata
)
