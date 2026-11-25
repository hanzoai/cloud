// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package fanclub is the opt-in Cloud-native FanClub AI organization
// intelligence subsystem (CLOUD_ENABLE_STAGED=fanclub).
//
// It replaces the legacy standalone Node/SQLite prototype previously hosted in
// hanzoai/stack with canonical Hanzo Cloud primitives:
//
//   - Identity: Hanzo IAM JWT validation via Cloud's SanitizeIdentity boundary
//     plus PKCE authorization code exchange with exact redirect URI allowlist.
//     Zero browser-chosen tenant selector; every record is scoped to the
//     validated IAM org.
//   - Persistence: Cloud Base (/v1/base) collections fc_members, fc_proposals,
//     and fc_audit exclusively. Zero independent SQLite database.
//   - Inference: Cloud's configured AIClient (Hanzo AI /v1/chat/completions or
//     ZAP gateway) on aggregate metrics only. Fails closed (503) when
//     unconfigured or during upstream model outages — never substitutes mock
//     rules or external providers.
//   - Approvals & Actions: role-authorized (org admin / SuperAdmin) human
//     review with atomic idempotency, consent enforcement, durable simulation
//     task enqueueing in Cloud Tasks, and audit receipts in Cloud Base + Cloud
//     Audit. Zero outbound side effects.
//   - Tools: every route is a typed zip op, automatically projected into REST,
//     /v1/openapi.json, /mcp JSON-RPC 2.0, ZAP, and CLI under the same IAM
//     identity and audit boundaries.
//
// The package imports zip, luxfi/log, and the Go standard library — never its
// host — so the same source mounts unchanged in both OSS and private Cloud.
package fanclub
