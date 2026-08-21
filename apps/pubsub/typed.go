package pubsub

// typed.go is the pubsub PRODUCT: the /v1/pubsub door onto the same embedded
// bus every in-process app rides. Two typed ops — publish and request/reply —
// each one registry entry projecting the REST route, the OpenAPI schema and
// prose, the MCP tool, the CLI command and every generated SDK method.
//
// BOTH ops drive the real plane. There is no store of its own and no second
// bus: each handler takes the ONE connection [Bus] holds and relays JetStream's
// own answer. A JetStream refusal surfaces as the 4xx it is, never as a
// reshaped success.
//
// TENANCY IS A NAMESPACE, NOT AN ACCOUNT. The embedded node runs one JetStream
// domain shared with the platform's own planes (the event stream, o11y, the
// Kafka facade), so the door scopes tenants by construction rather than by
// NATS accounts: org subjects live under "pub.<org>." — mapped on the way in,
// stripped on the way out, so a caller sees its own clean namespace and can
// never name another org's subjects or the platform's (event.>, $KV.>). The
// name rule for streams and buckets is [Qualify], next door.
//
// The NATS client port (:4222) is the CLUSTER'S plane — in-process apps and
// in-cluster clients, unscoped, exactly as before. This door is a TENANT'S
// plane, and only the tenant doors do tenancy.
//
// NOT SERVED, by decision rather than omission — the intent spec
// (hanzoai/openapi d86248f^:pubsub/openapi.yaml) authored 29 operations; the
// twelve below are refused and TestRefusedPubsubOpsStayRefused pins each
// address to a route-level 404:
//
//   - GET /v1/pubsub/subscribe (SSE): a stream is not a typed op — zip's typed
//     path writes ONE JSON Out and has no vocabulary for text/event-stream
//     (same measured fact that keeps POST /v1/ask untyped, apps/ask/ask.go).
//     Consumption is served by the NATS port, which speaks native
//     subscriptions, and by apps/mq's pull ops over the same streams.
//   - /v1/pubsub/objects/* (4 ops): cloud's object door is /v1/storage. A
//     second object store riding stream chunks would be two doors to one noun.
//   - /v1/pubsub/{varz,connz,jsz,routez,gatewayz,leafz,subsz} (6 ops + subsz):
//     operator telemetry, server-wide and cross-tenant by definition — connz
//     lists every client of every tenant. The operator plane is apps/o11y;
//     publishing it on a tenant surface would be a leak, not a feature.
//
// Payloads are TEXT. `data` is a JSON string carried verbatim as UTF-8 bytes —
// the JSON door is a text door, and its own round trip is exact. Binary
// payloads belong on the NATS port; bytes published there that are not UTF-8
// read back lossily here.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
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

// routes registers the tenant door. Registration order is match order, but both
// paths here begin with a distinct static segment, so nothing can shadow
// anything.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	// A typed op receives only a context, so the validated org reaches it by
	// being parked there — never as an In field. cloud.Bridge parks it, and the
	// composer owns that install: the fused host at its root, a plugin program
	// in its constructor.
	g := app.Group("/v1/pubsub")

	zip.Post(g, "/publish", o.publish)
	zip.Post(g, "/request", o.request)
}

// ----- tenancy --------------------------------------------------------------

// root is the org's subject namespace: every subject this door touches lives
// under it, so no caller can name another org's subjects or the platform's.
func root(org string) string { return "pub." + org + "." }

// mapSubject validates a caller subject and roots it in the org's namespace.
// Wildcards are refused outright: both ops here address ONE subject — a publish
// target and a request target — and '*' or '>' names a set, which neither can
// mean. (Where a wildcard IS the point, a consumer's filter, the op lives in
// apps/mq over the same streams.)
func mapSubject(org, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", zip.ErrBadRequest("subject is required")
	}
	if len(s) > 256 {
		return "", zip.ErrBadRequest("subject too long (max 256)")
	}
	for _, t := range strings.Split(s, ".") {
		if t == "" {
			return "", zip.ErrBadRequest("subject has an empty token")
		}
		for j := 0; j < len(t); j++ {
			if c := t[j]; c <= ' ' || c > '~' || c == '*' || c == '>' {
				return "", zip.ErrBadRequest("subject has an invalid character")
			}
		}
	}
	return root(org) + s, nil
}

// unmap returns the caller's view of a plane subject: the org root stripped. A
// subject outside the root (an inbox reply, say) passes through unchanged.
func unmap(org, s string) string { return strings.TrimPrefix(s, root(org)) }

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
	org, err := Org(ctx)
	if err != nil {
		return nil, err
	}
	subj, err := mapSubject(org, in.Subject)
	if err != nil {
		return nil, err
	}
	js, nc, err := Bus()
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
			return nil, Err(perr)
		}
		return &busAck{OK: true, Stream: unphys(org, ack.Stream), Seq: ack.Sequence, Duplicate: ack.Duplicate}, nil
	} else if !errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil, Err(err)
	}
	if err := nc.PublishMsg(msg); err != nil {
		return nil, Err(err)
	}
	// A bounded flush, so the 200 means "the server has it", not "buffered in
	// this process".
	if err := nc.FlushTimeout(5 * time.Second); err != nil {
		return nil, Err(err)
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
	org, err := Org(ctx)
	if err != nil {
		return nil, err
	}
	subj, err := mapSubject(org, in.Subject)
	if err != nil {
		return nil, err
	}
	_, nc, err := Bus()
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
		return nil, Err(err)
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
