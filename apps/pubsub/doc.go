// Package pubsub is your message bus: publish, subscribe, and durable streams
// your apps read at their own pace.
//
// It is the platform message bus: publish/subscribe messaging and durable
// JetStream streams, served to tenants at /v1/pubsub over the embedded Hanzo
// PubSub (NATS + JetStream) node this same package runs.
//
// The node binds the NATS client port (default :4222) and serves JetStream
// over the cloud data dir — the ONE durable log every other app publishes
// facts onto and consumes them from. The Kafka-wire adaptor (apps/kafka) and
// any in-cluster NATS/Kafka client talk to it. It is a single embedded node
// running JetStream over the local file store — there is NO ZooKeeper, raft,
// or etcd in the path (Lux consensus only; the optional Quasar PQ control
// plane is a follow-up, see github.com/hanzoai/pubsub/embed).
//
// ONE bus, MANY endpoints. The NATS port is the cluster's listener: in-process
// apps and in-cluster clients, unscoped. /v1/pubsub is this app's tenant
// endpoint: publish and request/reply (typed.go), each org confined to its own
// namespace by the validated principal, never by anything a caller asserts.
// apps/kv is a second tenant endpoint on the SAME plane — a bucket is not a
// message, so it answers under its own name — and it rides the four calls
// under "riding the plane" below rather than opening a bus of its own. Cloud's
// generic per-subsystem liveness route answers /v1/pubsub/health, and the K8s
// Service TCP-probes :4222 directly.
//
// It ALWAYS serves. The staged cutover it was gated behind is over — the
// standalone nats StatefulSet and the pubsub App are retired, so this is the ONE
// in-cluster messaging plane and a cloud that did not serve it would simply have
// no messaging. WHERE it listens stays configurable (CLOUD_PUBSUB_PORT,
// CLOUD_PUBSUB_HOST); a port collision is answered by moving the port, never by
// running without the plane.
//
// Open fails CLOSED: a bind/start error aborts boot rather than serving a phantom
// messaging plane.
//
// # THE BUS KNOB
//
// URL is where every app in this process reaches that plane, and it is the ONE
// knob for all of them — analytics publishing the event plane, webhooks consuming
// it, the Kafka facade bridging it. It defaults to the LOOPBACK address of the
// server this same binary just bound, so the default needs no configuration and
// cannot disagree with the server: both read CLOUD_PUBSUB_PORT.
//
//	CLOUD_PUBSUB_URL   full dial URL; set ONLY to point this process at a bus
//	                   other than its own embedded one.
//	CLOUD_PUBSUB_PORT  the port the embedded server binds AND the port the
//	                   default URL dials. Default 4222.
//	CLOUD_PUBSUB_HOST  the bind address of the embedded server (default
//	                   0.0.0.0). It is NOT a dial address: URL always dials
//	                   127.0.0.1, because 0.0.0.0 names every interface to a
//	                   listener and none to a client.
//
// There is deliberately no per-app URL variable and no "off". Mount fails boot
// closed, so a cloud that is up HAS a bus; an app that made its bus optional
// would silently ship with its half of the platform disconnected.
package pubsub
