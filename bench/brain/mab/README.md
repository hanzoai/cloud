# MemoryAgentBench · Conflict Resolution (FactConsolidation)

The lane CAR reports on (78.0 SH / 30.2 MH with gpt-4o-mini; 94.8 / 51.5 with
gpt-4o) and the one Naive's 91/57 is set beside. A haystack is a numbered list
of templated facts; a later fact about the same (subject, relation) silently
replaces an earlier one, and nothing marks the update. Metric:
`substring_exact_match`, the benchmark's own.

    node parse.mjs                       # extract typed facts, report coverage and update chains
    node index.mjs                       # dense (zen-embedding) + BM25 + entity + supersession indexes, cached
    node compile.mjs --model=<m> [--api=<openai-compatible base>] [--temperature=<t>] [--prompt=<file>] --out=plans-<tag>.json --force
    node lane.mjs --split=dev --rows=beam        # the typed search over every plan set on disk; no reader
    node lane.mjs --split=test --rows=beam       # once, after dev
    node nbest.mjs --run=mab-test-beam-none      # plan recall@N · execution success@N · selection accuracy@N · unique solves
    node misses.mjs --run=mab-test-beam-none     # every miss in the bucket where its chain broke
    node oracle.mjs --run=beam                   # what a perfect planner / entity lookup / version choice would reach
    node versions.mjs                            # which version the gold means, measured on dev
    node freeze.mjs --tag=<tag> --verify         # rerun, check, archive everything a rerun needs
    node table.mjs                               # the table from runs/mab-*

Dev = the two 6k haystacks; test = 32k, 64k, 262k. The metric is the benchmark's
substring exact match over every question of every haystack; an unanswered
question scores 0.

## How a question is answered

1. **Parse.** Every fact line becomes {serial, subject, relation, object}; 45
   templates, no line unparsed at any size; the supersession chain is keyed by
   (subject, relation). `versions.mjs` established on dev that "current" means the
   latest serial: for single-hop questions the gold is the last version 71 times
   and the first 0; multi-hop chains are reachable with the latest version at every
   hop 63 times and with the first 0.
2. **Compile.** A plan is a base entity and a chain of relations, or relation
   families (`language`, `origin`, `maker`, `leader`, `place`, `work`). Exact
   single-hop templates are compiled by rule; everything else by a model at
   temperature 0 (or sampled), or by the hand-written lexicon. Each compiler writes
   its own `plans-<tag>.json`, so a run can carry several plans per question.
3. **Search.** `beam.mjs` treats each plan as a sketch: the planned relation is
   preferred, its family-mates allowed at a cost, an older version taken only when
   the latest dead-ends, one typed bridge through the entity's own facts when it
   lacks the relation, a planned relation nothing satisfies skipped at a cost, and
   the question's answer type as the final constraint. The best complete path
   wins by a fixed score; with several plans, the best path over all of them. It
   is deterministic, reads no gold, and calls no model. The `beam` row emits the
   resolved object string; nothing reads the facts.
4. **Diagnose.** `oracle.mjs` searches the parsed graph for any path from the
   plan's base entity to the gold: at 262k a path exists for 65 of 67 misses of
   the plain resolver and 50 of 52 of the search's, which puts the ceiling of a
   perfect planner at 98 and says the remaining error is the plan. `misses.mjs`
   buckets each miss by where the chain broke; `nbest.mjs` separates plan recall
   from selection.

| row | what it does |
|---|---|
| noreader | the plain resolver: follow the chain, latest version at each hop, one bridge; the resolved string is the answer |
| beam | the typed search above, over every plan set on disk |
| semantic / lexical / rrf / entities / timeline / hops / resolver / full | earlier rows that hand facts to a reader; kept for the record |

## Reference numbers (verified from the CAR paper, arXiv 2606.01435)

CAR pools its headline over the 6K–262K haystacks: **78.0 SH / 30.2 MH** with
gpt-4o-mini, **94.8 / 51.5** with gpt-4o. At 262K alone the same pipelines
score 82 / 27 (4o-mini) and 93 / 41 (4o). Baselines at 262K: HippoRAG-v2 54 / 5,
gpt-4o whole-context 60 / 5, BM25 48 / 3, Cognee and MemGPT 28 / 3, Mem0 18 / 2,
Zep 7 / 3. Pith claims 68.0 EM on MH 262K on a vendor page, not peer-reviewed.
Naive's 91 / 57 is published without a protocol. `table.mjs` prints both the
per-size cells and the pooled mean so either comparison can be made, and names
the reader on every row; a row with a different reader is a different table.
