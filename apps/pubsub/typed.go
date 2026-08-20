package pubsub

// typed.go is the pubsub PRODUCT: the /v1/pubsub door onto the same embedded
// bus every in-process app rides. Eighteen typed ops — publish, request/reply,
// stream and consumer management, a pull fetch, and the key-value store — each
// one registry entry projecting the REST route, the OpenAPI schema and prose,
// the MCP tool, the CLI command and every generated SDK method.
//
// EVERY op drives the real plane. There is no store of its own and no second
// bus: each handler dials the ONE bus this process serves (in-process when it
// is the embedded server, CLOUD_PUBSUB_URL when a deployment pointed the knob
// elsewhere) and relays JetStream's own answer. A JetStream refusal surfaces as
// the 4xx it is, never as a reshaped success.
//
// TENANCY IS A NAMESPACE, NOT AN ACCOUNT. The embedded node runs one JetStream
// domain shared with the platform's own planes (the event stream, o11y, the
// Kafka facade), so the door scopes tenants by construction rather than by
// NATS accounts:
//
//   - subjects: org subjects live under "pub.<org>." — mapped on the way in,
//     stripped on the way out, so a caller sees its own clean namespace and can
//     never name another org's subjects or the platform's (event.>, $KV.>).
//   - streams and KV buckets: physical name "t-<org>-<name>". Caller names
//     carry no dash, so the physical name decodes to exactly one (org, name)
//     pair and one org's handle can never resolve to another org's stream. The
//     "t-" prefix keeps the tenant plane disjoint from platform streams.
//
// The NATS client port (:4222) is the CLUSTER'S plane — in-process apps and
// in-cluster clients, unscoped, exactly as before. This door is the TENANT'S
// plane. One bus, two doors, and only the tenant door does tenancy.
//
// NOT SERVED, by decision rather than omission — the intent spec
// (hanzoai/openapi d86248f^:pubsub/openapi.yaml) authored 29 operations; the
// twelve below are refused and TestRefusedPubsubOpsStayRefused pins each
// address to a route-level 404:
//
//   - GET /v1/pubsub/subscribe (SSE): a stream is not a typed op — zip's typed
//     path writes ONE JSON Out and has no vocabulary for text/event-stream
//     (same measured fact that keeps POST /v1/ask untyped, apps/ask/ask.go).
//     Consumption is served instead by the pull op (…/consumers/:name/next)
//     and by the NATS port, which speaks native subscriptions.
//   - /v1/pubsub/objects/* (4 ops): cloud's object door is /v1/storage. A
//     second object store riding stream chunks would be two doors to one noun.
//   - /v1/pubsub/{varz,connz,jsz,routez,gatewayz,leafz,subsz} (6 ops + subsz):
//     operator telemetry, server-wide and cross-tenant by definition — connz
//     lists every client of every tenant. The operator plane is apps/o11y;
//     publishing it on a tenant surface would be a leak, not a feature.
//
// Payloads are TEXT. `data` and `value` are JSON strings carried verbatim as
// UTF-8 bytes — the JSON door is a text door, and its own round trip is exact.
// Binary payloads belong on the NATS port; bytes published there that are not
// UTF-8 read back lossily here.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go — the ONLY way this prose reaches the published document and
// the MCP tool list (Go drops comments at compile time). Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the door to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so
// it arrives as a RECEIVER and every op is a method value (o.publish), the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// state is empty on purpose: the door owns no store. Its whole state is the
// package's running server (srv) and the one cached client connection (door).
type state struct{}

// noInput is the In of an op that takes nothing off the wire — it is addressed
// entirely by the caller's validated principal.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. An ALIAS
// for the unnamed empty struct, not a definition: zip keys the response on 204
// only when the Out type has no name.
type noContent = struct{}

// routes registers the tenant door. Registration order is match order, but every
// path here begins with a distinct static segment (publish, request, jetstream,
// kv), so nothing can shadow anything.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	// A typed op receives only a context, so the validated org reaches it by
	// being parked there — never as an In field. cloud.Bridge parks it, and the
	// composer owns that install: the fused host at its root, a plugin program
	// in its constructor.
	g := app.Group("/v1/pubsub")

	zip.Post(g, "/publish", o.publish)
	zip.Post(g, "/request", o.request)

	zip.Post(g, "/kv/:bucket", o.createBucket, zip.WithStatus(http.StatusCreated))
	zip.Delete(g, "/kv/:bucket", o.deleteBucket)
	zip.Get(g, "/kv/:bucket/:key", o.get)
	zip.Put(g, "/kv/:bucket/:key", o.put)
	zip.Delete(g, "/kv/:bucket/:key", o.del)
	zip.Get(g, "/kv/:bucket/:key/history", o.history)
}

// ----- the bus, from the door's side ----------------------------------------

// door holds the product door's ONE client connection to the plane, dialed on
// first use and reused. Guarded so a burst of concurrent first requests dials
// once; re-dials after a close rather than latching.
var door struct {
	mu sync.Mutex
	nc *nats.Conn
}

// bus returns the live JetStream handle over the door's connection. It dials
// the SAME plane every app in this process uses: CLOUD_PUBSUB_URL when set (the
// one knob), else the embedded server in-process — no TCP, and correct even
// when tests bind an ephemeral port.
func bus() (jetstream.JetStream, *nats.Conn, error) {
	door.mu.Lock()
	defer door.mu.Unlock()
	if door.nc == nil || door.nc.IsClosed() {
		var (
			nc  *nats.Conn
			err error
		)
		if u := strings.TrimSpace(os.Getenv(urlEnv)); u != "" {
			nc, err = nats.Connect(u, nats.Name("pubsub-door"), nats.MaxReconnects(-1))
		} else if srv != nil {
			nc, err = nats.Connect("", nats.InProcessServer(srv.NATS()), nats.Name("pubsub-door"), nats.MaxReconnects(-1))
		} else {
			return nil, nil, zip.Errorf(http.StatusServiceUnavailable, "bus not mounted")
		}
		if err != nil {
			return nil, nil, zip.Errorf(http.StatusServiceUnavailable, "bus unreachable: %v", err)
		}
		door.nc = nc
	}
	js, err := jetstream.New(door.nc)
	if err != nil {
		return nil, nil, zip.Errorf(http.StatusServiceUnavailable, "bus unreachable: %v", err)
	}
	return js, door.nc, nil
}

// closeDoor releases the door's connection on Shutdown, so a remount dials the
// new server instead of a dead pipe.
func closeDoor() {
	door.mu.Lock()
	defer door.mu.Unlock()
	if door.nc != nil {
		door.nc.Close()
		door.nc = nil
	}
}

// busErr maps the plane's own refusals onto the wire honestly: absence is 404,
// a name already taken is 409, a config JetStream refuses is the 4xx it
// reports, and only a genuinely broken bus is a 5xx.
func busErr(err error) error {
	var apiErr *jetstream.APIError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jetstream.ErrStreamNotFound):
		return zip.ErrNotFound("stream not found")
	case errors.Is(err, jetstream.ErrConsumerNotFound):
		return zip.ErrNotFound("consumer not found")
	case errors.Is(err, jetstream.ErrBucketNotFound):
		return zip.ErrNotFound("bucket not found")
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return zip.ErrNotFound("key not found")
	case errors.Is(err, jetstream.ErrStreamNameAlreadyInUse):
		return zip.Errorf(http.StatusConflict, "stream name already in use")
	case errors.Is(err, jetstream.ErrConsumerExists):
		return zip.Errorf(http.StatusConflict, "consumer already exists")
	case errors.Is(err, jetstream.ErrBucketExists):
		return zip.Errorf(http.StatusConflict, "bucket already exists")
	case errors.As(err, &apiErr):
		if apiErr.Code >= 400 && apiErr.Code < 500 {
			return zip.Errorf(apiErr.Code, "%s", apiErr.Description)
		}
		return zip.Errorf(http.StatusInternalServerError, "bus: %s", apiErr.Description)
	case errors.Is(err, context.DeadlineExceeded):
		return zip.Errorf(http.StatusGatewayTimeout, "bus timeout")
	default:
		return zip.Errorf(http.StatusInternalServerError, "bus: %v", err)
	}
}

// ----- tenancy --------------------------------------------------------------

// TenantPrefix marks every tenant-created stream and KV bucket, keeping the
// tenant plane disjoint by construction from the platform's own streams (EVENT
// et al.), which never carry it.
//
// It is EXPORTED for the one question a platform subsystem must be able to ask
// before it removes a stream it did not create: is this a tenant's? Asking the
// prefix's OWNER is what keeps that check from becoming a second "t-" literal
// somewhere else, which is how the two would drift apart.
const TenantPrefix = "t-"

// orgOf resolves the VALIDATED org — the tenant-isolation key — from the
// context cloud.Bridge parked it on. It is never an In field: an In field is
// caller-supplied, so a tenant key read from one would be a cross-tenant read
// the caller asserted for itself. Fails closed off the HTTP path with the same
// 403 every data plane answers.
//
// The org id must also be usable as a NATS subject token and name fragment;
// one that is not (outside [A-Za-z0-9_-], or over 64 bytes) is refused rather
// than mangled — a mangling could collide two orgs onto one namespace.
func orgOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("valid bearer required")
	}
	if !token(org, true) {
		return "", zip.ErrForbidden("org id is not addressable on the bus")
	}
	return org, nil
}

// token reports whether s is 1–64 bytes of [A-Za-z0-9_], plus '-' when dash is
// allowed. Stream and bucket names refuse the dash so the physical name
// "t-<org>-<name>" splits at its LAST dash into exactly one (org, name) pair.
func token(s string, dash bool) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		case c == '-' && dash:
		default:
			return false
		}
	}
	return true
}

// root is the org's subject namespace: every subject this door touches lives
// under it, so no caller can name another org's subjects or the platform's.
func root(org string) string { return "pub." + org + "." }

// phys is the physical (whole-plane) name of an org's stream or bucket.
func phys(org, name string) string { return TenantPrefix + org + "-" + name }

// mapSubject validates a caller subject and roots it in the org's namespace.
// Wildcards ('*', and '>' as the last token) are legal only where wild says so:
// consumer filters and stream bindings take them, a publish target cannot.
func mapSubject(org, s string, wild bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", zip.ErrBadRequest("subject is required")
	}
	if len(s) > 256 {
		return "", zip.ErrBadRequest("subject too long (max 256)")
	}
	tokens := strings.Split(s, ".")
	for i, t := range tokens {
		switch {
		case t == "":
			return "", zip.ErrBadRequest("subject has an empty token")
		case t == "*":
			if !wild {
				return "", zip.ErrBadRequest("wildcard subject not allowed here")
			}
		case t == ">":
			if !wild || i != len(tokens)-1 {
				return "", zip.ErrBadRequest("'>' must be the last token")
			}
		default:
			for j := 0; j < len(t); j++ {
				if c := t[j]; c <= ' ' || c > '~' || c == '*' || c == '>' {
					return "", zip.ErrBadRequest("subject has an invalid character")
				}
			}
		}
	}
	return root(org) + s, nil
}

// unmap returns the caller's view of a plane subject: the org root stripped. A
// subject outside the root (an inbox reply, say) passes through unchanged.
func unmap(org, s string) string { return strings.TrimPrefix(s, root(org)) }

// owns reports whether every subject a stream binds sits inside the org's
// namespace — the belt over the name decode's braces, so even a physical name
// forged into existence by some other path never resolves cross-tenant.
func owns(org string, cfg jetstream.StreamConfig) bool {
	for _, s := range cfg.Subjects {
		if !strings.HasPrefix(s, root(org)) {
			return false
		}
	}
	return len(cfg.Subjects) > 0
}

// streamOf resolves ONE org stream by its caller-visible name, refusing names
// that cannot be an org stream and answering absence as 404.
func streamOf(ctx context.Context, js jetstream.JetStream, org, name string) (jetstream.Stream, error) {
	if !token(name, false) {
		return nil, zip.ErrNotFound("stream not found")
	}
	st, err := js.Stream(ctx, phys(org, name))
	if err != nil {
		return nil, busErr(err)
	}
	if !owns(org, st.CachedInfo().Config) {
		return nil, zip.ErrNotFound("stream not found")
	}
	return st, nil
}

// ----- enums ----------------------------------------------------------------

func parseStorage(s string) (jetstream.StorageType, error) {
	switch s {
	case "", "file":
		return jetstream.FileStorage, nil
	case "memory":
		return jetstream.MemoryStorage, nil
	}
	return 0, zip.ErrBadRequest("storage must be file or memory")
}

func parseRetention(s string) (jetstream.RetentionPolicy, error) {
	switch s {
	case "", "limits":
		return jetstream.LimitsPolicy, nil
	case "interest":
		return jetstream.InterestPolicy, nil
	case "workqueue":
		return jetstream.WorkQueuePolicy, nil
	}
	return 0, zip.ErrBadRequest("retention must be limits, interest or workqueue")
}

func parseDiscard(s string) (jetstream.DiscardPolicy, error) {
	switch s {
	case "", "old":
		return jetstream.DiscardOld, nil
	case "new":
		return jetstream.DiscardNew, nil
	}
	return 0, zip.ErrBadRequest("discard must be old or new")
}

func parseDeliver(s string) (jetstream.DeliverPolicy, error) {
	switch s {
	case "", "all":
		return jetstream.DeliverAllPolicy, nil
	case "last":
		return jetstream.DeliverLastPolicy, nil
	case "new":
		return jetstream.DeliverNewPolicy, nil
	case "lastPerSubject":
		return jetstream.DeliverLastPerSubjectPolicy, nil
	}
	return 0, zip.ErrBadRequest("deliver must be all, last, new or lastPerSubject")
}

func parseAck(s string) (jetstream.AckPolicy, error) {
	switch s {
	case "", "explicit":
		return jetstream.AckExplicitPolicy, nil
	case "none":
		return jetstream.AckNonePolicy, nil
	case "all":
		return jetstream.AckAllPolicy, nil
	}
	return 0, zip.ErrBadRequest("ack must be explicit, none or all")
}

func storageStr(v jetstream.StorageType) string {
	if v == jetstream.MemoryStorage {
		return "memory"
	}
	return "file"
}

func retentionStr(v jetstream.RetentionPolicy) string {
	switch v {
	case jetstream.InterestPolicy:
		return "interest"
	case jetstream.WorkQueuePolicy:
		return "workqueue"
	}
	return "limits"
}

func discardStr(v jetstream.DiscardPolicy) string {
	if v == jetstream.DiscardNew {
		return "new"
	}
	return "old"
}

func deliverStr(v jetstream.DeliverPolicy) string {
	switch v {
	case jetstream.DeliverLastPolicy:
		return "last"
	case jetstream.DeliverNewPolicy:
		return "new"
	case jetstream.DeliverLastPerSubjectPolicy:
		return "lastPerSubject"
	}
	return "all"
}

func ackStr(v jetstream.AckPolicy) string {
	switch v {
	case jetstream.AckNonePolicy:
		return "none"
	case jetstream.AckAllPolicy:
		return "all"
	}
	return "explicit"
}

// ----- inputs ---------------------------------------------------------------

// busPublish is one message to put on the bus.
type busPublish struct {
	// Subject is the subject to publish to, in the org's own namespace — e.g.
	// orders.created. No wildcards.
	Subject string `json:"subject"`
	// Data is the payload, carried verbatim as UTF-8 text (typically JSON).
	// Binary payloads belong on the NATS port.
	Data string `json:"data"`
	// Headers are optional message headers, one value per name. A Nats-Msg-Id
	// header is JetStream's deduplication key: a repeat within the stream's
	// dedup window is acknowledged as duplicate rather than stored twice.
	Headers map[string]string `json:"headers"`
}

// busRequest is one request awaiting one reply.
type busRequest struct {
	// Subject is the subject a responder listens on, in the org's namespace.
	Subject string `json:"subject"`
	// Data is the request payload, carried verbatim as UTF-8 text.
	Data string `json:"data"`
	// Headers are optional request headers, one value per name.
	Headers map[string]string `json:"headers"`
	// TimeoutMs bounds the wait for a reply. 0 or less means the default of
	// 5000; anything above 30000 is clamped to 30000.
	TimeoutMs int `json:"timeoutMs"`
}

// streamSpec is the writable configuration of a stream.
type streamSpec struct {
	// Subjects are the subjects this stream captures, in the org's namespace,
	// wildcards allowed — e.g. ["orders.>"]. 1 to 16 of them.
	Subjects []string `json:"subjects"`
	// Storage is the backend: file (durable, the default) or memory.
	Storage string `json:"storage"`
	// Retention is the discipline: limits (the default — every consumer gets
	// its own copy), interest, or workqueue (a message leaves when ANY consumer
	// takes it).
	Retention string `json:"retention"`
	// Discard says which end gives way at the limits: old (the default) drops
	// the oldest, new refuses the newest.
	Discard string `json:"discard"`
	// MaxMsgs caps retained messages. 0 or less means unlimited.
	MaxMsgs int64 `json:"maxMsgs"`
	// MaxBytes caps retained bytes. 0 or less means unlimited.
	MaxBytes int64 `json:"maxBytes"`
	// MaxAge caps message age in SECONDS. 0 or less means unlimited.
	MaxAge int64 `json:"maxAge"`
}

// consumerSpec is the writable configuration of a consumer.
type consumerSpec struct {
	// Filter narrows the consumer to one subject, wildcards allowed. Empty
	// means every subject the stream captures.
	Filter string `json:"filter"`
	// Deliver picks the starting point: all (the default), last, new, or
	// lastPerSubject.
	Deliver string `json:"deliver"`
	// Ack is the acknowledgement discipline: explicit (the default), none, or
	// all.
	Ack string `json:"ack"`
	// MaxDeliver caps delivery attempts per message. 0 or less means unlimited.
	MaxDeliver int `json:"maxDeliver"`
	// AckWait is how long a delivered message may stay unacknowledged before
	// redelivery, in SECONDS. 0 or less means the default of 30.
	AckWait int64 `json:"ackWait"`
}

// bucketWrite creates a KV bucket.
type bucketWrite struct {
	// Bucket is the bucket's name within the org, from the path: 1–64 of
	// [A-Za-z0-9_], no dash.
	Bucket string `json:"bucket"`
	// History is how many revisions each key keeps, 1–64. 0 means 1.
	History int `json:"history"`
	// TTL expires entries after this many SECONDS. 0 means no expiry.
	TTL int64 `json:"ttl"`
	// MaxValue caps one value's size in bytes. 0 or less means the server's
	// ceiling.
	MaxValue int `json:"maxValue"`
}

// bucketRef addresses ONE bucket of the org by name, from the path.
type bucketRef struct {
	// Bucket is the bucket's name, from the path.
	Bucket string `json:"bucket"`
}

// keyRef addresses ONE key of one org bucket.
type keyRef struct {
	// Bucket is the bucket, from the path.
	Bucket string `json:"bucket"`
	// Key is the key, from the path.
	Key string `json:"key"`
}

// kvWrite sets one key to one value.
type kvWrite struct {
	// Bucket is the bucket, from the path.
	Bucket string `json:"bucket"`
	// Key is the key, from the path.
	Key string `json:"key"`
	// Value is the value, carried verbatim as UTF-8 text (typically JSON).
	Value string `json:"value"`
}

// ----- outputs --------------------------------------------------------------

// busAck is the bus's receipt for one published message.
type busAck struct {
	// OK is true when the bus accepted the message.
	OK bool `json:"ok"`
	// Stream is the stream that stored the message — absent when no stream
	// captures the subject and the message went out core (fire-and-forget).
	Stream string `json:"stream,omitempty"`
	// Seq is the message's sequence in that stream.
	Seq uint64 `json:"seq,omitempty"`
	// Duplicate is true when JetStream deduplicated the message by its
	// Nats-Msg-Id instead of storing it again.
	Duplicate bool `json:"duplicate,omitempty"`
}

// busMessage is one message as the bus carries it.
type busMessage struct {
	// Subject is the message's subject in the org's own namespace.
	Subject string `json:"subject"`
	// Data is the payload as UTF-8 text.
	Data string `json:"data"`
	// Headers are the message's headers, when it carries any.
	Headers map[string][]string `json:"headers,omitempty"`
	// Seq is the message's stream sequence — fetched messages only.
	Seq uint64 `json:"seq,omitempty"`
	// Time is when the stream stored the message, RFC3339 — fetched messages
	// only.
	Time string `json:"time,omitempty"`
}

// bucketRecord is a KV bucket as the API publishes it.
type bucketRecord struct {
	// Bucket is the bucket's name within the org.
	Bucket string `json:"bucket"`
	// History is how many revisions each key keeps.
	History int `json:"history"`
	// TTL is the entry expiry in seconds; 0 means none.
	TTL int64 `json:"ttl"`
	// Values is how many values the bucket holds right now.
	Values uint64 `json:"values"`
}

// kvEntry is one key's value at one revision.
type kvEntry struct {
	// Key is the entry's key.
	Key string `json:"key"`
	// Value is the value as UTF-8 text; empty for delete and purge markers.
	Value string `json:"value"`
	// Revision is the entry's revision in the bucket.
	Revision uint64 `json:"revision"`
	// Created is when this revision was written, RFC3339.
	Created string `json:"created"`
	// Operation is what wrote the revision: put, del or purge.
	Operation string `json:"operation"`
}

// kvAck is the bucket's receipt for one write.
type kvAck struct {
	// Revision is the revision the write created.
	Revision uint64 `json:"revision"`
}

// kvPage is one key's history, oldest revision first.
type kvPage struct {
	// Data are the key's retained revisions.
	Data []kvEntry `json:"data"`
}

// ----- messaging ops --------------------------------------------------------

// Publish puts one message on the org's bus. When a stream captures the subject
// the write is DURABLE — the receipt names the stream and sequence only after
// JetStream has it on storage, and a repeated Nats-Msg-Id header within the
// dedup window answers duplicate instead of storing twice. When nothing
// captures it, the message goes out core NATS: delivered to current
// subscribers, receipt {ok}, nothing retained.
//
// Example: {"subject": "orders.created", "data": "{\"id\":\"o_1\"}",
// "headers": {"Nats-Msg-Id": "o_1"}}
func (o ops) publish(ctx context.Context, in *busPublish) (*busAck, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	subj, err := mapSubject(org, in.Subject, false)
	if err != nil {
		return nil, err
	}
	js, nc, err := bus()
	if err != nil {
		return nil, err
	}
	msg := &nats.Msg{Subject: subj, Data: []byte(in.Data), Header: header(in.Headers)}

	// One question decides the path: does a stream capture this subject? A
	// JetStream publish to an uncaptured subject would not fail fast — it would
	// wait out the ack timeout — so the door asks first and falls back core.
	if _, err := js.StreamNameBySubject(ctx, subj); err == nil {
		ack, perr := js.PublishMsg(ctx, msg)
		if perr != nil {
			return nil, busErr(perr)
		}
		return &busAck{OK: true, Stream: unphys(org, ack.Stream), Seq: ack.Sequence, Duplicate: ack.Duplicate}, nil
	} else if !errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil, busErr(err)
	}
	if err := nc.PublishMsg(msg); err != nil {
		return nil, busErr(err)
	}
	// A bounded flush, so the 200 means "the server has it", not "buffered in
	// this process".
	if err := nc.FlushTimeout(5 * time.Second); err != nil {
		return nil, busErr(err)
	}
	return &busAck{OK: true}, nil
}

// unphys strips the org's physical prefix off a stream name JetStream reports,
// so a receipt names the stream the way the caller does.
func unphys(org, name string) string { return strings.TrimPrefix(name, phys(org, "")) }

// header converts wire headers to the bus's header shape.
func header(h map[string]string) nats.Header {
	if len(h) == 0 {
		return nil
	}
	out := nats.Header{}
	for k, v := range h {
		out.Set(k, v)
	}
	return out
}

// Request sends one request on the org's bus and waits for one reply — the
// synchronous half of pub/sub, for callers speaking to a responder subscribed
// on the NATS port. 404 when nobody is listening on the subject; 408 when a
// responder exists but no reply arrived within the timeout.
//
// Example: {"subject": "billing.quote", "data": "{\"sku\":\"gpu_1\"}",
// "timeoutMs": 2000}
func (o ops) request(ctx context.Context, in *busRequest) (*busMessage, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	subj, err := mapSubject(org, in.Subject, false)
	if err != nil {
		return nil, err
	}
	_, nc, err := bus()
	if err != nil {
		return nil, err
	}
	wait := clamp(in.TimeoutMs, 1, 30_000, 5_000)
	rctx, cancel := context.WithTimeout(ctx, time.Duration(wait)*time.Millisecond)
	defer cancel()
	reply, err := nc.RequestMsgWithContext(rctx, &nats.Msg{Subject: subj, Data: []byte(in.Data), Header: header(in.Headers)})
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return nil, zip.ErrNotFound("no responder on subject")
	case errors.Is(err, context.DeadlineExceeded):
		return nil, zip.Errorf(http.StatusRequestTimeout, "no reply within %dms", wait)
	case err != nil:
		return nil, busErr(err)
	}
	return &busMessage{Subject: unmap(org, reply.Subject), Data: string(reply.Data), Headers: reply.Header}, nil
}

// clamp bounds a caller integer: non-positive means the default, and the
// ceiling is a ceiling.
func clamp(n, min, max, def int) int {
	if n <= 0 {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
func max64(n, floor int64) int64 {
	if n < floor {
		return floor
	}
	return n
}

// CreateBucket creates a KV bucket and returns it. A bucket is keyed state on
// the same durable plane as the streams: each key holds up to History
// revisions, entries can expire by TTL, and watchers on the NATS port see every
// write. 409 when the org already has a bucket of that name.
//
// Example: {"history": 5, "ttl": 3600}
func (o ops) createBucket(ctx context.Context, in *bucketWrite) (*bucketRecord, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	if !token(in.Bucket, false) {
		return nil, zip.ErrBadRequest("bucket must be 1-64 of letters, digits or _")
	}
	history := in.History
	if history <= 0 {
		history = 1
	}
	if history > 64 {
		return nil, zip.ErrBadRequest("history is capped at 64")
	}
	js, _, err := bus()
	if err != nil {
		return nil, err
	}
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:       phys(org, in.Bucket),
		History:      uint8(history),
		TTL:          time.Duration(max64(in.TTL, 0)) * time.Second,
		MaxValueSize: int32(clamp(in.MaxValue, 1, 1<<30, -1)),
	})
	if err != nil {
		return nil, busErr(err)
	}
	return bucket(ctx, in.Bucket, kv)
}

// bucket publishes a bucket in the org's own view, from its live status.
func bucket(ctx context.Context, name string, kv jetstream.KeyValue) (*bucketRecord, error) {
	status, err := kv.Status(ctx)
	if err != nil {
		return nil, busErr(err)
	}
	return &bucketRecord{
		Bucket:  name,
		History: int(status.History()),
		TTL:     int64(status.TTL() / time.Second),
		Values:  status.Values(),
	}, nil
}

// bucketOf resolves ONE org bucket by its caller-visible name.
func bucketOf(ctx context.Context, org, name string) (jetstream.KeyValue, error) {
	if !token(name, false) {
		return nil, zip.ErrNotFound("bucket not found")
	}
	js, _, err := bus()
	if err != nil {
		return nil, err
	}
	kv, err := js.KeyValue(ctx, phys(org, name))
	if err != nil {
		return nil, busErr(err)
	}
	return kv, nil
}

// DeleteBucket removes one bucket of the caller's org — every key and every
// revision with it — and answers 204 with no body. 404 when the org has no
// bucket of that name.
func (o ops) deleteBucket(ctx context.Context, in *bucketRef) (*noContent, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	if !token(in.Bucket, false) {
		return nil, zip.ErrNotFound("bucket not found")
	}
	js, _, err := bus()
	if err != nil {
		return nil, err
	}
	if err := js.DeleteKeyValue(ctx, phys(org, in.Bucket)); err != nil {
		return nil, busErr(err)
	}
	return nil, nil
}

// Get returns one key's current value and revision. 404 when the bucket does
// not exist, the key was never written, or its latest revision is a delete.
func (o ops) get(ctx context.Context, in *keyRef) (*kvEntry, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	e, err := kv.Get(ctx, in.Key)
	if err != nil {
		return nil, busErr(err)
	}
	out := entry(e)
	return &out, nil
}

// entry publishes one KV entry.
func entry(e jetstream.KeyValueEntry) kvEntry {
	op := "put"
	switch e.Operation() {
	case jetstream.KeyValueDelete:
		op = "del"
	case jetstream.KeyValuePurge:
		op = "purge"
	}
	return kvEntry{
		Key:       e.Key(),
		Value:     string(e.Value()),
		Revision:  e.Revision(),
		Created:   e.Created().UTC().Format(time.RFC3339),
		Operation: op,
	}
}

// Put sets one key to one value and returns the revision the write created.
// Writes are versioned: each put is a new revision and the bucket retains up to
// its History of them per key.
//
// Example: {"value": "{\"theme\":\"dark\"}"}
func (o ops) put(ctx context.Context, in *kvWrite) (*kvAck, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	rev, err := kv.Put(ctx, in.Key, []byte(in.Value))
	if err != nil {
		return nil, busErr(err)
	}
	return &kvAck{Revision: rev}, nil
}

// Delete removes one key — a delete marker in the key's history, so watchers
// see it and Get answers 404 — and answers 204 with no body. 404 when the
// bucket does not exist.
func (o ops) del(ctx context.Context, in *keyRef) (*noContent, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	if err := kv.Delete(ctx, in.Key); err != nil {
		return nil, busErr(err)
	}
	return nil, nil
}

// History returns one key's retained revisions, oldest first — every put and
// every delete marker up to the bucket's History depth. 404 when the bucket
// does not exist or the key was never written.
func (o ops) history(ctx context.Context, in *keyRef) (*kvPage, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	entries, err := kv.History(ctx, in.Key)
	if err != nil {
		return nil, busErr(err)
	}
	page := make([]kvEntry, 0, len(entries))
	for _, e := range entries {
		page = append(page, entry(e))
	}
	return &kvPage{Data: page}, nil
}
