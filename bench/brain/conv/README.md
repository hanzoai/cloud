# LoCoMo-Conv

arXiv 2609.03467, Chang & Chen (NTU MiuLab), September 2026: the LoCoMo QA pool
rewritten into four conversational query styles — dialog (1,986), implicit (1,986),
counterfactual (1,540; the 446 adversarial items whose premise has no answer are
dropped), composed (1,069 clusters of two QAs with overlapping evidence) — plus a
`supportive_memory` layer: turns that successful chain-of-thought answers kept
reaching for beyond the annotated gold.

## Data: not released

The paper's only release URL is footnote 1, `https://github.com/MiuLab/LoCoMo-Conv/`.
As of this commit it answers 404; GitHub's API resolves no such repository; no
dataset by that name exists on Hugging Face (search, the MiuLab author page, and
`hf download` all come back empty); GitHub code search finds no `supportive_memory`
outside unrelated projects; MiuLab's twelve most recently pushed repositories do not
include it. `data/conv/` therefore holds only our own vectors. Check the URL again
before quoting a LoCoMo-Conv number; when it lands, `retrieve.mjs` has the loader
slot (`styles()`), and the four styles score through the same `score.mjs`.

Rebuilding the styles ourselves is possible in principle — the paper says the
rewrites came from prompting GPT-4-mini with the original QA pair, keeping the gold
answer and evidence ids, and composed items pair QAs whose evidence overlaps — but
that would be our data, not theirs, and would not be comparable to their table.
Not done.

## Protocol (theirs, implemented here)

- memories: LoCoMo turns, `speaker: text`, one conversation per bank
- embedding: all-MiniLM-L6-v2 (the exact sentence-transformers weights via
  `embed-st.py`; Ollama's `all-minilm` GGUF gives the same number to 0.1 pt)
- top-k = 10
- recall = |retrieved ∩ gold| / |gold|, a gold turn counting when its verbatim text
  appears, case-insensitively, inside any returned memory
- reader Gemma-4-31B-it, T=0, max_tokens 300; judge GPT-4-mini with reasoning
  (validated against Claude Sonnet 4.5 and Qwen3.6-35B-A3B, κ .76–.80)
- response metrics: dialog/implicit `fact_used` (1 / .5 / 0); counterfactual
  unaware / hedge / corrected (0 / .5 / 1); composed atomic-fact coverage

`score.mjs` carries the recall and bootstrap intervals; the three response judges
need the paper's prompts, which are not published, so they are not implemented.

## Sanity check on the closest public proxy

The rewrites' source is the original LoCoMo QA pool, so the paper's dialog row
should sit near Naive RAG over the original questions. Exact MiniLM, top-10:

| memory unit         | pool: all 1,977 | pool: no adversarial 1,531 | tokens/q |
|---------------------|----------------:|---------------------------:|---------:|
| single turn         | 42.9 [42.0, 44.1] | 45.5 [43.3, 47.1]        |      262 |
| 2-turn window       | 56.3            | 54.3                       |      571 |
| 3-turn window       | 62.5            | 60.7                       |      829 |
| 5-turn window       | 60.3            | 58.2                       |     1258 |
| whole session       | 62.1            | 61.8                       |     6855 |
| paper, Naive RAG    | dialog .533     | counterfactual .573        |        — |

By category at the single-turn unit: single-hop 52.3, multi-hop 27.0, temporal 49.4,
open-domain 26.3, adversarial 33.9 (ALL 39.0, ANY 47.9). Latency p50 0.3 ms.

Reading: the paper does not state its memory unit, and its containment rule pays
for larger units. Single turns land ten points under its Naive RAG row; a two-turn
exchange lands within three points either side of it. Our lane keeps the single
turn — strictest, and a fifth of the tokens — and says so beside any comparison.

## Files

- `embed-st.py` — exact all-MiniLM-L6-v2 vectors → `data/conv/vec-st-minilm.json`
- `retrieve.mjs` — the harness; `run(rank, label)` takes any
  `rank(convIndex, {text, v}) -> [turn ids]`; `VEC=` picks the embedder, `K=` the cut
- `score.mjs` — the paper's recall, ALL/ANY, bootstrap CI
- `units.py` — the memory-unit sweep above
- runs: `runs/conv-proxy-naive-rag-{st-minilm,all-minilm,zenlm_zen-embedding-0.6b}-k10`,
  `runs/conv-proxy-units-st-minilm-k10`
