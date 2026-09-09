"""LoCoMo turns and questions under the exact all-MiniLM-L6-v2 the LoCoMo-Conv
protocol names (sentence-transformers weights, not a GGUF), written once to
data/conv/vec-st-minilm.json for conv/retrieve.mjs.

    uv run --with sentence-transformers python3 embed-st.py
"""
import json, pathlib, re, time
from sentence_transformers import SentenceTransformer
root = pathlib.Path(__file__).resolve().parent.parent
corpus = json.load(open(root / 'locomo10.json'))
m = SentenceTransformer('all-MiniLM-L6-v2')
out, t0 = [], time.time()
for conv in corpus:
    c = conv['conversation']; turns = []
    for k in sorted((k for k in c if re.fullmatch(r'session_\d+', k)), key=lambda k: int(k.split('_')[1])):
        for t in c[k]:
            turns.append({'id': t['dia_id'], 'session': k, 'speaker': t['speaker'], 'text': t['text']})
    qa = [{'question': q['question'], 'answer': q.get('answer'), 'category': q['category'],
           'evidence': q['evidence'] if isinstance(q.get('evidence'), list) else [q['evidence']] if q.get('evidence') else []} for q in conv['qa']]
    tv = m.encode([f"{t['speaker']}: {t['text']}" for t in turns], normalize_embeddings=True, batch_size=128)
    qv = m.encode([q['question'] for q in qa], normalize_embeddings=True, batch_size=128)
    for t, v in zip(turns, tv): t['v'] = [round(float(x), 6) for x in v]
    for q, v in zip(qa, qv): q['v'] = [round(float(x), 6) for x in v]
    out.append({'turns': turns, 'qa': qa})
    print(f"conversation {len(out)}/{len(corpus)}: {len(turns)} turns, {len(qa)} qa")
dst = root / 'data' / 'conv' / 'vec-st-minilm.json'; dst.parent.mkdir(parents=True, exist_ok=True)
json.dump(out, open(dst, 'w')); print(f"wrote {dst} in {time.time() - t0:.0f}s")
