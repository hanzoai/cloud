# MemoryAgentBench · Conflict Resolution (FactConsolidation)

The lane CAR reports on (78.0 SH / 30.2 MH with gpt-4o-mini; 94.8 / 51.5 with
gpt-4o) and the one Naive's 91/57 is set beside. A haystack is a numbered list
of templated facts; a later fact about the same (subject, relation) silently
replaces an earlier one, and nothing marks the update. Metric:
`substring_exact_match`, the benchmark's own.

    node parse.mjs                       # extract typed facts, report coverage and update chains
    node index.mjs                       # dense (zen-embedding) + BM25 + entity + supersession indexes, cached
    node compile.mjs --model=...         # question → (entity, relation chain), once per question
    node lane.mjs --split=dev --rows=noreader,resolver,full,... --reader=gemma4:31b
    node table.mjs                       # the table from runs/mab-*

Dev = the two 6k haystacks; test = 32k, 64k, 262k. Every row hands the same
reader the same prompt (`prompts/reader-mab.txt`); rows differ only in which
facts reach it and in what order.

| row | what reaches the reader |
|---|---|
| semantic | dense top-k facts by the question |
| lexical | dense ∪ BM25 top-k, in serial order |
| rrf | global reciprocal-rank fusion of dense, BM25 and the entity's facts (a failed variant) |
| entities | every fact of the planned base entity, all versions, plus dense top-5 |
| timeline | the same, current versions only (latest serial per (subject, relation)) |
| hops | the plan followed hop by hop with every version kept; the reader picks |
| resolver | the plan followed hop by hop, the latest version chosen deterministically at each hop |
| full | resolver, with lexical and dense fallbacks when an exact (entity, relation) is missing |
| noreader | full's resolved object string, no model at all |

The write side is `parse.mjs`: every line becomes {serial, subject, relation,
object}, and the supersession chain is keyed by (subject, relation). The read
side is `compile.mjs` (query understanding, one model call per question,
independent of any haystack) and `lane.mjs` (retrieve, resolve, read).

## Reference numbers (verified from the CAR paper, arXiv 2606.01435)

CAR pools its headline over the 6K–262K haystacks: **78.0 SH / 30.2 MH** with
gpt-4o-mini, **94.8 / 51.5** with gpt-4o. At 262K alone the same pipelines
score 82 / 27 (4o-mini) and 93 / 41 (4o). Baselines at 262K: HippoRAG-v2 54 / 5,
gpt-4o whole-context 60 / 5, BM25 48 / 3, Cognee and MemGPT 28 / 3, Mem0 18 / 2,
Zep 7 / 3. Pith claims 68.0 EM on MH 262K on a vendor page, not peer-reviewed.
Naive's 91 / 57 is published without a protocol. `table.mjs` prints both the
per-size cells and the pooled mean so either comparison can be made, and names
the reader on every row; a row with a different reader is a different table.
