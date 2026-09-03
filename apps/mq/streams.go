package mq

// /v1/mq/stream — durable JetStream streams inside the caller's org
// namespace: list, create, inspect, update, delete, purge, and direct message
// access. Every op resolves the org from the validated principal and touches
// only streams whose broker name carries that org's prefix (mq.go, TENANCY).

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/hanzoai/cloud"
	"sort"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"
)

// streams carries the broker client onto the stream ops.
type streams struct{ b *broker }

// Config is a stream's configuration, spec-shaped: subjects are org-relative
// and durations are strings, exactly as the caller states them.
type Config struct {
	// Name is the stream name, unique within the org (alphanumeric, hyphens, underscores).
	Name string `json:"name"`
	// Subjects are the org-relative subjects bound to this stream (wildcards supported). Default: the stream name.
	Subjects []string `json:"subjects"`
	// Retention is the retention policy: limits (default), interest, or workqueue.
	Retention string `json:"retention"`
	// MaxMsgs caps the number of stored messages; -1 (default) is unlimited.
	MaxMsgs int64 `json:"max_msgs"`
	// MaxBytes caps the stream's total stored bytes; -1 (default) is unlimited.
	MaxBytes int64 `json:"max_bytes"`
	// MaxAge caps message age, e.g. "24h" or "7d"; "0" (default) is unlimited.
	MaxAge string `json:"max_age"`
	// MaxMsgSize caps one message's size in bytes; -1 (default) is the broker's limit.
	MaxMsgSize int32 `json:"max_msg_size"`
	// Storage is the storage backend: file (default) or memory.
	Storage string `json:"storage"`
	// Replicas is the number of stream replicas (1–5); this plane runs 1.
	Replicas int `json:"num_replicas"`
}

// State is a stream's current state on the broker.
type State struct {
	// Messages is the number of messages currently stored.
	Messages uint64 `json:"messages"`
	// Bytes is the total stored size.
	Bytes uint64 `json:"bytes"`
	// FirstSeq is the sequence of the first stored message.
	FirstSeq uint64 `json:"first_seq"`
	// FirstTS is the timestamp of the first stored message.
	FirstTS time.Time `json:"first_ts"`
	// LastSeq is the sequence of the last stored message.
	LastSeq uint64 `json:"last_seq"`
	// LastTS is the timestamp of the last stored message.
	LastTS time.Time `json:"last_ts"`
	// Consumers is the number of consumers attached to this stream.
	Consumers int `json:"consumer_count"`
	// Subjects is the number of distinct subjects stored.
	Subjects uint64 `json:"num_subjects"`
	// Deleted is the number of deleted messages (sequence gaps).
	Deleted int `json:"num_deleted"`
}

// Stream is one stream: its configuration and live state.
type Stream struct {
	// Name is the stream name within the org.
	Name string `json:"name"`
	// Config is the stream's configuration.
	Config Config `json:"config"`
	// State is the stream's current state.
	State State `json:"state"`
	// Created is when the stream was created.
	Created time.Time `json:"created"`
}

// wire maps a spec-shaped config into the org's namespace on the broker.
func (in *Config) wire(org string) (jetstream.StreamConfig, error) {
	id, err := streamID(org, in.Name)
	if err != nil {
		return jetstream.StreamConfig{}, err
	}
	retention, err := retentions.of(in.Retention, "retention", jetstream.LimitsPolicy)
	if err != nil {
		return jetstream.StreamConfig{}, err
	}
	storage, err := storages.of(in.Storage, "storage", jetstream.FileStorage)
	if err != nil {
		return jetstream.StreamConfig{}, err
	}
	age, err := dur(in.MaxAge)
	if err != nil {
		return jetstream.StreamConfig{}, err
	}
	rel := in.Subjects
	if len(rel) == 0 {
		rel = []string{in.Name}
	}
	subjects := make([]string, len(rel))
	for i, s := range rel {
		if subjects[i], err = absSubject(org, s); err != nil {
			return jetstream.StreamConfig{}, err
		}
	}
	maxMsgs, maxBytes := in.MaxMsgs, in.MaxBytes
	if maxMsgs == 0 {
		maxMsgs = -1
	}
	if maxBytes == 0 {
		maxBytes = -1
	}
	maxMsgSize := in.MaxMsgSize
	if maxMsgSize == 0 {
		maxMsgSize = -1
	}
	replicas := in.Replicas
	if replicas == 0 {
		replicas = 1
	}
	return jetstream.StreamConfig{
		Name:       id,
		Subjects:   subjects,
		Retention:  retention,
		MaxMsgs:    maxMsgs,
		MaxBytes:   maxBytes,
		MaxAge:     age,
		MaxMsgSize: maxMsgSize,
		Storage:    storage,
		Replicas:   replicas,
	}, nil
}

// present maps broker stream info back into the org's own view.
func present(org string, info *jetstream.StreamInfo) Stream {
	n, _ := streamName(org, info.Config.Name)
	rel := make([]string, len(info.Config.Subjects))
	for i, s := range info.Config.Subjects {
		rel[i] = relSubject(org, s)
	}
	return Stream{
		Name: n,
		Config: Config{
			Name:       n,
			Subjects:   rel,
			Retention:  retentions.word(info.Config.Retention),
			MaxMsgs:    info.Config.MaxMsgs,
			MaxBytes:   info.Config.MaxBytes,
			MaxAge:     durOut(info.Config.MaxAge),
			MaxMsgSize: info.Config.MaxMsgSize,
			Storage:    storages.word(info.Config.Storage),
			Replicas:   info.Config.Replicas,
		},
		State: State{
			Messages:  info.State.Msgs,
			Bytes:     info.State.Bytes,
			FirstSeq:  info.State.FirstSeq,
			FirstTS:   info.State.FirstTime,
			LastSeq:   info.State.LastSeq,
			LastTS:    info.State.LastTime,
			Consumers: info.State.Consumers,
			Subjects:  info.State.NumSubjects,
			Deleted:   info.State.NumDeleted,
		},
		Created: info.Created,
	}
}

// resolve is the one path every by-name op goes through: the org's stream or
// the error the contract names (400 bad name, 404 not the org's).
func (s streams) resolve(ctx context.Context, org, n string) (jetstream.Stream, error) {
	id, err := streamID(org, n)
	if err != nil {
		return nil, err
	}
	st, err := s.b.js.Stream(ctx, id)
	if err != nil {
		return nil, errHTTP(err)
	}
	return st, nil
}

// listIn bounds one page of the stream listing.
type listIn struct {
	// Limit caps the streams returned (1–1000, default 100).
	Limit int `json:"limit"`
	// Offset skips that many streams, name-ordered.
	Offset int `json:"offset"`
}

// Streams is one page of the org's streams.
type Streams struct {
	// Streams is the page, ordered by name.
	Streams []Stream `json:"streams"`
	// Total is the org's stream count before paging.
	Total int `json:"total"`
}

// list returns the org's streams, name-ordered, with their live state.
func (s streams) list(ctx context.Context, in *listIn) (*Streams, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	all := []Stream{}
	lister := s.b.js.ListStreams(ctx)
	for info := range lister.Info() {
		if _, ok := streamName(org, info.Config.Name); ok {
			all = append(all, present(org, info))
		}
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
	return &Streams{Streams: all[offset : offset+limit], Total: total}, nil
}

// create creates a durable stream in the org's namespace and returns it.
func (s streams) create(ctx context.Context, in *Config) (*Stream, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := in.wire(org)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := s.b.js.CreateStream(ctx, cfg)
	if err != nil {
		return nil, errHTTP(err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return nil, errHTTP(err)
	}
	out := present(org, info)
	return &out, nil
}

// nameIn addresses one stream by name; a GET and a DELETE take their input
// from the URL and carry no body.
type nameIn struct {
	// Name is the stream name, from the path.
	Name string `json:"name"`
}

// get returns one stream's configuration and live state.
func (s streams) get(ctx context.Context, in *nameIn) (*Stream, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := s.resolve(ctx, org, in.Name)
	if err != nil {
		return nil, err
	}
	info, err := st.Info(ctx)
	if err != nil {
		return nil, errHTTP(err)
	}
	out := present(org, info)
	return &out, nil
}

// update reconfigures an existing stream; the path names the stream, and the
// immutable fields (storage, retention) must restate what they are.
func (s streams) update(ctx context.Context, in *Config) (*Stream, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := in.wire(org)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := s.b.js.UpdateStream(ctx, cfg)
	if err != nil {
		return nil, errHTTP(err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return nil, errHTTP(err)
	}
	out := present(org, info)
	return &out, nil
}

// delete removes a stream with all its messages and consumers. Irreversible.
func (s streams) delete(ctx context.Context, in *nameIn) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	id, err := streamID(org, in.Name)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err := s.b.js.DeleteStream(ctx, id); err != nil {
		return nil, errHTTP(err)
	}
	return nil, nil
}

// Purge narrows a purge: by org-relative subject filter, or keeping the
// newest messages.
type Purge struct {
	// Name is the stream name, from the path.
	Name string `json:"name"`
	// Filter purges only messages on this org-relative subject (wildcards supported).
	Filter string `json:"filter"`
	// Keep retains that many newest messages.
	Keep uint64 `json:"keep"`
}

// purgeOut reports what a purge removed.
type purgeOut struct {
	// Purged is the number of messages removed.
	Purged uint64 `json:"purged"`
}

// purge removes messages from a stream, leaving its consumers in place.
func (s streams) purge(ctx context.Context, in *Purge) (*purgeOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := s.resolve(ctx, org, in.Name)
	if err != nil {
		return nil, err
	}
	before, err := st.Info(ctx)
	if err != nil {
		return nil, errHTTP(err)
	}
	var opts []jetstream.StreamPurgeOpt
	if in.Filter != "" {
		subj, err := absSubject(org, in.Filter)
		if err != nil {
			return nil, err
		}
		opts = append(opts, jetstream.WithPurgeSubject(subj))
	}
	if in.Keep > 0 {
		opts = append(opts, jetstream.WithPurgeKeep(in.Keep))
	}
	if err := st.Purge(ctx, opts...); err != nil {
		return nil, errHTTP(err)
	}
	after, err := st.Info(ctx)
	if err != nil {
		return nil, errHTTP(err)
	}
	purged := uint64(0)
	if before.State.Msgs > after.State.Msgs {
		purged = before.State.Msgs - after.State.Msgs
	}
	return &purgeOut{Purged: purged}, nil
}

// messages carries the broker client onto the direct message-access ops.
type messages struct{ b *broker }

// Delivery is one stored message, payload base64-encoded.
type Delivery struct {
	// Subject is the org-relative subject the message was stored under.
	Subject string `json:"subject"`
	// Data is the payload, base64-encoded.
	Data string `json:"data"`
	// Headers are the message headers, when any were published.
	Headers map[string][]string `json:"headers,omitempty"`
	// Sequence is the message's stream sequence.
	Sequence uint64 `json:"sequence"`
	// Timestamp is when the broker stored the message.
	Timestamp time.Time `json:"timestamp"`
	// Delivered is how many times a consumer has been handed this message (pulls only).
	Delivered int `json:"num_delivered,omitempty"`
	// Remaining is how many messages follow this one for the consumer (pulls only).
	Remaining uint64 `json:"num_pending,omitempty"`
}

// raw presents one stored broker message org-relative.
func raw(org string, m *jetstream.RawStreamMsg) Delivery {
	return Delivery{
		Subject:   relSubject(org, m.Subject),
		Data:      base64.StdEncoding.EncodeToString(m.Data),
		Headers:   m.Header,
		Sequence:  m.Sequence,
		Timestamp: m.Time,
	}
}

// readIn addresses stored messages directly: one by sequence, the last on a
// subject, or a walk forward from a sequence along a subject.
type readIn struct {
	// Name is the stream name, from the path.
	Name string `json:"name"`
	// Seq reads the message at this sequence (with next_by_subject: the walk's start).
	Seq uint64 `json:"seq"`
	// LastBySubject reads the newest message on this org-relative subject.
	LastBySubject string `json:"last_by_subject"`
	// NextBySubject walks forward from seq collecting messages on this org-relative subject (wildcards supported).
	NextBySubject string `json:"next_by_subject"`
	// Limit caps a next_by_subject walk (1–1000, default 100).
	Limit int `json:"limit"`
}

// readOut is the messages a direct read found.
type readOut struct {
	// Messages is what was read, stream-ordered.
	Messages []Delivery `json:"messages"`
}

// list reads stored messages without a consumer: by sequence, by newest on a
// subject, or walking a subject forward from a sequence.
func (m messages) list(ctx context.Context, in *readIn) (*readOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := m.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := streams{m.b}.resolve(ctx, org, in.Name)
	if err != nil {
		return nil, err
	}
	switch {
	case in.NextBySubject != "":
		subj, err := absSubject(org, in.NextBySubject)
		if err != nil {
			return nil, err
		}
		limit, _ := page(in.Limit, 0)
		seq := in.Seq
		if seq == 0 {
			seq = 1
		}
		out := readOut{Messages: []Delivery{}}
		for len(out.Messages) < limit {
			msg, err := st.GetMsg(ctx, seq, jetstream.WithGetMsgSubject(subj))
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				break
			}
			if err != nil {
				return nil, errHTTP(err)
			}
			out.Messages = append(out.Messages, raw(org, msg))
			seq = msg.Sequence + 1
		}
		return &out, nil
	case in.LastBySubject != "":
		subj, err := absSubject(org, in.LastBySubject)
		if err != nil {
			return nil, err
		}
		msg, err := st.GetLastMsgForSubject(ctx, subj)
		if err != nil {
			return nil, errHTTP(err)
		}
		return &readOut{Messages: []Delivery{raw(org, msg)}}, nil
	case in.Seq > 0:
		msg, err := st.GetMsg(ctx, in.Seq)
		if err != nil {
			return nil, errHTTP(err)
		}
		return &readOut{Messages: []Delivery{raw(org, msg)}}, nil
	}
	return nil, zip.ErrBadRequest("one of seq, last_by_subject or next_by_subject is required")
}

// seqIn addresses one stored message by stream and sequence.
type seqIn struct {
	// Name is the stream name, from the path.
	Name string `json:"name"`
	// Seq is the message's stream sequence, from the path.
	Seq uint64 `json:"seq"`
}

// delete erases one message by sequence; the sequence gap remains.
func (m messages) delete(ctx context.Context, in *seqIn) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := m.b.live(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := streams{m.b}.resolve(ctx, org, in.Name)
	if err != nil {
		return nil, err
	}
	if err := st.DeleteMsg(ctx, in.Seq); err != nil {
		return nil, errHTTP(err)
	}
	return nil, nil
}
