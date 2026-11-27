package host

// The host's shared backends. sdk/coord answers alone, in memory, until a driver
// registers; with KV_URL set and none linked it refuses (coord.ErrNoDriver).
import (
	_ "github.com/hanzoai/cloud/internal/driver/kv"
	_ "github.com/hanzoai/cloud/internal/driver/pubsub"
	_ "github.com/hanzoai/cloud/internal/driver/s3"
)
