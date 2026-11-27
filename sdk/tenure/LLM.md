# LLM.md — internal/org

Guidance for sdk/tenure. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Zero-downtime HA for per-org stores (rolling-upgrade safe)

The per-org store path (`cloud.OrgStore` + `internal/org`) is HA over embedded
SQLite: `ha` decides WHO writes (HRW election + a monotone fencing round), `vfs`
FencedStore decides HOW state ships (hydrate-on-open + fenced ship to S3), and this
layer decides WHEN ownership transitions. Three orthogonal lanes; SQLite stays
embedded underneath.

- **Durability is THE path, capability-detected — no flag.** `buildDurability`
  probes at boot: no object store reachable (dev / native-Go) → local-only, same
  code path; a reachable store → `org.ProbeCAS` PROVES its conditional-PUT
  atomicity (two racing If-None-Match creates + If-Match updates, exactly one winner
  each) before fencing any tenant data. A store that can't be proven atomic fails
  SAFE to local-only + a loud alert (never fence where two writers could win one
  round). Replaced the old `CLOUD_RESEARCH_DURABLE` opt-in — the atomicity gate (H2)
  is now a self-check the binary runs.
- **Live membership (no static peer list).** `membership_k8s.go` lists Ready,
  non-terminating pods by label (`CLOUD_PEER_SELECTOR`) via the K8s API each 2s
  refresh, so a rolling upgrade's changing pod set is tracked and a draining/dead pod
  (`DeletionTimestamp` set, or NotReady) leaves the writer election at once. Out of
  cluster / no selector → static self set (`podWriterEligible` is the ONE ready gate;
  visor has the twin, the shared `hanzoai/ha/k8s` source folds them).
- **M3 live re-acquire, no restart.** A store that opened degraded (read-only) is
  promoted IN PLACE when this replica becomes the org's elected owner:
  `Durable.PendingPromotion` gates it, `TryClaim` probes the lease (CAS only, no file
  I/O), then `OrgStore.promote` quiesces the read-only handle and reopens as owner
  (Hydrate renews + CarryForward-restores under the FRESH handle — the file swap is
  why the reopen is required). The reopen claims a strictly higher round, fencing the
  prior owner — never two live writers.
- **Graceful drain.** SIGTERM → `SetDraining()` → `/readyz` 503 (drain-aware, ops
  port) → K8s marks NotReady → peers re-elect this pod's orgs to live successors
  (which hydrate via M3) → the pod stays serving a short grace, then in-flight drains
  and final state ships (`OrgStore.CloseAll`, ship-before-close). The shard router
  routes on the live set when the durable plane is on, so a draining pod's orgs go to
  the ready successor — not to the gone pod. Manifest: readiness → `/readyz` on the
  metrics port, `terminationGracePeriodSeconds` ≥ ~40s, RBAC pods:list,watch, the
  downward-API `POD_NAME`/`POD_NAMESPACE`, `CLOUD_PEER_SELECTOR` (all in the universe chart).
- **Proof.** `sdk/tenure/rollingupgrade_test.go` rolls 3 pods over 8 orgs with
  continuous writes and asserts zero lost acked writes, zero split-brain (no
  (org,round) acked by two pods), and continuous availability — across both a pod
  restart (fresh rehydrate) and an in-place ownership flap (M3, no restart).
- **Hydrate reconciles; it never discards the local file.** The durable object is
  a copy of a local file as of its last ship, and the local file plus its -wal (which
  SQLite replays on open) can hold commits the object does not. `Durable.reconcile`
  keeps the local file whenever it descends from the object, restores the object only
  where no local file exists or another writer shipped since the file last reconciled,
  and then renames the local file to `<db>.aside-<unixnano>{,-wal,-shm}` (logged at
  ERROR) — never removes it. What the file last reconciled with lives beside it in
  `<db>.durable` (payload digest, a pending digest written before each put, and the
  file's shape). Subsystem processes are stopped without notice, so the close-time
  ship is not counted on: `OrgStore.shipLoop` ships every owned store whose file
  changed, each `StoreShipEvery` (1m). Proofs: `sdk/tenure/reconcile_test.go`
  (pure-Go) and `orgdb_restart_test.go` (needs the SQLCipher codec; skips without it).
- **Two extensibility clients (for the tiered-storage perf pass).** The fence's
  `ConditionalStore` is constructed in `buildDurability`, so a KV read/write-through
  cache (L1 over the S3 L2) wraps it as a one-line decorator. The ship mechanism is a
  swappable `snapshotCodec` (default `wholeFile`), so WAL-frame delta shipping
  replaces it without touching the fence/round. `WithSeal` injects the crypto client
  (`sqlitedrv.Checkpoint`): on a defer-encryption-to-checkpoint backend the ship MUST
  route through the driver's re-encrypting Checkpoint so ship-before-ack reads FRESH
  ciphertext; on the write-time backends it is a successful no-op.
- **A ship does not cost the store its connection, and the file does not move under
  the copy.** `wholeFile.produce` folds the WAL on the store's sole connection and,
  STILL HOLDING IT, opens a read transaction on a second read-only handle
  (`WithReader` → `orgReader` → `cek.OpenAt`), then gives the connection back. In WAL
  mode only a checkpoint writes the main database file, and a reader that begins while
  the WAL is empty holds the lock a checkpointer needs exclusively — so the copy reads
  exactly what the fold left, while every statement on that org is served. The order is
  the whole argument: a frame appended between the fold and the BEGIN puts the reader
  on a different read mark and the file is free to move again, which is why the
  connection is held across both statements. Pinned by `sdk/tenure/pin_test.go`,
  whose controls must FAIL to hold. The second handle opens only where
  `sqlitedrv.CodecLinked()` — the envelope backend keeps one plaintext copy per handle
  and last close wins, so it gets none. Every binary that opens an org file is a
  `plugin/<name>` built CGO=1 against libsqlcipher; `cmd/cloud` reaches no org DB.
