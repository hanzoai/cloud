# Agent Brain — retrieval on LoCoMo

```
curl -sL https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json -o locomo10.json
node brain.mjs        # embed the corpus, score single-hop and multi-hop recall
node hops.mjs         # four retrieval policies over the same index
node evidence.mjs     # where the missing evidence actually sits
```

LoCoMo (Maharana et al.): ten long conversations, 1,986 questions, each citing
the `dia_id` of the turns that answer it. Category 4 is single-hop retrieval,
category 1 is multi-hop reasoning. Retrieval here is `zen-embedding-0.6b`,
1024-d, cosine over one conversation.

## Baseline

| | single-hop (841 Q) | multi-hop (282 Q) |
|---|---|---|
| recall@5 all | 59.1% | 10.3% |
| recall@10 all | 69.3% | 14.9% |
| recall@20 all | **78.2%** | 22.7% |
| recall@20 any | 80.7% | **80.1%** |

*all* = every gold turn retrieved; *any* = at least one.

## Searching twice does not fix multi-hop

Pseudo-relevance feedback and two flavours of graph-free hop expansion, over
the same index, at k=20:

| policy | single-hop | multi-hop | Δ multi | ms |
|---|---|---|---|---|
| single | 78.2% | 22.7% | — | 1.24 |
| prf | 75.1% | **23.4%** | +0.7 | 2.48 |
| multi | 77.9% | 22.0% | −0.7 | 5.88 |
| chain | 78.1% | 22.3% | −0.4 | 8.63 |

Nought point seven points for five times the work. The hypothesis — *the right
turns are in reach, hop again and collect them* — is wrong, and `evidence.mjs`
says why.

## Where the evidence actually sits

Measured over the 282 multi-hop questions:

| | |
|---|---|
| gold turns per question | **3.1** average, up to 19 |
| evidence spanning >1 session | **95.4%** |
| median rank of a gold turn | 24 |
| median rank of the **worst** gold turn | **67** |
| questions whose worst gold turn is past rank 20 | **77.2%** |
| past rank 100 | 43.1% |

That is the whole explanation. A multi-hop question needs about three turns,
they are almost always in different sessions, and the hardest one sits at rank
67. Asking for all three inside the top twenty is asking the wrong thing of a
similarity index.

And expanding by similarity cannot reach them: turns in another session are not
paraphrases of the first hit, they are linked to it by an entity or a date. More
hops through the same metric fetch more of the same neighbourhood.

## What this measures, before anyone compares

Everything above is **retrieval recall**: did the gold turns land in the top k.
No model reads them and no answer is produced.

The published figures this tends to get held against are a different quantity.
CAR reports 78% single-hop and 30.2% multi-hop on `gpt-4o-mini` (94.8% / 51.5%
on `gpt-4o`); Naive's Brain reports 91% / 57%. Those are **answer accuracy** — a
model reads a retrieved context and is scored on what it says. Recall does not
bound accuracy and accuracy does not bound recall, and 22.7% against 57% is not
a comparison, it is a category error.

What the recall numbers do say is where the ceiling is: *any* recall@20 on
multi-hop is 80.1%, so the evidence a reader would need is usually in reach.
What has never been measured here is what a resolver on top of it would score.

## Where the field has got to

Read as reported by their authors, on their own harnesses; the architectures are
the point and the percentages are not comparable across rows.

| | what it adds | reported |
|---|---|---|
| **CAR** — *Don't Ask the LLM to Track Freshness* (Reddy & Challaram, May 2026) | structured facts extracted from candidates, then **deterministic** resolution by version and timestamp; multi-hop decomposed into atomic hops with a resolution step between each | 78% / 30.2% QA on `gpt-4o-mini`, 94.8% / 51.5% on `gpt-4o` |
| **APEX-MEM** (Apr 2026) | entity-centric property graph, temporally grounded events, append-only history, a retrieval agent that resolves conflicts at query time | 88.9% LoCoMo QA, 86.2% LongMemEval |
| **MemTxn** (Jul 2026) | a temporal resolver **outside** the model, deciding which version of a fact is visible | best mean F1 across its FactConsolidation configurations |
| **TEPA** (Aug 2026) | validity as a lifecycle — newer contradictory evidence **revokes** a stale precedent rather than sitting beside it | finds multi-hop is what remains once validity is solved |
| **Naive Brain** | advertises facts and relationships, a graph and a timeline, a Postgres spine | 91% / 57%, method unpublished |

Naive's is the row to be careful with. Their lab lists the number and marks the
paper that would explain how it gets from CAR's 30.2% to 57% as *coming soon*.
What they do publish about the shape — facts, relationships, graph, timeline —
is the same shape as every other row.

The sentence worth carrying out of CAR: **the bottleneck is assembly, not
storage.** Retrieval can hold the evidence and the system still fails, because
nothing identified, connected, ordered and resolved it. That is this
directory's result stated from the other side.

## What this says to build

Not a better embedding, and not more vector hops. **Linkage.** Similarity
becomes one candidate generator among several, and something deterministic sits
between the hops.

```
                     +- semantic
   query - planner --+- entity
                     +- relationship
                     +- temporal
                           |
                     candidate set
                           |
                   deterministic resolve
                           |
                 graph / timeline expansion
                           |
                    hop again, or answer
```

A stored memory stops being `embedding + text` and starts carrying what a
resolver needs:

```
event_id   session_id   turn_id
subject    predicate    object
identities[]            relationships[]
event_time observed_time valid_from valid_to
supersedes superseded_by
source_turn confidence
embedding  raw_text
```

Two clocks rather than one: when it happened, and when we learned it.
`valid_to` and `superseded_by` are what let a later fact revoke an earlier one
instead of competing with it in a similarity ranking — which is the whole
difference between a store and a memory.

The measurement that would settle it is the one this directory does not have
yet: the same 282 multi-hop questions, answered, with the resolver and without.

## On comparing to a published figure

Any external multi-hop number should be read with its k and its metric. At k=20
this corpus gives 22.7% under *all* and 80.1% under *any* — a spread of 57
points from the same retrieval, decided entirely by the definition. A number
without one is not a target.
