package mq

// /v1/mq/health and /v1/mq/info — the surface's own view of the plane it
// fronts. health answers for anyone and never errors: a broken plane is a
// degraded answer, not a refusal. info is org-scoped and reports what the wire
// protocol really tells a client about the broker — no invented server
// monitoring (that refusal is pinned in typed_wire_test.go).

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
)

// status carries the broker client onto the health and info ops.
type status struct{ b *broker }

// nothing is the In of an op that takes nothing off the wire.
type nothing struct{}

// Health is the surface's liveness answer.
type Health struct {
	// Status is ok when the message plane answers, degraded otherwise.
	Status string `json:"status"`
	// Version is the connected broker's server version; empty while degraded.
	Version string `json:"version"`
	// Uptime is how long this surface has been mounted.
	Uptime string `json:"uptime"`
}

// health reports whether the message plane behind this surface answers.
func (st status) health(context.Context, *nothing) (*Health, error) {
	out := &Health{Status: "degraded"}
	if st.b != nil {
		out.Uptime = time.Since(st.b.mounted).Round(time.Second).String()
		if st.b.nc != nil && st.b.nc.Status() == nats.CONNECTED {
			out.Status = "ok"
			out.Version = st.b.nc.ConnectedServerVersion()
		}
	}
	return out, nil
}

// infoOut is what the broker tells a connected client about itself, plus the
// org's own footprint on it.
type infoOut struct {
	// Server is the broker's server id.
	Server string `json:"server_id"`
	// Name is the broker's server name.
	Name string `json:"server_name"`
	// Version is the broker's server version.
	Version string `json:"version"`
	// JetStream is true when durable streams are enabled.
	JetStream bool `json:"jetstream"`
	// MaxPayload is the broker's message-size ceiling in bytes.
	MaxPayload int64 `json:"max_payload"`
	// Streams is the org's stream count.
	Streams int `json:"streams"`
}

// info returns the broker's identity and the org's stream count.
func (st status) info(ctx context.Context, _ *nothing) (*infoOut, error) {
	if _, err := callerOf(ctx); err != nil {
		return nil, err
	}
	ctx, cancel, err := st.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out := &infoOut{
		Server:     st.b.nc.ConnectedServerId(),
		Name:       st.b.nc.ConnectedServerName(),
		Version:    st.b.nc.ConnectedServerVersion(),
		MaxPayload: st.b.nc.MaxPayload(),
	}
	if _, err := st.b.js.AccountInfo(ctx); err == nil {
		out.JetStream = true
	}
	owned, err := streams{st.b}.list(ctx, &listIn{Limit: 1})
	if err != nil {
		return nil, err
	}
	out.Streams = owned.Total
	return out, nil
}
