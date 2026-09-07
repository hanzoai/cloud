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

## What this says to build

Not a better embedding, and not more vector hops. **Linkage.** Index the
entities and the timeline alongside the text, so a hop can follow *"the same
person"* or *"the week after"* rather than *"words like these"*. That is the
structure the graph-based memory systems carry, and this measurement is the
argument for paying for it.

## On comparing to a published figure

Any external multi-hop number should be read with its k and its metric. At k=20
this corpus gives 22.7% under *all* and 80.1% under *any* — a spread of 57
points from the same retrieval, decided entirely by the definition. A number
without one is not a target.
