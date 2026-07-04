# sqlite-ha-proto — on-stack SQLite zero-downtime HA proof

Proves the mechanism behind `cloud`'s zero-downtime deploy design (see the root
`LLM.md` § "SQLite zero-downtime HA") using ONLY first-party packages:

- `github.com/hanzoai/sqlite` — the ONE driver (`database/sql` name `sqlite`).
- `github.com/hanzoai/replicate` — WAL→LTX streaming to an object store.
- `github.com/hanzoai/replicate/file.Leaser` — single-primary election
  (filesystem CAS; the dev analog of `replicate/s3.Leaser`, which runs against the
  in-cluster SeaweedFS `s3` service in prod).

NO LiteFS, NO Litestream, NO Postgres. Two "pods" (primary + warm standby) share
ONLY the object store (a `file://` dir here; SeaweedFS in prod). It models an
orderly rolling deploy: standby catches up → old primary drains (final sync +
lease release) → standby promotes. A tight-loop read probe runs across the whole
handoff.

## Run

```
GOWORK=off CGO_ENABLED=0 go run .
```

(`CGO_ENABLED=0` selects the modernc/nocgo backend — the same build mode cloud
ships. This module is deliberately OUTSIDE cloud's module graph.)

## Proof (captured)

```
== Hanzo-native SQLite HA prototype (sqlite + replicate) ==
[A] acquired primary lease gen=1 owner=ra:17458
[B] warm standby caught up (readiness OK) — entering read rotation
== ROLL: draining primary A, promoting standby B ==
[A] drained. final committed rows on A = 30 (lease released)
[B] acquired primary lease gen=1 (handoff)
[B] rows present at promotion = 30 (expected 30 from A)

==================== PROOF ====================
rows written by A (pre-roll) : 30
rows written by B (post-roll): 30
final row count on served DB : 60 (expected 60)
max id / distinct ids        : 60 / 60 (contiguous=true)
integrity_check              : "ok" (ok=true)
read probes total            : 123
read probe FAILURES          : 0
==============================================
RESULT: PASS — zero read-downtime across an orderly primary handoff, zero data loss, no corruption.
```

Assertions enforced by the harness (non-zero exit on any failure): probe failures
== 0, final row count == A+B, ids contiguous (no gap/dup), `integrity_check == ok`,
and rows-at-promotion == A's committed count (zero data loss on handoff).
