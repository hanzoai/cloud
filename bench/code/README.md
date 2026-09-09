# Hanzo Context on code — the same engine, typed links instead of facts

The claim of the memory lane is that similarity is one candidate generator
among several and that the others follow typed links. Code is the second
domain where that can be measured against public gold: a completion point
needs one snippet from another file, and the file's imports, definitions and
identifiers say which — before any model reads anything.

`context-code.mjs` has the shape of `../brain/context.mjs`: bounded
generators, one linear scorer, every contribution recorded in the trace.

| generator | what nominates a candidate |
|---|---|
| dense | cosine between the last lines above the point and the candidate (all-MiniLM-L6-v2, local Ollama; `--embed` names it) |
| lexical | BM25 over the candidates, queried with the last three lines |
| unused | the candidate defines a name the file imports that the code above has not used yet — in the cross-file-first setting the next line is that first use |
| used | the candidate defines an imported name the code above already mentions — a demotion in cross-file-first, a promotion in cross-file-random; the sign is chosen on dev |

The link generators are regular expressions over `import`, `from … import`,
`def`, `class` and identifiers. Nothing reads `next_line`; it is the answer.

## Data

- RepoBench-R (arXiv 2306.03091), Python, both settings: cross-file-first
  (`cff`) and cross-file-random (`cfr`), the `easy` and `hard` levels; fetched
  into `data/repobench-r/` (gitignored) and split once, before any run:
  dev = `train_easy[0:100] + train_hard[0:100]`, test = `test_easy[0:250] +
  test_hard[0:250]`, per setting.
- CrossCodeEval (arXiv 2310.11248): `data/cceval/` holds the release (Python,
  TypeScript, Java, C#); it is a generation benchmark whose cross-file context
  is the retrieval target, and a retrieval lane over it is the next run.
- RepoProbe (arXiv 2608.04783, ASE 2026): architecture-level questions over 50
  repositories; not fetched in this pass.

## Run

```
node context-code.mjs --setting=cff --split=dev --rows=all        # every row, dev
node context-code.mjs --setting=cff --sweep                       # dev only → ablations/frozen-cff.json
node context-code.mjs --setting=cff --split=test --frozen --write --rows=all
node context-code.mjs --setting=cfr --split=test --frozen --write --rows=all
```

Rows: dense only · BM25 only · typed links only (no model) · dense + BM25 ·
dense + typed links · BM25 + typed links (no model) · full. Metrics per row:
ALL/ANY (one gold snippet, so they coincide with R@k), MRR, nDCG at k = 1, 3,
5 with bootstrap 95% intervals, candidates examined, p50 ms; split by `easy`
and `hard`. Weights are chosen on dev by the same coordinate descent as the
memory lane (objective: MRR on dev) and frozen in `ablations/frozen-<setting>.json`
with the commit; test runs once. Runs land in `runs/repobench-r-<setting>-<split>-<row>/`.

## The LSP-gold benchmark (design)

Public code benchmarks score a completion; the engine's claim is about
retrieval, and the gold for retrieval is what a compiler already knows. The
next lane derives it from a language server over a pinned repository:

| link | gold from |
|---|---|
| definition | `textDocument/definition` |
| references | `textDocument/references` |
| implementation | `textDocument/implementation` |
| callers / callees | call hierarchy |
| type hierarchy | `typeHierarchy/supertypes`, `subtypes` |
| imports | the module graph |
| tests | the test that references the symbol |

An item is a position in the repository and the set of locations one of these
links names; a retrieval run is scored by ALL/ANY, MRR and nDCG against that
set exactly as above, and by the ten ContextBench dimensions where they apply
(answer, grounding, evidence completeness, composition, efficiency, latency,
provenance). The first repository is hanzoai/cloud itself. Items derived with
regular expressions stand in until the language-server pass runs, and are
marked as such.
