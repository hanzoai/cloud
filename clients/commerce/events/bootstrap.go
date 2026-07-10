package events

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/infra"
)

// Bootstrap creates the COMMERCE JetStream stream if it doesn't exist.
func Bootstrap(ctx context.Context, pubsub *infra.PubSubClient) error {
	if pubsub == nil {
		return nil
	}

	err := pubsub.EnsureStream(ctx, &infra.StreamConfig{
		Name:     StreamName,
		Subjects: StreamSubjects,
	})
	if err != nil {
		return fmt.Errorf("bootstrap commerce stream: %w", err)
	}

	return nil
}
