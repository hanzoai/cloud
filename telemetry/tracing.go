package telemetry

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	AttrRuntimeDigest    = "gen_ai.hanzo.runtime_digest"
	AttrCacheConnector   = "gen_ai.hanzo.cache_connector"
	AttrReactivationMs   = "hanzo.session.reactivation_ms"
)

// RecordAgentExecution attaches Hanzo-specific GenAI conventions to the current span
func RecordAgentExecution(span trace.Span, digest string, connector string, reactivationMs int64) {
	span.SetAttributes(
		attribute.String(AttrRuntimeDigest, digest),
		attribute.String(AttrCacheConnector, connector),
		attribute.Int64(AttrReactivationMs, reactivationMs),
	)
}
