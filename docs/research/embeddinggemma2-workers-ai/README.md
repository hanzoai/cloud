# EmbeddingGemma 2: Hanzo Engine vs Cloudflare Workers AI (#807)

Status: both halves are measured, on the same documents, queries, widths and prefix policies. Every number
below is in `results/` (`hanzo.json`, `cloudflare.json`, `compare.json`, `report.md`).

## Results: Hanzo Engine

Retrieval, nDCG@10 (100 queries, 300 documents per set; exact search; each cell its own index):

| set | policy | 768 | 512 | 256 | 128 |
|---|---|---|---|---|---|
| text (SciFact) | task | 0.962 | 0.969 | 0.952 | 0.947 |
| text (SciFact) | none | 0.961 | 0.963 | 0.955 | 0.926 |
| code (CodeSearchNet python) | task | 0.960 | 0.958 | 0.962 | 0.943 |
| code (CodeSearchNet python) | none | 0.961 | 0.957 | 0.952 | 0.943 |
| images (Flickr30k) | either | not supported | | | |

- **Width**: 768, 512 and 256 are within the noise of 100 queries (about 0.02); 128 costs about 0.015 to 0.035
  nDCG@10. Recall@100 is 0.99 to 1.0 at every width.
- **Prefix**: at 768 the model card's `task` prompts and raw text retrieve alike here; at 128 on text the
  prompts are worth 0.021. The vectors are not alike: the same document under `task` and under `none` has a
  cosine of 0.895 on average (0.843 at worst) for text and 0.952 for code. Two policies are two spaces.
- **`dimensions`**: asking the engine for 512, 256 or 128 returns exactly the leading values of its 768 vector
  normalized again (cosine 1.0000000 on every probe, norm 1.000000), for both policies.
- **Images**: the engine loads no vision tower, and `input: {image_url}` answers HTTP 200 with a 768-wide
  vector of zeros and `prompt_tokens: 1`. An indexer would store it. The harness treats an all-zero vector as a
  failure; the engine should refuse the request instead (follow-up for hanzoai/engine).
- **Tokens** (the server's count): 323 per SciFact document under `task` (318 under `none`), 240 per function,
  26 per claim, 92 per docstring query.
- **Latency and throughput were measured on a shared, oversubscribed 8-vCPU x86-64 host** (Xeon, CPU only;
  the engine got about one core): 56 to 73 tokens/s, one-query round trip p50 0.85 s, p99 1.4 s (text). They
  say nothing about the engine; the cost model uses production's own measurement instead, 385 tokens/s per
  replica on four cores (universe `values/hanzo/engine.yaml`, engine 1.7.104).

## Results: Workers AI, side by side

`@cf/google/embeddinggemma-2` on the same 100 queries and 300 documents per set (2026-11-21, batch 32,
concurrency 4, from one host over the internet). Neither side adds a prefix; `compare` refuses runs whose input
strings differ.

- **Quality is the same.** nDCG@10 differs by at most 0.004 on text and 0.001 on code, at every width and under
  both policies; recall@100 is the same except one text cell at 128 (0.99 engine, 0.98 Workers AI).
- **The vectors are the same.** Cosine between the two sides' 768 vectors for one input is 0.99997 or more on
  average and 0.99987 at worst; top-10 overlap is 0.981 to 0.998 at every width.
- **`dimensions` is close, not exact.** Workers AI's 512, 256 and 128 vectors agree with its own 768 vector at
  cosine 0.99996 at worst (text) and 0.99997 (code); the engine's agree at 1.0000000.
- **Images** (engine: not supported). Workers AI, nDCG@10 under `none`: 0.934 at 768, 0.935 at 512, 0.909 at 256,
  0.791 at 128; recall@10 is 0.99 down to 512 and 0.91 at 128. Under `task` it is 0.914, 0.923, 0.898, 0.764.
- **Latency and throughput** (Workers AI, this host): one-query round trip p50 0.56 s and p99 3.2 s on text,
  p50 0.49 s and p99 0.71 s on code; 86 documents/s (27,900 tokens/s) on text, 110 documents/s on code, 12
  images/s with 57 MB of base64 sent for 300 images. The engine's figures above were taken on a host that gave it
  about one core and say nothing about it.
- **Tokens**: the same 97,008 document tokens on text and 72,054 on code on both sides; an image is 264.5 tokens.

So hosted inference is enough where the table in "Where the owned runtime is justified" says so, and the owned
runtime is justified by residency, offline use and a pinned revision, not by quality: the two agree.

## Full cost

For 1,000,000 documents and 1,000,000 queries a month (`compare --docs --queries-month` scales it); tokens are
the measured counts above, prices are list prices with their sources in `results/report.md`.

**Inference.** Workers AI is $0.0017 per million tokens. The owned runtime on CPU costs $0.167 per million
(an m7a.xlarge-hour, $0.2315, buys 1.39 M tokens at 385 tokens/s), about 98 times more per token, before the
floor: two replicas at 2 CPUs requested each, about $169 a month whatever the load.

| | Workers AI | owned CPU |
|---|---|---|
| re-embed 1 M SciFact-size docs (323 M tokens) | $0.55, and 5 days of the 10,000-neuron daily free allocation would cover it | $54, 233 replica-hours (about 5 days on production's two replicas) |
| re-embed 1 M functions (240 M tokens) | $0.41 | $40, 173 replica-hours |
| 1 M text queries a month (26 M tokens) | $0.04 | $4.29 marginal |
| 1 M images (about 260 tokens each, Workers AI's figure) | $0.44 of inference **plus $17.11 to move 190 GB of base64 out of S3** | not possible today |

**Media transfer dominates multimodal.** Workers AI takes media only as base64 data URIs, so each image
(142.6 KB on average in the 300 measured, 190 KB as a data URI) leaves storage and crosses the internet on every (re)index:
from S3 that egress is 39 times the inference bill. From R2 it is free; in-cluster to an owned runtime it is
free.

**Storage and search** depend on the width, not the embedder:

| width | vectors (1 M) | Vectorize a month (stored + queried dimensions) | owned store (gp3, vectors plus graph) |
|---|---|---|---|
| 768 | 3.07 GB | $15.24 | $0.49 |
| 512 | 2.05 GB | $9.99 | $0.33 |
| 256 | 1.02 GB | $4.74 | $0.16 |
| 128 | 0.51 GB | $2.12 | $0.08 |

Vectorize bills queried dimensions as (queries + stored vectors) x width, so at a million queries it costs
more than the embedding itself; 256 cuts it threefold at no measurable quality cost here.

**Index rebuild.** A new prefix policy or model revision is a full re-embed (row 1 of the inference table); a
narrower width is not, if the 768 vectors were kept. On the owned CPU runtime a million-document re-embed is
days; on Workers AI it is bounded by its rate limit (3,000 requests a minute for text embeddings) and costs
cents.

## Where the owned runtime is justified, and where hosted is enough

The owned runtime is justified where Hanzo must control where the text goes or what the vectors are:

- **Residency and private deployments.** The engine runs in our cluster, a customer's VPC or an EU region,
  and the text never leaves it. Workers AI's model page states zero data retention but not where inference
  runs, and the REST API takes no region.
- **Offline and air-gapped.** No hosted endpoint is reachable there at all.
- **A pinned space.** The engine serves the revision `models.json` names (`914f7f89`), so an index can
  record `model@revision` (#808, `vec.Space.Model`) and know it holds one space. Workers AI exposes no
  revision: an index built on it cannot state which weights made its vectors, so a silent model update on
  Cloudflare's side would put two spaces in one index without anything noticing. A Workers AI index has to
  be treated as its own space (`@cf/google/embeddinggemma-2`, revision unknown), never mixed with the
  engine's, whatever the measured agreement.
- **Media already in the cluster.** An in-region embedder reads media for free; Workers AI takes only base64
  data URIs (no URLs), so every image is downloaded from storage and uploaded again on every (re)index.

Hosted inference is enough, and much cheaper, for:

- **Text and code indexes without a residency constraint**, especially bulk (re)indexing: see the cost below.
- **Multimodal retrieval today.** The engine loads the text backbone only, so images, video and audio are
  Workers AI or nothing until the vision (170M) and audio (300M) towers are ported.

## What this means for #808 (immutable index metadata)

- **A narrower width is not a new embedding.** The probe shows `dimensions: d` returns the leading `d` values
  of the 768 vector normalized again, on both sides. An index that keeps its 768 vectors can open a 512, 256
  or 128 space by truncation, with no inference; one that keeps only 128 cannot widen.
- **A prefix policy is a new embedding.** `task` and `none` vectors of the same text have a cosine of about
  0.90 (text) to 0.95 (code), so a policy change is a rebuild, as `vec.Space.Task` already records.
- **The provider is part of `Model`.** `google/embeddinggemma-2@914f7f89` (engine) and
  `@cf/google/embeddinggemma-2` (Workers AI) are two spaces even if the weights match, because only one of
  them can be pinned.

## What is compared

| | Hanzo Engine | Workers AI |
|---|---|---|
| Model | `google/embeddinggemma-2` at revision `914f7f89` (pinned, as production) | `@cf/google/embeddinggemma-2` (no revision stated or selectable) |
| Runtime | `hanzo-server` from hanzoai/engine main (`079edc61`, 1.7.108 + #119; the embedding path is unchanged since 1.7.104, which production runs), F32 on CPU | Cloudflare's |
| Modalities | text only: the vision and audio towers are not loaded, and an image request (`input: {image_url}`) answers **200 with an all-zero vector** (`prompt_tokens: 1`) rather than refusing | text, image, video, audio (`messages`, base64 data URIs only) |
| Request | `POST /v1/embeddings {model, input[], dimensions, encoding_format: base64}` | `POST /accounts/{id}/ai/run/@cf/google/embeddinggemma-2 {input[], dimensions, encoding_format: base64}` |
| Price | CPU time (see cost) | $0.0017 per M input tokens (154 neurons/M; 10,000 neurons a day free) |

In production every engine replica loads this model beside Qwen3-Embedding (universe `files/engine/models.json`,
about 1.7 GiB each), but since 2026-10-27 `zen-embedding-2` is an alias of `zen-embedding-3` (zen catalog), so no
public SKU routes to it today.

Neither server adds a task prefix: the caller writes it into the text, so both sides receive byte-identical
strings. The harness records a sha256 over every string a side sent and `compare` refuses to join two runs
whose inputs differ.

## Method

`embed_bench.py` (standard library only; numpy is used for scoring when present):

- **Datasets**, pinned by Hugging Face revision and checked by sha256 after preparation:
  - text: SciFact (BEIR, `mteb/scifact@cf10ab68`), 300 test claims against 5,183 titled abstracts;
  - code: CodeSearchNet python (`mteb/CodeSearchNetRetrieval@68e8f073`), 1,000 docstrings against 1,000 functions;
  - multimodal: Flickr30k 1K test (`nlphuji/flickr_1k_test_image_text_retrieval@c4a4cdc6`), 5,000 captions
    against 1,000 images (text to image).
- **The compared run uses a fixed subset of each**: `--max-queries 100 --max-docs 300` keeps the first 100
  judged queries, every document they judge, and a seeded sample of the rest (seed 807) up to 300 documents.
  Both sides must pass the same two flags; `compare` refuses runs over different documents. The subset keeps
  the CPU half to hours on a shared host. Absolute scores are therefore higher than on the full corpora and
  not comparable with leaderboard numbers; the comparison between the sides is what they are for.
- **Prefix policies**: `task` (the model card's prompts: `task: fact checking | query: ` for SciFact claims,
  `task: code retrieval | query: ` for docstrings, `task: search result | query: ` for captions; documents
  `title: {title or none} | text: {body}`; images take no prefix) and `none` (the dataset's raw strings).
- **Dimensions** 768, 512, 256, 128. Every input is embedded once at 768; each narrower index is the leading
  values normalized again, which is what both servers document for `dimensions`. A probe asks each server
  for every narrower width directly (8 queries and 8 documents per policy, `--probe 8`) and records the minimum cosine
  against that truncation and the vectors' norms, so the shortcut is measured on both sides.
- **No mixing**: each (suite, policy, width) is its own index. A query is scored only against documents of
  its own width and policy.
- **Metrics**: nDCG@10, Recall@10, Recall@100, MRR@10 (text, code); Recall@1/5/10 and nDCG@10 (images).
  Exact search (inner product of unit vectors), so the numbers measure the embedder, not an ANN index.
- **Agreement** (`compare`, both sides present): cosine between the two sides' 768 vectors for the same input,
  and the overlap of their top-10 lists at every width.
- **Latency**: 30 sequential one-query requests (`--latency 30`, three warm-ups dropped), p50/p90/p99 of the round trip.
  **Throughput**: the corpus embedded in batches of 32, four requests in flight; inputs/s, tokens/s (the
  server's `usage.prompt_tokens`), bytes sent.

## Reproduce

```bash
H=docs/research/embeddinggemma2-workers-ai/embed_bench.py
R=docs/research/embeddinggemma2-workers-ai/results
SAME="--max-queries 100 --max-docs 300 --probe 8 --latency 30"   # the compared run; both sides
python3 $H prepare --data /tmp/eb                       # ~150 MB, checks the pinned sha256

# Hanzo Engine (a local hanzo-server, or any OpenAI-shaped endpoint with --url and HANZO_API_KEY)
NUM_THREADS=4 hanzo-server --port 1234 --token-source none multi-model --config models.json \
  --default-model-id google/embeddinggemma-2           # models.json: the embeddinggemma-2 entry of universe's
python3 $H run --side hanzo --data /tmp/eb --out $R $SAME --label "engine 079edc61"

# Workers AI
CLOUDFLARE_ACCOUNT_ID=... CLOUDFLARE_API_TOKEN=... \
  python3 $H run --side cloudflare --data /tmp/eb --out $R $SAME

python3 $H compare --out $R                             # writes report.md and compare.json
python3 docs/research/embeddinggemma2-workers-ai/embed_bench_test.py
```

The Workers AI side can also run through Hanzo's own relay over an org's linked Cloudflare account
(`--url https://api.hanzo.ai/v1/cloudflare/ai/run/@cf/google/embeddinggemma-2` with `HANZO_API_KEY`); it adds
Hanzo's per-call BYO fee and one more hop to the latency, so the direct API is the cleaner measurement.

Run with `--out docs/research/embeddinggemma2-workers-ai/results` to add to the committed results:
`<side>.json` (every number), and per suite and policy `sample.json` (the first 32 query and document vectors)
and `top10_<dim>.json` (each query's top ten, as positions in the shared document order). Full vectors
(`*.f32`) stay on the machine that ran; `compare` measures agreement on them when both sides' are present,
otherwise on the samples.
