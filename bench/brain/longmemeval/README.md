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

Results: see below (filled by the run).
