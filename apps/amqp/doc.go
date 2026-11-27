// Package amqp is AMQP 0-9-1 on the platform bus: point a standard RabbitMQ
// client at :5672 and it works unchanged.
//
// The Hanzo AMQP gateway (github.com/hanzoai/amqp) speaks the AMQP 0-9-1 wire
// protocol and translates it to and from the JetStream apps/pubsub serves, so
// every client shares ONE bus — the same fold apps/kafka is for :9092 and
// apps/mq is for HTTP. It connects to that PubSub as a NATS client on loopback
// :4222, so there is no second broker to run, keep up, or lose a message
// between. An exchange and a routing key become a subject, a queue becomes a
// durable JetStream consumer, and basic.ack becomes an Ack; see the gateway's
// route.go for the whole map.
//
// It is LAZY, exactly as apps/kafka is. Under the light host the host owns :5672
// (manifest App.Wire): the first AMQP connection starts this app, and the host
// relays every connection to the unix socket this app listens on, its zip socket
// + manifest.WireSuffix. So a pod no AMQP client talks to runs no gateway, and a
// start that fails is tried again by the next connection rather than leaving the
// gateway absent until the pod restarts. Run standalone, it binds the TCP port
// itself.
//
// It mounts NO HTTP routes of its own, so cloud's generic per-subsystem liveness
// route answers /v1/amqp/health with an unconditional ok. Readiness is
// structural instead: Use fails CLOSED when it cannot reach the bus or open its
// listener, so the start fails and the host closes the connection that asked for
// it. A gateway that dies AFTER its start is reported through readiness.Degraded (the
// binary's /v1/health `degraded` field, which the release smoke refuses an image
// on). The :5672 listener is an in-pod socket: the K8s Service exposes only the
// HTTP/metrics/ZAP ports, so reaching the gateway beyond the pod is a Service
// (and NetworkPolicy) decision this app does not make.
//
// It dials the bus through bus.URL — the ONE knob every app in this process reads
// — so it cannot end up bridging a different bus than the one analytics publishes
// and webhooks consumes, and there is never a silent half-embed.
package amqp
