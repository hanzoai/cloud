package mq

// /v1/mq/streams/{stream}/consumers — durable pull consumers on the org's
// streams: list, create, inspect, delete, and pull the next batch. Delivery
// here is the QUEUE half of the product (pull, at-least-once tracked by the
// broker); the subject side (publish/subscribe) is pubsub's surface.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"
)

// consumers carries the broker client onto the consumer ops.
type consumers struct{ b *broker }

// Durable is a consumer's configuration, spec-shaped.
type Durable struct {
	// Name is the durable consumer name (alphanumeric, hyphens, underscores).
	Name string `json:"durable_name"`
	// Filter delivers only messages on this org-relative subject (wildcards supported).
	Filter string `json:"filter_subject"`
	// Ack is the acknowledgment policy: explicit (default), all, or none.
	Ack string `json:"ack_policy"`
	// Deliver is where delivery starts: all (default), last, new, by_start_sequence, by_start_time, or last_per_subject.
	Deliver string `json:"deliver_policy"`
	// StartSeq is the starting sequence for deliver_policy by_start_sequence.
	StartSeq uint64 `json:"opt_start_seq"`
	// StartTime is the starting instant for deliver_policy by_start_time.
	StartTime *time.Time `json:"opt_start_time"`
	// MaxDeliver caps delivery attempts per message; -1 (default) is unlimited.
	MaxDeliver int `json:"max_deliver"`
	// AckWait is how long the broker waits for an ack before redelivering, e.g. "30s" (default).
	AckWait string `json:"ack_wait"`
	// Replay is the replay pacing: instant (default) or original.
	Replay string `json:"replay_policy"`
	// MaxAckPending caps unacknowledged messages in flight (default 1000).
	MaxAckPending int `json:"max_ack_pending"`
	// Description says what this consumer is for.
	Description string `json:"description"`
}

// Sequences is a consumer/stream sequence pair.
type Sequences struct {
	// Consumer is the consumer's own sequence.
	Consumer uint64 `json:"consumer_seq"`
	// Stream is the corresponding stream sequence.
	Stream uint64 `json:"stream_seq"`
}

// Consumer is one durable consumer: its configuration and delivery state.
type Consumer struct {
	// Name is the consumer name.
	Name string `json:"name"`
	// Stream is the stream this consumer reads.
	Stream string `json:"stream_name"`
	// Config is the consumer's configuration.
	Config Durable `json:"config"`
	// Delivered is the highest delivered sequence pair.
	Delivered Sequences `json:"delivered"`
	// AckFloor is the highest contiguously acknowledged sequence pair.
	AckFloor Sequences `json:"ack_floor"`
	// Pending is the number of messages yet to be delivered.
	Pending uint64 `json:"num_pending"`
	// Redelivered is the number of messages currently being redelivered.
	Redelivered int `json:"num_redelivered"`
	// Waiting is the number of pull requests waiting for messages.
	Waiting int `json:"num_waiting"`
	// AckPending is the number of delivered, not yet acknowledged messages.
	AckPending int `json:"num_ack_pending"`
	// Created is when the consumer was created.
	Created time.Time `json:"created"`
}

// wire maps a spec-shaped consumer config into the org's namespace.
func (in *Durable) wire(org string) (jetstream.ConsumerConfig, error) {
	if !name.MatchString(in.Name) {
		return jetstream.ConsumerConfig{}, zip.ErrBadRequest("durable_name must match ^[a-zA-Z0-9_-]{1,256}$")
	}
	ack, err := acks.of(in.Ack, "ack_policy", jetstream.AckExplicitPolicy)
	if err != nil {
		return jetstream.ConsumerConfig{}, err
	}
	deliver, err := delivers.of(in.Deliver, "deliver_policy", jetstream.DeliverAllPolicy)
	if err != nil {
		return jetstream.ConsumerConfig{}, err
	}
	replay, err := replays.of(in.Replay, "replay_policy", jetstream.ReplayInstantPolicy)
	if err != nil {
		return jetstream.ConsumerConfig{}, err
	}
	wait, err := dur(in.AckWait)
	if err != nil {
		return jetstream.ConsumerConfig{}, err
	}
	if wait == 0 {
		wait = 30 * time.Second
	}
	filter := ""
	if in.Filter != "" {
		if filter, err = wireSubject(org, in.Filter); err != nil {
			return jetstream.ConsumerConfig{}, err
		}
	}
	maxDeliver := in.MaxDeliver
	if maxDeliver == 0 {
		maxDeliver = -1
	}
	maxAck := in.MaxAckPending
	if maxAck == 0 {
		maxAck = 1000
	}
	return jetstream.ConsumerConfig{
		Durable:       in.Name,
		Description:   in.Description,
		FilterSubject: filter,
		AckPolicy:     ack,
		DeliverPolicy: deliver,
		OptStartSeq:   in.StartSeq,
		OptStartTime:  in.StartTime,
		MaxDeliver:    maxDeliver,
		AckWait:       wait,
		ReplayPolicy:  replay,
		MaxAckPending: maxAck,
	}, nil
}

// consumer presents broker consumer info org-relative.
func consumer(org string, info *jetstream.ConsumerInfo) Consumer {
	stream, _ := streamName(org, info.Stream)
	return Consumer{
		Name:   info.Name,
		Stream: stream,
		Config: Durable{
			Name:          info.Config.Durable,
			Filter:        relSubject(org, info.Config.FilterSubject),
			Ack:           acks.word(info.Config.AckPolicy),
			Deliver:       delivers.word(info.Config.DeliverPolicy),
			StartSeq:      info.Config.OptStartSeq,
			StartTime:     info.Config.OptStartTime,
			MaxDeliver:    info.Config.MaxDeliver,
			AckWait:       durOut(info.Config.AckWait),
			Replay:        replays.word(info.Config.ReplayPolicy),
			MaxAckPending: info.Config.MaxAckPending,
			Description:   info.Config.Description,
		},
		Delivered:   Sequences{Consumer: info.Delivered.Consumer, Stream: info.Delivered.Stream},
		AckFloor:    Sequences{Consumer: info.AckFloor.Consumer, Stream: info.AckFloor.Stream},
		Pending:     info.NumPending,
		Redelivered: info.NumRedelivered,
		Waiting:     info.NumWaiting,
		AckPending:  info.NumAckPending,
		Created:     info.Created,
	}
}

// pickIn bounds one page of a stream's consumer listing.
type pickIn struct {
	// Stream is the stream name, from the path.
	Stream string `json:"stream"`
	// Limit caps the consumers returned (1–1000, default 100).
	Limit int `json:"limit"`
	// Offset skips that many consumers, name-ordered.
	Offset int `json:"offset"`
}

// pickOut is one page of a stream's consumers.
type pickOut struct {
	// Consumers is the page, ordered by name.
	Consumers []Consumer `json:"consumers"`
	// Total is the stream's consumer count before paging.
	Total int `json:"total"`
}

// list returns a stream's consumers, name-ordered, with delivery state.
func (co consumers) list(ctx context.Context, in *pickIn) (*pickOut, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := co.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := streams{co.b}.resolve(ctx, org, in.Stream)
	if err != nil {
		return nil, err
	}
	all := []Consumer{}
	lister := st.ListConsumers(ctx)
	for info := range lister.Info() {
		all = append(all, consumer(org, info))
	}
	if err := lister.Err(); err != nil && !errors.Is(err, jetstream.ErrEndOfData) {
		return nil, errHTTP(err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	limit, offset := page(in.Limit, in.Offset)
	total := len(all)
	if offset > total {
		offset = total
	}
	if offset+limit > total {
		limit = total - offset
	}
	return &pickOut{Consumers: all[offset : offset+limit], Total: total}, nil
}

// makeIn creates one durable consumer on the path's stream.
type makeIn struct {
	// Stream is the stream name, from the path.
	Stream string `json:"stream"`
	Durable
}

// create creates a durable pull consumer on a stream and returns it.
func (co consumers) create(ctx context.Context, in *makeIn) (*Consumer, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := in.wire(org)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := co.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := streams{co.b}.resolve(ctx, org, in.Stream)
	if err != nil {
		return nil, err
	}
	c, err := st.CreateConsumer(ctx, cfg)
	if err != nil {
		return nil, errHTTP(err)
	}
	out := consumer(org, c.CachedInfo())
	return &out, nil
}

// twoIn addresses one consumer by stream and name.
type twoIn struct {
	// Stream is the stream name, from the path.
	Stream string `json:"stream"`
	// Name is the consumer name, from the path.
	Name string `json:"name"`
}

// pick resolves the org's consumer or the error the contract names.
func (co consumers) pick(ctx context.Context, org string, in *twoIn) (jetstream.Consumer, error) {
	if !name.MatchString(in.Name) {
		return nil, zip.ErrBadRequest("consumer name must match ^[a-zA-Z0-9_-]{1,256}$")
	}
	st, err := streams{co.b}.resolve(ctx, org, in.Stream)
	if err != nil {
		return nil, err
	}
	c, err := st.Consumer(ctx, in.Name)
	if err != nil {
		return nil, errHTTP(err)
	}
	return c, nil
}

// get returns one consumer's configuration and delivery state.
func (co consumers) get(ctx context.Context, in *twoIn) (*Consumer, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := co.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	c, err := co.pick(ctx, org, in)
	if err != nil {
		return nil, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, errHTTP(err)
	}
	out := consumer(org, info)
	return &out, nil
}

// delete removes a consumer and its delivery state; unacknowledged messages
// stay in the stream.
func (co consumers) delete(ctx context.Context, in *twoIn) (*struct{}, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := co.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := streams{co.b}.resolve(ctx, org, in.Stream)
	if err != nil {
		return nil, err
	}
	if !name.MatchString(in.Name) {
		return nil, zip.ErrBadRequest("consumer name must match ^[a-zA-Z0-9_-]{1,256}$")
	}
	if err := st.DeleteConsumer(ctx, in.Name); err != nil {
		return nil, errHTTP(err)
	}
	return nil, nil
}

// nextIn asks a consumer for its next batch.
type nextIn struct {
	// Stream is the stream name, from the path.
	Stream string `json:"stream"`
	// Name is the consumer name, from the path.
	Name string `json:"name"`
	// Batch is how many messages to pull (1–1000, default 1).
	Batch int `json:"batch"`
	// Expires is how long to wait for messages, e.g. "5s" (default "30s", max "60s").
	Expires string `json:"expires"`
	// NoWait answers immediately with whatever is available instead of waiting.
	NoWait bool `json:"no_wait"`
}

// next pulls the consumer's next batch. Delivered messages are acknowledged on
// delivery — the broker will not redeliver what this call returns; an empty
// wait answers 408.
func (co consumers) next(ctx context.Context, in *nextIn) (*readOut, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	wait, err := dur(in.Expires)
	if err != nil {
		return nil, err
	}
	if wait <= 0 {
		wait = 30 * time.Second
	}
	if wait > maxWait {
		wait = maxWait
	}
	batch := in.Batch
	if batch < 1 || batch > 1000 {
		batch = 1
	}
	// The pull carries its own wait, so the bound is wait plus one management
	// round — not the flat apiTimeout, which would cut a 30s pull at 10.
	if co.b == nil || co.b.nc == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "message plane unreachable")
	}
	ctx, cancel := context.WithTimeout(ctx, wait+apiTimeout)
	defer cancel()
	c, err := co.pick(ctx, org, &twoIn{Stream: in.Stream, Name: in.Name})
	if err != nil {
		return nil, err
	}
	var msgs jetstream.MessageBatch
	if in.NoWait {
		msgs, err = c.FetchNoWait(batch)
	} else {
		msgs, err = c.Fetch(batch, jetstream.FetchMaxWait(wait))
	}
	if err != nil {
		return nil, errHTTP(err)
	}
	out := readOut{Messages: []Delivery{}}
	for msg := range msgs.Messages() {
		item := Delivery{
			Subject: relSubject(org, msg.Subject()),
			Data:    base64.StdEncoding.EncodeToString(msg.Data()),
			Headers: msg.Headers(),
		}
		if meta, err := msg.Metadata(); err == nil {
			item.Sequence = meta.Sequence.Stream
			item.Timestamp = meta.Timestamp
			item.Delivered = int(meta.NumDelivered)
			item.Remaining = meta.NumPending
		}
		_ = msg.Ack()
		out.Messages = append(out.Messages, item)
	}
	if err := msgs.Error(); err != nil {
		return nil, errHTTP(err)
	}
	if len(out.Messages) == 0 && !in.NoWait {
		return nil, zip.Errorf(http.StatusRequestTimeout, "no messages before expiry")
	}
	return &out, nil
}
