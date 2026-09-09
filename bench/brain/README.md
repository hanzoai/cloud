# Hanzo Context — memory retrieval, measured

Similarity finds what sounds related. Memory needs what is connected. This
directory is the measurement of that difference: a retrieval engine whose
candidates come from several bounded generators — dense, BM25, atomic facts,
canonical entities, the timeline, adjacency, one hop of typed expansion, and a
second hop asked with what the first resolved — scored once, with every
contribution written to the trace, and evaluated on public benchmarks under a
declared protocol. `METHOD.md` is the protocol. `RESULTS.md` is generated from
the runs. Nothing in either is typed in.

## Run

```
# data (gitignored)
curl -sL https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json -o locomo10.json
hf download ai-hyz/MemoryAgentBench --repo-type dataset --local-dir data/mab

# embeddings, once (Ollama: zenlm/zen-embedding-0.6b; all-minilm for the LoCoMo-Conv lane)
node brain.mjs                                   # brain-vectors.json: turns + questions, categories 1–4
node embed-facts.mjs                             # facts-vectors.json: LoCoMo's observation layer
node embed-store.mjs --model=all-minilm --name=minilm

# retrieval
node context.mjs --split=dev --rows=all          # every row, dev split
node sweep.mjs --rows='+iterative hops'          # dev-only search → ablations/frozen-locomo.json
node context.mjs --split=test --frozen --write --rows=all
node context.mjs --split=all  --frozen --write --rows=all
node context.mjs --embed=minilm --k=10 --split=all --frozen --write --rows=all

# answers (readers: enso-flash on the router, gemma4:31b in Ollama, gpt-oss-120b on the router)
node run.mjs --policy=context --k=20 --reader=gemma4:31b --cats=1,2,3,4
node run.mjs --policy=cosine  --k=20 --reader=gemma4:31b --cats=1,2,3,4
node run.mjs --policy=oracle  --k=20 --reader=gemma4:31b --cats=1,2,3,4

# MemoryAgentBench FactConsolidation, LoCoMo-Conv, LongMemEval
node mab/lane.mjs --help
node conv/retrieve.mjs --help
node longmemeval/retrieve.mjs --help

# the tables
node results.mjs                                 # RESULTS.md, benchmarks.json, benchmarks-retrieval.json
node tables.mjs                                  # LaTeX tables into ../../../papers/tables/
```

Credentials: `HANZO_API_KEY`, else the token `hanzo auth login` saved. Never printed,
never in a run file.

## Layout

| path | what it is |
|---|---|
| `context.mjs` | the engine: generators, one scorer, trace, evaluation, the rows of every table |
| `metrics.mjs` | ALL/ANY, R@k, MRR, nDCG, F1, EM, substring EM, bootstrap CI — defined once |
| `llm.mjs` | one way to ask a model: local Ollama first, the router second, every reply cached under `data/llm/` |
| `sweep.mjs` | coordinate descent on the dev split; writes `ablations/sweep-*.json` and `ablations/frozen-*.json` |
| `rank.mjs`, `run.mjs`, `answer.mjs`, `score.mjs` | the answer runs: a policy ranks, a reader answers, a checkpointed runner finishes |
| `extract.mjs`, `entities.mjs`, `coverage.mjs` | the write side: atomic facts, canonical entities, events, cue anchors; coverage of the gold turns |
| `mab/`, `conv/`, `longmemeval/` | the other benchmarks, each with its own README |
| `cer2.mjs`, `cer.mjs`, `oracle.mjs`, `misses.mjs`, `hops.mjs`, `evidence.mjs` | the first cut and its diagnostics, kept so the earlier numbers stay reproducible |
| `runs/<bench>-<split>-<row>-<facts>[-<embedding>]/` | `meta.json`, `metrics.json`, `traces.jsonl` or `predictions.jsonl` |
| `ablations/`, `prompts/`, `RESULTS.md`, `METHOD.md` | the search tables, every prompt, the generated results, the protocol |

## What is measured, and what is not

Retrieval is scored against annotated evidence turns: ALL (every gold turn in
the top k), ANY (at least one), R@k, MRR, nDCG. Answers are scored by token F1
and exact match (LoCoMo) or substring exact match (MemoryAgentBench). The two
are never called by each other's name. ALL is kept because a multi-hop question
is only answerable when every turn it needs is present, and kept as a
diagnostic rather than a headline because a system can ground a correct answer
on equivalent evidence the annotator did not mark — the `supported` column
counts a top-k that holds a gold turn, a turn sharing an atomic fact with a gold
turn, or a turn containing the gold answer. Splits are declared before any
search (`METHOD.md`); a configuration is frozen at a commit on dev and the test
split runs once.

## Results so far

`RESULTS.md` carries every table with intervals; `hanzo.ai/benchmarks` renders
the same files. The headline retrieval rows, LoCoMo facts, k=20:

| | multi-hop ALL / ANY | single-hop ALL | temporal ALL | nDCG | supported |
|---|---|---|---|---|---|
| test split, cosine over turns | 18.8 / 80.3 | 78.3 | 72.3 | 48.8 | 80.5 |
| test split, the engine (frozen at `33d0f874`) | **37.5 / 88.0** | **87.7** | **81.4** | **61.6** | **87.8** |
| all ten, cosine | 22.7 / 79.8 | 78.2 | 77.3 | 49.6 | 81.0 |
| all ten, the engine | **39.0 / 87.9** | **86.8** | **84.4** | **62.6** | **88.0** |
| all ten, all-MiniLM-L6-v2 vectors, k=10, cosine | 9.6 / 49.6 | 51.4 | 46.1 | 31.1 | 53.1 |
| all ten, same, the engine | **24.1 / 78.0** | **77.5** | **73.5** | **55.8** | **78.5** |

Retrieval examines about 77 candidates and takes 1–2 ms per question. The fact
index carries most of the gain; the second hop moves single-hop and temporal
questions; the shapes that lost — pseudo-relevance feedback, chain search,
surface-entity expansion, global reciprocal-rank fusion — are rows in the same
tables. Answer rows, the MemoryAgentBench lane, the extraction coverage and the
LongMemEval baseline are written into `RESULTS.md` by their runs as they finish.

LoCoMo-Conv (arXiv 2609.03467) is not released at the time of writing — its one
release address answers 404 — so no LoCoMo-Conv number appears anywhere here.
`conv/` holds the harness on the paper's protocol and the sanity check on the
original questions (`RESULTS.md`, `conv-proxy-*`), which found that the memory
unit alone moves R@10 by twenty points and the embedder by seventeen.

## The first cut, and what it taught

`cer2.mjs` was the four-term pool and re-rank that shipped in
`/v1/memory/search`: cosine over turns plus the best citing fact, adjacency and
a time cue. On all ten conversations it moved multi-hop ALL@20 from 22.7 to
36.2 and single-hop from 78.2 to 81.8; by k, multi-hop ALL is 12.8 at 5, 23.0 at
10, 36.2 at 20. Three findings from that cut shaped the engine:

- **Searching twice does not fix multi-hop.** Pseudo-relevance feedback and
  two hop-by-similarity policies gained 0.7 points for five times the work
  (`hops.mjs`). `evidence.mjs` says why: a multi-hop question needs 3.1 turns on
  average, 95.4% of them span more than one session, and the worst gold turn
  sits at median rank 67. Turns in another session are not paraphrases of the
  first hit; they are linked to it by a person or a date.
- **The fact index is the representation, and it has a ceiling.** 79.9% of
  multi-hop gold turns are cited by some LoCoMo observation, and for those the
  best citing fact ranks at median 8 where the turn ranks at median 20
  (`misses.mjs`). The other 20.1% are cited by nothing; that is what the write
  side (`extract.mjs`) is for.
- **Surface linkage does not work.** Fusing whole ranked lists lost four points
  (`cer.mjs`); a hop through shared capitalised words lost two. "Sunday" is not
  an entity. The hop has to follow a typed thing — the same person, the week
  after — which is, again, extraction.

## Reference points, read with their protocol

CAR (arXiv 2606.01435) reports MemoryAgentBench FactConsolidation 78.0 / 30.2
(single / multi-hop) pooled over 6k–262k haystacks with gpt-4o-mini and 94.8 /
51.5 with gpt-4o; at 262k alone, 82 / 27 and 93 / 41. APEX-MEM reports 88.9 on
LoCoMo QA and 86.2 on LongMemEval under its own setup. Mem0 and Zep publish
92.5 and 94.7 on LoCoMo under their answer-accuracy protocols with LLM judges.
Pith claims 68.0 substring EM on MemoryAgentBench multi-hop 262k. A "91 / 57"
attributed to Naive Brain has no published source or method and is not compared
against. A number from any of them is comparable to a row here only where the
split, the embedder, the reader and the metric match, and `RESULTS.md` says where
they do.
