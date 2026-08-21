// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// replay.go — the session-replay snapshot door.
//
//	POST /v1/event/replay   body: {sessionId, windowId, distinctId, events:[…]}  ->  {accepted, dropped}
//
// A browser recorder posts a batch of rrweb events; this door turns it into ONE
// message on the replay ingest topic and answers the SAME receipt every other
// analytics door answers. It is the whole of cloud's part: the downstream
// ingester consumes the topic, writes the snappy blocks to object storage and
// computes the summary row the player reads. Cloud does not touch object storage
// and does not touch the warehouse on this path.
//
// IT IS NOT A `doors` ENTRY, and cannot be. Every row of that table is a wire that
// decodes to []CaptureEvent and flows through the ONE write core onto the event
// plane (ingestEvents → the JetStream fact plane → the warehouse). A snapshot batch
// decodes to none of that: it is an opaque rrweb recording bound for a different
// consumer on a different transport. Forcing it into the doors table would mean
// either a decoder that returns no events (a door that always drops) or a second
// meaning for CaptureEvent. So it is registered explicitly in routes, exactly as
// the Sentry relay is — the other route on this surface whose body is not the
// canonical wire.
//
// WHAT IS SHARED IS ADMISSION, and that is the half that matters. The credential is
// resolved by eventTenant — the SAME pluggable, fail-closed, strict-trust-order
// resolver every door uses — and refused with the SAME cannotAttribute/cannotWrite
// vocabulary. There is no second resolver here, so a pk- that works on /v1/event
// works here, and a key that is refused there is refused here, forever, by
// construction rather than by two functions being kept in agreement.
//
// THE TENANT IS NEVER READ FROM THE BODY, and it is the ONLY thing this door tells the
// ingester about who is writing. The org is whatever eventTenant resolved from the
// credential; it rides the message as a ROUTING fact, and the ingester files the
// recording under the project that org owns. It is not a credential and it does not
// authenticate anything — the authentication already happened here, once, against the
// IAM-issued pk-, and a message on this topic is only ever produced by this door.
//
// So there is no second token anywhere on this path. An org's recordings cannot be
// steered into another tenant's stream by naming one, because the caller never names
// it: the value on the wire is the server-resolved org and nothing a caller can set.
//
// A PRODUCE FAILURE IS A 503, NEVER A 200. The produce is the commit point, exactly
// as the fact publish is on the event plane (bus.go): the door answers "accepted"
// only after the broker has the message. A fire-and-forget produce would turn every
// receipt into a maybe, which is the failure mode `answer` (event.go) exists to make
// impossible.

package event

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/cloud"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/zap-proto/zip"
)

// replayPath is the door. No /api/ prefix and no version but v1 — the house rule
// for every route on api.hanzo.ai.
const replayPath = "/v1/event/replay"

// sourceReplay is this door's origin tag. It is a METRIC label only, and it lives
// here rather than in capture.go's source block deliberately: every tag in that
// block is stamped into a warehouse row's $source by ingestEvents, and this door
// writes no row. Naming it there would put a value in a table whose comment promises
// one tag per entry in `doors` — a promise this door is precisely the exception to.
const sourceReplay = "replay"

// The snapshot contract, as CONSTANTS rather than literals at the point of use.
// Every one of them is read by a consumer this repo does not own, so each is a
// published fact: changing one is a wire change, and it should take an edit here.
const (
	// snapshotTopic is the topic the replay ingester consumes.
	snapshotTopic = "session_recording_snapshot_item_events"
	// snapshotEvent is the event name, carried BOTH in the envelope and in the
	// `event` message header.
	snapshotEvent = "$snapshot_items"
	// snapshotSource names the recorder's platform.
	snapshotSource = "web"
	// snapshotLib is the SDK that produced the recording.
	snapshotLib = "@hanzo/replay"
	// snapshotTime is RFC3339 at millisecond precision — the rendering the `now`
	// and `timestamp` envelope fields and the `now` header all carry. Seconds-only
	// RFC3339 would lose the ordering of two batches inside one second.
	snapshotTime = "2006-01-02T15:04:05.000Z07:00"
)

// maxSessionID is the longest session id the pipeline accepts. It mirrors the
// upstream producer's own check, and it is enforced HERE because this value becomes
// the PARTITION KEY: an id the downstream refuses is a message that is produced,
// partitioned and then dropped at the far end, which the caller would see as a 200.
const maxSessionID = 70

// maxReplayBytes bounds ONE snapshot request, and it is the ONLY bound on one, because
// there is only one thing to bound: how much a single message can carry. A second cap
// on the event COUNT would be a second answer to that question, arbitrary where this
// one is derived, and a recorder that hit it would be told 400 about a batch whose
// only problem was its size.
//
// THE BYTE CAP IS NOT ARBITRARY AND IT IS NOT THE GLOBAL ONE. One batch becomes ONE
// Kafka message, and the transport under the embedded broker is the platform bus,
// whose default maximum payload is 1 MiB. The envelope also carries the recording
// DOUBLE-ENCODED — `data` is a JSON string, so every quote in the rrweb payload is
// escaped, and a quote-dense recording inflates well past its raw size. 512 KiB of
// body therefore still fits a 1 MiB message with room for the escaping, while the
// global 16 MiB BodyLimit (config.go) would not: a body admitted there would be
// produced, refused by the bus, and answered 503 for a reason the caller cannot act
// on. Bounding at the door turns that into an honest 413 the recorder can chunk
// against.
const maxReplayBytes = 512 << 10

// replayProduceTimeout bounds ONE produce so a wedged broker cannot hold an ingest
// request open; the caller gets an honest 503 instead. Same budget, same reason, as
// the event plane's publishTimeout.
const replayProduceTimeout = 5 * time.Second

// replayBody is the PUBLIC wire, and it is deliberately not the pipeline's. The
// message the ingester reads is a PostHog-flavored envelope with $-prefixed
// properties and a double-encoded body; none of that is a fact about Hanzo's API, so
// none of it appears here. A recorder posts the four things it actually knows.
type replayBody struct {
	// SessionID groups every batch of one recorded visit and is the message's
	// partition key, so all of a session's batches stay ordered behind one another.
	// Required: at most 70 characters, ASCII letters, digits and '-'.
	SessionID string `json:"sessionId"`
	// WindowID distinguishes two tabs inside one session. Optional.
	WindowID string `json:"windowId"`
	// DistinctID is the person the recording is attributed to. Optional.
	DistinctID string `json:"distinctId"`
	// Events is the rrweb batch, each element a raw eventWithTime object
	// ({type, timestamp, data}). Carried VERBATIM — the ingester derives the
	// summary (click, keypress and mouse-activity counts, size) from these bytes,
	// so cloud re-encodes nothing and drops no field it does not understand.
	Events []json.RawMessage `json:"events"`
}

// validSessionID reports whether an id may be used as the partition key: non-empty,
// at most maxSessionID characters, and ASCII alphanumeric or '-' throughout.
//
// It is BYTE-wise on purpose. Ranging a string in Go yields runes, so a multi-byte
// character would be counted as one against a limit the downstream applies to bytes,
// and this check has to refuse exactly what the pipeline refuses — a length this
// accepts and the far end rejects is a message produced into a partition and then
// dropped, reported to the caller as a success.
func validSessionID(id string) bool {
	if id == "" || len(id) > maxSessionID {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// ── the message ─────────────────────────────────────────────────────────────

// snapshotEnvelope is the OUTER message value. `Data` is the inner document as a
// JSON STRING — the encoding is DOUBLE on purpose, because that is what the consumer
// parses; it reads Data as text and unmarshals it a second time.
//
// It carries no tenant field. The org travels in the message HEADER, in ONE copy,
// because the header is where the ingester reads it — a second copy in the envelope
// would be a value that can only ever agree with itself, or disagree.
type snapshotEnvelope struct {
	UUID       string `json:"uuid"`
	DistinctID string `json:"distinct_id"`
	IP         string `json:"ip"`
	Data       string `json:"data"`
	Now        string `json:"now"`
	Event      string `json:"event"`
	Timestamp  string `json:"timestamp"`
}

// snapshotData is the INNER document, the one carried as text in the envelope's
// `data`. Its property names are the pipeline's, $-prefixes and all: this is the
// one place the public wire is translated into the consumer's vocabulary, which is
// precisely why replayBody does not have to speak it.
type snapshotData struct {
	Event      string             `json:"event"`
	Properties snapshotProperties `json:"properties"`
}

type snapshotProperties struct {
	DistinctID     string            `json:"distinct_id"`
	SessionID      string            `json:"$session_id"`
	WindowID       string            `json:"$window_id"`
	SnapshotSource string            `json:"$snapshot_source"`
	SnapshotItems  []json.RawMessage `json:"$snapshot_items"`
	Lib            string            `json:"$lib"`
}

// snapshotRecord renders ONE accepted batch as the message the replay ingester
// consumes: the partition key, the seven headers, and the double-encoded envelope.
//
// It is PURE — every varying input (the id, the clock, the resolved org's token, the
// client address) is a parameter rather than something it reaches for — so the whole
// wire contract is asserted directly in a test, byte for byte, with no broker and no
// clock. That matters more here than anywhere else on this surface: the consumer is
// out of this repo and out of this language, so a drift in a header name or in the
// double encoding cannot be caught by anything in the build. The test IS the
// contract.
//
// THE ORG TRAVELS IN A HEADER, in one copy, and it is the whole of what this message
// says about tenancy. The ingester resolves it to the project the org owns and files
// the recording there; a message whose org resolves to no project is dropped at the
// far end rather than filed anywhere, which is the same fail-closed shape this door
// answers with.
func snapshotRecord(in replayBody, org, ip, id string, now time.Time) (*kgo.Record, error) {
	stamp := now.UTC().Format(snapshotTime)
	data, err := json.Marshal(snapshotData{
		Event: snapshotEvent,
		Properties: snapshotProperties{
			DistinctID:     in.DistinctID,
			SessionID:      in.SessionID,
			WindowID:       in.WindowID,
			SnapshotSource: snapshotSource,
			SnapshotItems:  in.Events,
			Lib:            snapshotLib,
		},
	})
	if err != nil {
		return nil, err
	}
	// The second encode is what makes `data` a STRING in the outer object. Marshal
	// of a Go string does exactly that escaping, so the double encoding is a
	// property of the type and not of a hand-built literal.
	value, err := json.Marshal(snapshotEnvelope{
		UUID:       id,
		DistinctID: in.DistinctID,
		IP:         ip,
		Data:       string(data),
		Now:        stamp,
		Event:      snapshotEvent,
		Timestamp:  stamp,
	})
	if err != nil {
		return nil, err
	}
	return &kgo.Record{
		Topic: snapshotTopic,
		// The session id, raw. It is the partition key, so every batch of one
		// recording lands on one partition and stays in order behind the last.
		Key:   []byte(in.SessionID),
		Value: value,
		Headers: []kgo.RecordHeader{
			// The tenant, as the ingester's routing key. It is the SERVER-RESOLVED
			// org — the same value every other write on this surface is attributed
			// to — and it replaces the project token this message used to carry.
			{Key: "org", Value: []byte(org)},
			{Key: "distinct_id", Value: []byte(in.DistinctID)},
			{Key: "session_id", Value: []byte(in.SessionID)},
			// Unix MILLIS as a decimal string — the one header that is not the same
			// rendering as its envelope twin.
			{Key: "timestamp", Value: []byte(strconv.FormatInt(now.UnixMilli(), 10))},
			{Key: "event", Value: []byte(snapshotEvent)},
			{Key: "uuid", Value: []byte(id)},
			{Key: "now", Value: []byte(stamp)},
		},
	}, nil
}

// ── the producer ────────────────────────────────────────────────────────────

// replayBrokersEnv is where this process reaches the platform's Kafka wire. It
// defaults to the in-cluster Service that fronts the embedded adaptor (apps/kafka,
// which serves the Kafka binary protocol over the same JetStream everything else on
// this plane rides).
const replayBrokersEnv = "CLOUD_REPLAY_BROKERS"

// defaultReplayBrokers is that Service.
const defaultReplayBrokers = "kafka:9092"

func replayBrokers() []string {
	raw := strings.TrimSpace(os.Getenv(replayBrokersEnv))
	if raw == "" {
		raw = defaultReplayBrokers
	}
	return strings.Split(raw, ",")
}

// producer holds the process's ONE Kafka client, dialed on first use and reused.
// A struct rather than a bare var so the dial is guarded by a single mutex: a burst
// of concurrent first requests dials once, not once per request. Same shape, and the
// same reason, as bus.go's connection to the event plane.
type producer struct {
	mu     sync.Mutex
	client *kgo.Client
}

var snapshots = &producer{}

// connect returns the live client, dialing on first use.
//
// IT GOES OVER THE WIRE, and that is a correctness decision rather than a
// convenience. The broker assigns offsets under a per-partition lock it holds
// privately (hanzoai/kafka protocol.Broker: partitionMu, partitionBounds,
// stampBaseOffsets), so producing is only sound THROUGH it. Reaching around it —
// publishing record bytes straight onto the partition's subject — would skip both
// the lock and the offset stamp and corrupt the partition for every consumer, which
// is why no such shortcut exists here.
func (p *producer) connect() (*kgo.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(replayBrokers()...),
		kgo.ClientID("hanzo-cloud-replay"),
		// UNCOMPRESSED, deliberately. A record batch is stored and served verbatim,
		// so the codec is a contract with the CONSUMER, not with the broker — and
		// the consumer is out of this repo. franz-go would negotiate snappy by
		// default; an uncompressed batch is readable by every client that speaks the
		// protocol at all, and the traffic is in-cluster where the bandwidth this
		// costs does not signify.
		kgo.ProducerBatchCompression(kgo.NoCompression()),
	)
	if err != nil {
		return nil, err
	}
	p.client = cl
	return cl, nil
}

// close releases the client on graceful shutdown. Idempotent.
func (p *producer) close() {
	p.mu.Lock()
	cl := p.client
	p.client = nil
	p.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
}

// produce is this door's ONE seam onto the broker, and the only way a recording
// leaves this package. It is a package var for exactly the reason `publish` is
// (bus.go): production is always produceSnapshot, and a test substitutes it to read
// back the record a request actually produced — the headers and the double-encoded
// value are the whole contract, and none of it is observable through a status code.
var produce = produceSnapshot

// produceSnapshot produces one record, SYNCHRONOUSLY. The produce is the commit
// point: the door answers a receipt only once the broker has the message, so an
// "accepted" means durable rather than "buffered in this process".
func produceSnapshot(ctx context.Context, rec *kgo.Record) error {
	ctx, cancel := context.WithTimeout(ctx, replayProduceTimeout)
	defer cancel()
	cl, err := snapshots.connect()
	if err != nil {
		return err
	}
	return cl.ProduceSync(ctx, rec).FirstErr()
}

// closeProducer releases the Kafka client. Cloud calls it on graceful shutdown,
// beside the event plane's own close.
func closeProducer() { snapshots.close() }

// ── the door ────────────────────────────────────────────────────────────────

// replayIngest is the handler: admission, then bounds, then the one produce.
//
// The ORDER is the wire and is chosen for the same reason public.go's is — a caller
// must be told the FIRST thing that is wrong with its request, and the first thing
// is always whether anybody vouched for it. Credential, then size, then shape, then
// the tenant's configuration, then the produce.
func replayIngest(_ *cloud.Service[state], c *zip.Ctx) error {
	a, ok := eventTenant(c)
	if !ok {
		return cannotAttribute(presented(c))
	}
	// A REDUCED principal is refused rather than projected. The projection
	// (publicIngest) narrows an event to a pageview or an error in a closed
	// vocabulary; a screen recording has no such reduction — it is the full-fidelity
	// capture of a session, and there is no version of it that is safe for a guest
	// to write into a host org it was invited into for one channel. So the answer is
	// the capability refusal that already exists, not a weaker recording.
	if !a.full {
		return cannotWrite(true)
	}

	body := c.Body()
	if len(body) > maxReplayBytes {
		return zip.Errorf(http.StatusRequestEntityTooLarge,
			"snapshot payload too large: %d bytes, limit %d — send the recording in smaller batches", len(body), maxReplayBytes)
	}
	var in replayBody
	if err := json.Unmarshal(body, &in); err != nil {
		return zip.ErrBadRequest("malformed snapshot payload")
	}
	if !validSessionID(in.SessionID) {
		return zip.ErrBadRequest("sessionId must be 1-70 characters of ASCII letters, digits or '-'")
	}
	if len(in.Events) == 0 {
		return zip.ErrBadRequest("no rrweb events in this batch")
	}

	// The org is the one eventTenant resolved from the credential, and it goes on the
	// wire as-is. There is nothing to look up and nothing that can be unconfigured:
	// the only way to reach this line is to have authenticated, and the tenant a
	// credential resolves to is exactly the tenant its recordings are filed under.
	rec, err := snapshotRecord(in, a.org, cloud.ClientIP(c), uuid.NewString(), time.Now())
	if err != nil {
		return zip.ErrBadRequest("snapshot payload cannot be encoded")
	}
	if err := produce(c.Context(), rec); err != nil {
		// 503 and never a 200 with a count. The batch was admitted and could not be
		// made durable, which is the caller's business — it is the one thing a
		// recorder can act on, by retrying.
		// org is NOT repeated here: the request logger already binds it, and a second
		// copy in the same line is a field that can only ever agree with itself.
		c.Log().Warn("replay snapshot produce failed", "session", in.SessionID, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "session replay ingest unavailable: %v", err)
	}
	// The SAME receipt every other door answers. One batch is one accepted unit:
	// the recording is produced whole or not at all, so there is no partial count to
	// report and `dropped` is honestly zero.
	cloud.ObserveIngest(sourceReplay, 1, 0)
	return c.JSON(http.StatusOK, CaptureResult{Accepted: 1})
}
