// Package notion is the pure record-shaping logic for the Notion long-tail connector:
// how to turn the raw JSON a Notion search returns (via the activepieces provider run
// through the auto engine) into normalized {title, body, external_id, url, timestamp}
// documents for KB ingestion. It has NO cloud dependency, so the Notion parsing —
// where the real per-provider complexity lives — is unit-testable in isolation.
// apps/knowledge/sync_piece.go wires this into the framework.Ingest path.
package notion
