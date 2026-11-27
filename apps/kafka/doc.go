// Package kafka is Kafka on the platform bus: point a standard producer or
// consumer at :9092 and it works unchanged.
//
// The Hanzo Kafka adaptor (github.com/hanzoai/kafka) speaks the Kafka binary
// protocol and translates it to and from the JetStream apps/pubsub serves, so
// every client shares ONE bus. It connects to that PubSub as a NATS client on
// loopback :4222 — replacing the standalone `insights-kafka` Deployment. No
// ZooKeeper: the adaptor is stateless over JetStream (Lux consensus only).
//
// It is LAZY. Under the light host the host owns :9092 (manifest App.Wire): the
// first Kafka connection starts this app, and the host relays every connection to
// the unix socket this app listens on, its zip socket + manifest.WireSuffix. So a
// pod no Kafka client talks to runs no broker, and one the datastore and insights
// stay connected to keeps it running. Run standalone, it binds the TCP port
// itself.
//
// It mounts NO HTTP routes of its own, so cloud's generic per-subsystem liveness
// route answers /v1/kafka/health with an unconditional ok. Readiness is
// structural instead: Use fails CLOSED when it cannot reach the bus or open its
// listener, so the start fails and the host closes the connection that asked for
// it; the next connection tries again. A broker that dies AFTER its start is
// reported through readiness.Degraded (the binary's /v1/health `degraded` field, which
// the release smoke refuses an image on).
//
// It dials the bus through bus.URL — the ONE knob every app in this process reads
// — so it cannot end up bridging a different bus than the one analytics publishes
// and webhooks consumes, and there is never a silent half-embed.
//
// Every topic partition is a stream on that bus, bounded by age and size:
// CLOUD_KAFKA_RETENTION (a Go duration) and CLOUD_KAFKA_RETENTION_BYTES (per
// partition). Unset takes the adaptor's default, 7 days and 256 MiB; a
// negative value lifts the limit on that axis. At start the adaptor bounds the
// partitions it finds with no limit at all, so topics made before retention
// existed stop growing too. A value that does not parse fails Use rather
// than quietly taking the default.
package kafka
