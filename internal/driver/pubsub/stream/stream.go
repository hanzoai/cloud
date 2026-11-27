// Package stream is the JetStream half of the pubsub driver: the handle the apps whose
// product IS the bus (apps/pubsub, apps/kv) and the ledger's usage stream take, and the
// mapping of JetStream's refusals onto zip statuses for sdk/bus.Err.
//
// It rides the connection internal/driver/pubsub dials, and is a package of its own so
// the light host, which links that driver for the coordination bus, does not link the
// JetStream client with it.
package stream

import (
	"context"
	"errors"
	"net/http"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/driver/pubsub"
	"github.com/hanzoai/cloud/sdk/bus"
)

func init() { bus.Register(bus.Driver{Err: mapErr}) }

// Bus returns the live JetStream handle and connection on THE one plane. It answers
// 503 rather than an error the caller has to translate.
func Bus() (jetstream.JetStream, *nats.Conn, error) {
	nc, err := pubsub.Conn()
	if err != nil {
		return nil, nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, nil, zip.Errorf(http.StatusServiceUnavailable, "bus unreachable: %v", err)
	}
	return js, nc, nil
}

// mapErr maps the plane's own refusals onto the wire honestly.
func mapErr(err error) (error, bool) {
	var apiErr *jetstream.APIError
	switch {
	case errors.Is(err, jetstream.ErrStreamNotFound):
		return zip.ErrNotFound("stream not found"), true
	case errors.Is(err, jetstream.ErrConsumerNotFound):
		return zip.ErrNotFound("consumer not found"), true
	case errors.Is(err, jetstream.ErrBucketNotFound):
		return zip.ErrNotFound("bucket not found"), true
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return zip.ErrNotFound("key not found"), true
	case errors.Is(err, jetstream.ErrStreamNameAlreadyInUse):
		return zip.Errorf(http.StatusConflict, "stream name already in use"), true
	case errors.Is(err, jetstream.ErrConsumerExists):
		return zip.Errorf(http.StatusConflict, "consumer already exists"), true
	case errors.Is(err, jetstream.ErrBucketExists):
		return zip.Errorf(http.StatusConflict, "bucket already exists"), true
	case errors.As(err, &apiErr):
		if apiErr.Code >= 400 && apiErr.Code < 500 {
			return zip.Errorf(apiErr.Code, "%s", apiErr.Description), true
		}
		return zip.Errorf(http.StatusInternalServerError, "bus: %s", apiErr.Description), true
	case errors.Is(err, context.DeadlineExceeded):
		return zip.Errorf(http.StatusGatewayTimeout, "bus timeout"), true
	}
	return nil, false
}
