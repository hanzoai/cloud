# apps/mq — Hanzo MQ

Org-scoped administration of durable JetStream queues on the platform message
plane: streams, direct message access, pull consumers, health, info — served
at /v1/mq over the broker apps/pubsub embeds, dialled through `pubsub.URL()`
(the ONE bus knob). Every route is a TYPED op; the package doc's first
sentence is the product description everywhere.

## The split — mq vs pubsub (one broker, two orthogonal products)

- **pubsub** = the subject side: publish, subscribe, request/reply,
  subject introspection.
- **mq** = the queue side: stream admin, stored-message access, pull
  consumers, delivery.

No operation appears on both surfaces. The authored spec's
publish/subscribe/request/subjects ops are refused here for exactly that
reason (see the ledger below).

## Spec of intent → the ledger

The authored document (`hanzoai/openapi` @ `d86248f^:mq/openapi.yaml`,
deleted as unserved in d86248f) states **41 operations**. This surface serves
the **15** the broker genuinely answers for a tenant; the other **26** are
refused with pinned reasons. Both directions are enforced by
`typed_wire_test.go` (`served` / `refused` ledgers + the 41-op coverage
check):

- **served (15)**: streams CRUD + purge + messages (8), consumers CRUD +
  next (5), health + info (2).
- **refused (26)**: publish/subscribe/request/subjects (5 → pubsub's
  surface), kv (10 → /v1/kv & /v1/datastore own keyed storage), objects
  (8 → /v1/s3 owns object storage), accounts (3 → broker monitor data the
  embedded plane does not expose to a client; no real backend today).

A refusal row is deleted the day its fact stops being true, and the test
fails if a refused op starts being served (or vice versa).

## Tenancy — enforced here, from the validated principal

The broker is shared (it also carries the analytics event plane and the
Kafka facade's topics), so isolation is cloud's job:

- stream names: `MQ_<tok(org)>_<name>` on the wire, bare to the caller.
- subjects: confined to `mq.<tok(org)>.>`; callers state org-relative
  subjects, the prefix is added inbound and stripped outbound. No tenant
  can bind another tenant's or a platform subject (event.>, commerce.>).
- `tok` is an injective encoding into the broker's name alphabet
  (`_`-hex escape), so distinct orgs can never share a namespace
  (TestNamespaceEncodingIsInjective).
- the org comes from `principal.Org` via `cloud.Bridge` — never an In field.

## Wire semantics worth knowing

- `next` (pull) ACKS ON DELIVERY: the authored spec has no ack endpoint, so
  not acking would redelivery-loop every explicit consumer forever. The op
  doc states it; TestConsumerPullAcksOnDelivery pins it.
- an empty waiting pull answers 408 (the authored contract); `no_wait`
  answers 200 with an empty page.
- purge reports before−after because the Go client discards the API's own
  purged count.
- Mount NEVER needs a live broker (retry-connect), so `describe` and route
  projection work anywhere; ops answer 503 and health says "degraded" until
  the plane is reachable — fail-honest, not fail-fake.

## Tests

Real backend only: every functional test opens an embedded
`github.com/hanzoai/pubsub/embed` node (random port, temp store) and drives
the HTTP surface over it — stream lifecycle, pull+ack, tenancy (a second org
sees nothing; platform streams invisible), degraded honesty. Run:

    make -C apps/mq test
