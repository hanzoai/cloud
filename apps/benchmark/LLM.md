# LLM.md — apps/benchmark

Guidance for apps/benchmark. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The benchmark arena (`apps/benchmark`, `/v1/benchmark`)

Two planes, never blended: **claims** (`claims.go`, latest row per (org, benchmark, model,
source)) and **attempts** (`attempts.go`, per-item rows per model). Both, and each org's stored
router blends (`presets.go`), are one store (`store.go`): tables `benchmark_claims`,
`benchmark_attempts` and `benchmark_presets`, append-only (a restatement is a new row), each row
its JSON document beside the columns a query needs, in the one dialect, opened through
`locus.Use` (`manifest.SQLStores["benchmark"]`), so it is a schema on hanzo-sql and moves
there from its file like the rest. The leaderboard's `published` reads only the curator's (org
`admin`) public claims; `measured`, `history` and `compare` read attempts.

- **The write lock is the store's transaction.** The daily-limit count, the once-per-run check
  and the rows they admit are one `Dialect.BeginWrite` transaction (`Store.write`), so replicas
  cannot both fit under one allowance or both import one run. The work under the lock asks the
  store only through the `book` it is handed (the transaction): a pool of four whose writers each
  hold a connection while they wait cannot also serve the holder a second. The audit trail is
  another store and is asked as before. A claims write is all-or-nothing: the intent names every
  row, and if the store fails the outcome says every row failed. A read the store cannot answer is
  503, never an empty arena.
- **The JSONL files are read once** (`legacy.go`): `{DataDir}/benchmark/attempts/*.jsonl`,
  `claims/*.jsonl` and `presets.jsonl` are carried on the first open, each plane in one
  transaction under the write lock with a row in `benchmark_legacy` beside it, so a second
  replica carries nothing and a pod with no volume finds no files. An error fails the mount. The
  files are left where they are.
- **Publication is a SuperAdmin act.** Any member files claims under its own org (private by
  default, `visibility: public` to show them at `/claims`); none reaches the board on its own.
  A SuperAdmin acting in org `admin` (no `X-Org-Id` switch) publishes by writing under `admin` or
  by `POST /v1/benchmark/claims/promote {"org": "hanzo"}`, which copies the org's public claims
  as filed and keeps `from` on the row and on the board. A SuperAdmin switched into an org
  (`X-Org-Id: hanzo`) writes that org's claims and cannot promote.
- **The catalog is compiled** (`catalog` in `benchmark.go`): the public 14, the coding 4
  (`deepswe`, `terminal_bench_4_0`, `swe_atlas_qna`, `swe_bench_pro_v2`: claims only, none in
  enso-bench's `registry/benchmarks.yaml`, each distinct from its v1), then the Decision
  Suite (`jev.*`, `app.*`, `typed_decisions`, `massive`, `cap.cardinality`, `decision.stability`,
  `jailbreak.wild`, `decision.isolation`). Every row has `direction` (`higher`/`lower`); the board
  orders by it and `selectClaim` picks the better score in it. Every score is a percentage:
  latency (`decision.latency`) stays in research runs. `cap.cardinality` names its size in the
  model (`kai-1@k150`), as the Enso filings name pool size (`pool-oracle@K10`).
- **`POST /v1/benchmark/runs` spends nothing.** The arena has no executor: per benchmark it
  answers the model's latest run from the attempts held, or says nothing was queued and names who
  measures it (`hanzoai/enso-bench` for a native row, the attempts import for the rest). No
  `force`, `endpoint` or `attempts`: nothing here would act on them.
- **A research run's `key`** (`keyOf`, record.go) is the sha256 of benchmark, split, system,
  version, k, temperature, max_tokens, embedder, reader and the four digests, NUL-terminated;
  derived on read like completion, never stored. `GET /v1/research/runs?key=&completion=complete`
  is the cache read before a paid run; hanzo-research `key()`/`measure()` hold the same vectors
  (`TestKeyVector`).
- **Attempts have one way in**, `POST /v1/benchmark/attempts` (SuperAdmin in `admin`): a run's
  per-item rows FOR a published claim whose source is
  `…/v1/research/runs?…&id=<run>`, scoring that claim within 0.05 points, once per run, higher-is-
  better benchmarks only. Research's own Attempt plane has no read route, so the rows come from
  the harness's per-item file.
- **Presets** (`presets.go`) persist per org and name in `{DataDir}/benchmark/presets.jsonl`;
  `served_as` is `enso-<name>` and a bare name is not expressible (no shadowing a model id; Hanzo's
  own tiers auto/flash/pro/ultra/free/embed are refused). Nothing serves a stored preset yet.
- Production: one replica, PVC `cloud-data-local` at `/var/lib/cloud`.
