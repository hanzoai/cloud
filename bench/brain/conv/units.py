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
# Seeded, so the chance row below is a fact about the metric and not about today.
rng = np.random.default_rng(0)
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
preds = []
for unit in ['turn', 'w2', 'w3', 'w5', 'session']:
    for ci, conv in enumerate(corpus):
        turns = turns_of(conv); us = units(turns, unit); text = {t[0]: t[3] for t in turns}
        uv = m.encode([u[1] for u in us], normalize_embeddings=True, batch_size=128)
        qs = [q for q in conv['qa'] if q.get('evidence')]
        qv = m.encode([q['question'] for q in qs], normalize_embeddings=True, batch_size=128)
        top = np.argsort(-(qv @ uv.T), axis=1)[:, :K]
        # WHAT THE METRIC PAYS FOR NOTHING. Recall here counts a gold turn as found
        # when its text appears anywhere inside a returned memory, so a larger memory
        # contains more gold by construction and the score rises with unit size alone.
        # The docstring says the paper's Naive RAG row cannot be compared without
        # knowing its unit; this is that warning as a number. K units drawn uniformly
        # at random, same containment rule, same everything else — whatever it scores
        # is the floor that unit size hands out before any retrieval happens, and a
        # row is only worth its distance above its own chance row.
        chance = [rng.choice(len(us), size=min(K, len(us)), replace=False) for _ in qs]
        for q, idx, cidx in zip(qs, top, chance):
            gold = q['evidence'] if isinstance(q['evidence'], list) else [q['evidence']]
            got_ids = set(i for j in idx for i in us[j][0]); bodies = [us[j][1].lower() for j in idx]
            hit = [g for g in gold if g in got_ids or (text.get(g) and any(text[g].lower() in b for b in bodies))]
            recall, toks = len(hit) / len(gold), sum(len(us[j][1]) for j in idx) / 4
            cids = set(i for j in cidx for i in us[j][0]); cbodies = [us[j][1].lower() for j in cidx]
            chit = [g for g in gold if g in cids or (text.get(g) and any(text[g].lower() in b for b in cbodies))]
            crecall = len(chit) / len(gold)
            # The row, not only its average. METHOD.md declares a predictions.jsonl
            # per run and a dev/test split for LoCoMo-Conv; this table published
            # neither, so its figures — including the 8x token claim — could not be
            # cut on the held-out half or checked a question at a time. `ci` is what
            # the split is taken on, exactly as conv/retrieve.mjs takes it.
            # tokens EXACT, not rounded: the cuts average this field, and averaging a
            # rounded per-question count moves a published figure in the fourth decimal
            # for nothing. A row is data, not a display.
            preds.append({'style': unit, 'category': q['category'], 'ci': ci,
                          'recall': recall, 'tokens': toks, 'chance': crecall})
    # Chance rides every cut as a FIELD, the shape conv/retrieve.mjs publishes. It was
    # a row of its own here, which made one idea read two ways across two tables and
    # left the floor unavailable per split — the cut where it matters most, since a
    # held-out number is the one anybody quotes.
    mine = [p for p in preds if p['style'] == unit]
    def cut(key, rs):
        table[key] = {'n': len(rs), 'recall': float(np.mean([r['recall'] for r in rs])),
                      'chance': float(np.mean([r['chance'] for r in rs])),
                      'tokens': float(np.mean([r['tokens'] for r in rs])), 'units_per_conv': len(us)}
    for pool, keep in [('all', lambda p: True), ('no-adversarial', lambda p: p['category'] != 5)]:
        cut(f'{unit}·{pool}', [p for p in mine if keep(p)])
    for split, keep in [('dev', lambda p: p['ci'] <= 1), ('test', lambda p: p['ci'] > 1)]:
        cut(f'{unit}·{split}', [p for p in mine if keep(p)])
    a = table[unit + '·all']
    print(f"{unit:8s} all {a['recall']*100:5.1f}   no-adversarial {table[unit+'·no-adversarial']['recall']*100:5.1f}   chance {a['chance']*100:5.1f}   over chance {(a['recall']-a['chance'])*100:+5.1f}   test over chance {(table[unit+'·test']['recall']-table[unit+'·test']['chance'])*100:+5.1f}   tokens/q {a['tokens']:6.0f}   units in last conv {len(us)}")
(out := root / 'runs' / 'conv-proxy-units-st-minilm-k10').mkdir(parents=True, exist_ok=True)
open(out / 'predictions.jsonl', 'w').write('\n'.join(json.dumps(p) for p in preds) + '\n')
json.dump({'protocol': 'exact all-MiniLM-L6-v2, top-10, recall=|ret∩gold|/|gold| with containment, original LoCoMo QA pool', 'paper_naive_rag': {'dialog': .533, 'implicit': .312, 'counterfactual': .573, 'composed': .266}, 'table': table}, open(out / 'metrics.json', 'w'), indent=1)
