# LongMemEval

Wu et al. 2024 (arXiv 2410.10813). 500 questions, each with its own haystack of
chat sessions between a user and an assistant; one or more sessions hold the
evidence. Data: Hugging Face `xiaowu0162/longmemeval` (the S variant, ~115k tokens
of history per question; M is ~1.5M) under `data/longmemeval/` (gitignored, 2.8 GB
for all three files). The card marks this set deprecated in favour of
`xiaowu0162/longmemeval-cleaned`, which removes history sessions that interfere
with answer correctness; fetch that one before a headline run.

## Fields

`question_id`, `question_type`, `question`, `answer`, `question_date`,
`haystack_dates`, `haystack_session_ids`, `haystack_sessions` (a list of sessions,
each a list of `{role, content}` turns; the evidence turns carry `has_answer` in
the oracle file), `answer_session_ids`.

Question types in S: multi-session 133, temporal-reasoning 133, knowledge-update 78,
single-session-user 70, single-session-assistant 56, single-session-preference 30.
Item 0 has 54 sessions and 550 turns (485k characters); unique sessions across the
500 items: 4,846 with 199,641 turns.

## Official metric

Answer accuracy judged by GPT-4o against the gold answer (the paper's `evaluate_qa`);
abstention items count when the model declines. Retrieval is reported as recall of
the answer sessions (and turns) at k — the same ALL/ANY reading this bench uses.

## Baseline here

`retrieve.mjs`: every unique session embedded once, turn by turn; a session is
ranked by its best turn (`turn-max`) or by the mean of its turns (`session-mean`);
R@5 and R@10 ALL/ANY over `answer_session_ids`, MRR, per type, with retrieval
latency. Embedder from `EMBED=` (Ollama). Results in `baseline-<embed>.json`.

## Results

LongMemEval-S, all 500 questions, session recall over `answer_session_ids`, all-MiniLM-L6-v2 through
Ollama (`EMBED=all-minilm`), 50 sessions per haystack, 623 haystack sessions carry no turns and rank last.
Retrieval p50 0.20 ms, p95 0.29 ms per question once the 199,641 turns are embedded (1,146 s once, cached as float32).

| type | n | turn-max R@5 ALL / ANY | R@10 ALL / ANY | MRR | session-mean R@5 ALL / ANY | R@10 ALL / ANY | MRR |
|---|---|---|---|---|---|---|---|
| ALL | 500 | 85.8 / 97.4 | 93.4 / 98.8 | 0.903 | 82.6 / 95.8 | 92.2 / 97.4 | 0.868 |
| single-session-user | 70 | 95.7 / 95.7 | 97.1 / 97.1 | 0.852 | 91.4 / 91.4 | 94.3 / 94.3 | 0.787 |
| multi-session | 133 | 75.9 / 98.5 | 89.5 / 100.0 | 0.912 | 73.7 / 97.0 | 86.5 / 99.2 | 0.884 |
| single-session-preference | 30 | 96.7 / 96.7 | 96.7 / 96.7 | 0.803 | 93.3 / 93.3 | 96.7 / 96.7 | 0.795 |
| temporal-reasoning | 133 | 75.9 / 94.7 | 88.7 / 97.7 | 0.865 | 72.2 / 94.0 | 88.7 / 94.7 | 0.824 |
| knowledge-update | 78 | 96.2 / 100.0 | 98.7 / 100.0 | 0.964 | 91.0 / 98.7 | 98.7 / 100.0 | 0.921 |
| single-session-assistant | 56 | 100.0 / 100.0 | 100.0 / 100.0 | 1.000 | 100.0 / 100.0 | 100.0 / 100.0 | 1.000 |

Numbers from `baseline-all-minilm.json`; rerun `EMBED=all-minilm node longmemeval/retrieve.mjs` to regenerate.
This is the cosine floor of the lane, on the deprecated release; the cleaned release and the typed rows are the next runs.
