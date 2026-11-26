// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0
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

// Package agenttrace implements HIP-1348: unified agent execution tracing under
// a single task_id aligned with the OpenTelemetry GenAI Semantic Conventions.
//
// # Canonical Architecture Alignment
//
// Every autonomous agent task spans six canonical Hanzo subsystems without
// replacing or duplicating any of them:
//
//  1. Model calls (Enso routing + Zen generation): gen_ai.chat, model,
//     input/output/cached tokens, TTFT, finish reason.
//  2. Retrieval (hanzoai/index & hanzoai/graph): query digest, top-k,
//     hit count, ACL scope.
//  3. MCP tools (hanzoai/mcp): execute_tool, tool name, call ID, status, latency.
//  4. VFS operations (hanzoai/vfs -> hanzoai/s3): read/write/fsync/snapshot
//     bytes, coalesced op count, S3 PUT/GET count.
//  5. Sandbox startup (hanzoai/visor): boot latency, image ref, policy digest.
//  6. Billing & Cloud metering (hanzoai/cloud): micro-USD cost, token units,
//     compute units, reconciled 1:1 against emitted spans under the same task_id.
//
// # Design Invariants
//
//   - Single task_id & W3C Trace Context (traceparent/tracestate) propagation
//     across every subsystem hop, yielding a connected parent-child DAG with
//     zero orphan spans.
//   - Fail-closed multi-tenant isolation: every trace query, verification, and
//     OTLP export is strictly scoped by server-validated org_id and tenant_id.
//     Global exporter diagnostics require explicit platform operator authority.
//   - Default-on DLP / sensitive content redaction: gen_ai.prompt and
//     gen_ai.completion are redacted (or SHA-256 hashed) by default, and common
//     secret patterns (JWTs, API keys, PEM keys, bearer tokens, sensitive header
//     keys) are scrubbed before export.
//   - Bounded non-blocking ring buffer + batched OTLP/JSON exporter: hot-path
//     span emission never blocks agent execution on exporter backpressure and
//     exposes explicit drop-counter telemetry under overload.
//   - Zero host imports: imports only Go stdlib, github.com/zap-proto/zip, and
//     github.com/luxfi/log so the package mounts unchanged in any zip host.
package agenttrace
