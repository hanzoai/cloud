"""What a "memory" is decides the recall number. The LoCoMo-Conv paper does not
say what unit its Naive RAG indexes, and the containment rule in its recall
(a gold turn counts if its text appears inside any returned memory) rewards
larger units. So the same protocol is run with the memory unit varied: single
turns, sliding windows of 2/3/5 turns, and whole sessions — exact
all-MiniLM-L6-v2, top-10, on the original LoCoMo QA pool (the rewrites' source).

    uv run --with sentence-transformers python3 units.py
"""
import json, pathlib, re, numpy as np
from sentence_transformers import SentenceTransformer
root = pathlib.Path(__file__).resolve().parent.parent
corpus = json.load(open(root / 'locomo10.json')); m = SentenceTransformer('all-MiniLM-L6-v2'); K = 10
def turns_of(conv):
    c = conv['conversation']; out = []
    for k in sorted((k for k in c if re.fullmatch(r'session_\d+', k)), key=lambda k: int(k.split('_')[1])):
        for t in c[k]: out.append((t['dia_id'], k, f"{t['speaker']}: {t['text']}", t['text']))
    return out
def units(turns, unit):
    if unit == 'turn': return [([t[0]], t[2]) for t in turns]
    if unit == 'session':
        by = {}; [by.setdefault(t[1], []).append(t) for t in turns]
        return [([t[0] for t in ts], '\n'.join(t[2] for t in ts)) for ts in by.values()]
    w = int(unit[1:]); out = []
    for i in range(len(turns)):
        win = [t for t in turns[i:i + w] if t[1] == turns[i][1]]
        out.append(([t[0] for t in win], '\n'.join(t[2] for t in win)))
    return out
table = {}
for unit in ['turn', 'w2', 'w3', 'w5', 'session']:
    rows = []
    for conv in corpus:
        turns = turns_of(conv); us = units(turns, unit); text = {t[0]: t[3] for t in turns}
        uv = m.encode([u[1] for u in us], normalize_embeddings=True, batch_size=128)
        qs = [q for q in conv['qa'] if q.get('evidence')]
        qv = m.encode([q['question'] for q in qs], normalize_embeddings=True, batch_size=128)
        top = np.argsort(-(qv @ uv.T), axis=1)[:, :K]
        for q, idx in zip(qs, top):
            gold = q['evidence'] if isinstance(q['evidence'], list) else [q['evidence']]
            got_ids = set(i for j in idx for i in us[j][0]); bodies = [us[j][1].lower() for j in idx]
            hit = [g for g in gold if g in got_ids or (text.get(g) and any(text[g].lower() in b for b in bodies))]
            rows.append((q['category'], len(hit) / len(gold), sum(len(us[j][1]) for j in idx) / 4))
    for pool, keep in [('all', lambda c: True), ('no-adversarial', lambda c: c != 5)]:
        rs = [r for r in rows if keep(r[0])]
        table[f'{unit}·{pool}'] = {'n': len(rs), 'recall': float(np.mean([r[1] for r in rs])), 'tokens': float(np.mean([r[2] for r in rs])), 'units_per_conv': len(us)}
    print(f"{unit:8s} all {table[unit+'·all']['recall']*100:5.1f}   no-adversarial {table[unit+'·no-adversarial']['recall']*100:5.1f}   tokens/q {table[unit+'·all']['tokens']:6.0f}   units in last conv {len(us)}")
out = root / 'runs' / 'conv-proxy-units-st-minilm-k10'; out.mkdir(parents=True, exist_ok=True)
json.dump({'protocol': 'exact all-MiniLM-L6-v2, top-10, recall=|ret∩gold|/|gold| with containment, original LoCoMo QA pool', 'paper_naive_rag': {'dialog': .533, 'implicit': .312, 'counterfactual': .573, 'composed': .266}, 'table': table}, open(out / 'metrics.json', 'w'), indent=1)
