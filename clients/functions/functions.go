package functions

import (
	"context"
	"time"
	"github.com/hanzoai/cloud/telemetry"
	"go.opentelemetry.io/otel"
)

// ExecuteWithTracing wraps the function execution with unified tracing
func (f *Runner) ExecuteWithTracing(ctx context.Context, taskID string, fn func() error) error {
	tracer := otel.Tracer("hanzoai/cloud/functions")
	ctx, span := tracer.Start(ctx, "agent.execution")
	defer span.End()

	start := time.Now()
	err := fn()
	
	// Mocking retrieval of metadata for demonstration of the correlation requirement
	// In production, these are fetched from the sandbox state manager
	digest := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	connector := "s3-cache-v1"
	reactivationMs := time.Since(start).Milliseconds()

	telemetry.RecordAgentExecution(span, digest, connector, reactivationMs)
	
	return err
}
