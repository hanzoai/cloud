// Package mq is queue and stream admin for your org: create them, watch them
// drain, ack what you pulled.
//
// It is Hanzo MQ, the managed message-queue product: org-scoped administration of
// durable JetStream queues on the platform message plane — streams, their
// messages, pull consumers and delivery — served at /v1/mq over the broker
// apps/pubsub embeds.
//
// # THE SPLIT — mq vs pubsub
//
// One broker, two ORTHOGONAL surfaces. pubsub is the messaging DATA plane
// (publish, subscribe, request/reply — the subject side). mq is the queue and
// stream ADMIN plane plus pull delivery (create/inspect/purge/delete streams,
// manage consumers, pull the next batch). No operation appears on both: the
// authored MQ spec's publish/subscribe/request/subjects operations are
// deliberately NOT served here — see typed_wire_test.go, where every refused
// operation of the authored spec (openapi d86248f^:mq/openapi.yaml) is pinned
// with its reason.
//
// # TENANCY
//
// The broker is the ONE in-cluster plane; it also carries platform-internal
// streams (the analytics event plane, the Kafka facade's topics). Isolation is
// therefore enforced HERE, from the validated principal and never from a
// request field:
//
//   - stream NAMES are namespaced per org on the wire ("MQ_<org>_<name>") and
//     presented bare; a caller can name only streams inside its own namespace.
//   - stream SUBJECTS are confined to the org's subject space "mq.<org>.>":
//     callers state subjects RELATIVE to it ("orders.*"), the prefix is added
//     on the way in and stripped on the way out. Two orgs can never bind
//     overlapping subjects, and no tenant stream can capture a platform
//     subject (event.>, commerce.>, …).
//
// # CONNECTION
//
// Mount dials bus.URL() — the ONE bus knob every app in this process reads —
// with unlimited reconnect, so this app mounts (and can describe itself) with
// no broker running; every op answers 503 until the plane is reachable and the
// health op reports degraded rather than lying. Shutdown drains the client.
package mq
