#!/usr/bin/env python3
"""EmbeddingGemma 2 on Hanzo Engine against Cloudflare Workers AI `@cf/google/embeddinggemma-2`.

One harness, two sides, the same bytes (hanzo-inc/cloud#807). Each side embeds the same documents and
queries, written by the same prefix policy, and is scored by the same code. Only the standard library
is needed; numpy, when importable, only makes scoring faster.

  prepare  download the pinned public datasets into --data and check them against their sha256
  run      embed one side's suites into --out/<side>/ and score them (--out/<side>.json)
  compare  join the sides: vector agreement, retrieval deltas, latency and full cost (--out/report.md)

Suites (fixed public sets, pinned by revision and checked by sha256 after preparation):

  text   SciFact (BEIR, via mteb/scifact): 300 test claims against 5,183 titled abstracts.
  code   CodeSearchNet python (mteb/CodeSearchNetRetrieval): 1,000 docstrings against 1,000 functions.
  image  Flickr30k 1K test (nlphuji/flickr_1k_test_image_text_retrieval): 5,000 captions against 1,000
         images, text to image.

Prefix policies (the same text goes to both sides; neither server adds a prefix):

  task   the model card's prompts: queries "task: <task> | query: <q>", documents
         "title: <title or none> | text: <body>". Images carry no prefix (model card).
  none   the dataset's raw strings: the query as is, the document as "<title> <body>".

Dimensions: every input is embedded once at 768 and the 512, 256 and 128 indexes are its leading values
normalized again, which is what both servers' `dimensions` does (Matryoshka truncation). A probe asks each
server for every width directly and records its cosine with that truncation, so the shortcut is measured,
not assumed. Each (suite, policy, dimension) is its own index: no vector is ever scored against a vector
of another width or another policy.

Sides:

  hanzo       POST --url (default http://127.0.0.1:1234/v1/embeddings), OpenAI-shaped, bearer
              $HANZO_API_KEY when set, `model` from --model (default google/embeddinggemma-2).
  cloudflare  POST https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/ai/run/
              @cf/google/embeddinggemma-2 with bearer $CLOUDFLARE_API_TOKEN; or --url
              https://api.hanzo.ai/v1/cloudflare/ai/run/@cf/google/embeddinggemma-2 with
              $HANZO_API_KEY (Hanzo's relay over an org's own linked Cloudflare account).

Example:

  python3 docs/research/embeddinggemma2-workers-ai/embed_bench.py prepare --data /tmp/eb
  python3 docs/research/embeddinggemma2-workers-ai/embed_bench.py run --side hanzo --data /tmp/eb --out /tmp/eb/out
  CLOUDFLARE_ACCOUNT_ID=... CLOUDFLARE_API_TOKEN=... \\
    python3 docs/research/embeddinggemma2-workers-ai/embed_bench.py run --side cloudflare --data /tmp/eb --out /tmp/eb/out
  python3 docs/research/embeddinggemma2-workers-ai/embed_bench.py compare --out /tmp/eb/out
"""
import argparse
import base64
import concurrent.futures
import csv
import hashlib
import heapq
import io
import json
import math
import operator
import os
import platform
import random
import re
import struct
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

try:  # optional: only speeds up scoring
    import numpy as np
except ImportError:  # pragma: no cover - exercised where numpy is absent
    np = None

DIMS = (768, 512, 256, 128)
POLICIES = ("task", "none")
SAMPLE = 32  # vectors per kind kept in the committable sample.json, for agreement across machines
SUITE_NAMES = ("text", "code", "image")
CF_MODEL = "@cf/google/embeddinggemma-2"
HANZO_MODEL = "google/embeddinggemma-2"
HF = "https://huggingface.co"
HF_ROWS = "https://datasets-server.huggingface.co/rows"
UA = "hanzo-embed-bench/1"

# The prompt each suite's queries take under the `task` policy (model card, "Prompts").
QUERY_TASK = {
    "text": "task: fact checking | query: ",
    "code": "task: code retrieval | query: ",
    "image": "task: search result | query: ",
}

SUITES = {
    "text": {
        "dataset": "mteb/scifact",
        "revision": "cf10ab6856b15b0e670ef8ae5dae4e266c12d035",
        "license": "CC BY-NC 2.0 (SciFact)",
    },
    "code": {
        "dataset": "mteb/CodeSearchNetRetrieval",
        "revision": "68e8f0731a656fa4bd5b7c81936d95ad48a39bfe",
        "config": "python",
        "license": "MIT (dataset card); code under its repositories' licenses",
    },
    "image": {
        "dataset": "nlphuji/flickr_1k_test_image_text_retrieval",
        "revision": "c4a4cdc67b77fce148b45484a067957bf75ec4c3",
        "license": "Flickr30k terms (non-commercial research)",
    },
}

# sha256 of each prepared file. A mismatch means the upstream rows changed and results would no longer
# compare with published ones; `prepare --allow-drift` records the new digests instead of failing.
EXPECTED = {
    "text": {
        "corpus.jsonl": "2b37c383eaab6226df61fc52058396d91c2fb027f4c1bbd75aa15a2928316066",
        "queries.jsonl": "b3a87da17d18cba4f99921c4a7feaa60837b2027290962b97fe746a829c5b0ef",
        "qrels.jsonl": "4f3b8900188996b73b5de19261b7f5af6ac44c88fb5688bf57961eea22fe7193",
    },
    "code": {
        "corpus.jsonl": "47a8d1a1b2a5136f4e43a13664e1e322248ce9d6b3da4110804b4a8cbda14544",
        "queries.jsonl": "a4c17c4fbd92bba1b3298fd1432ec5965dc03ee1ee08ef413419d636d4ca8337",
        "qrels.jsonl": "40fdbd652939af9ac2fbb1a65470a54d173f0ca7c5cb18b01993c679865156d5",
    },
    "image": {
        "corpus.jsonl": "6ed2da72bfae8d24e6118739258aadeae75ba4fd7ed023f726e1cea38333f04b",
        "queries.jsonl": "f7f7ca1417a2657242c71769a9cec5b78f02a69abe8f15be89f07f9d91279fe3",
        "qrels.jsonl": "7bca201c59950ee5abb8dbba73a2f90169e9c17c8ac520647fcac7df7e87bc71",
        "images": "b931804b4db6be3efed5ce621fce5767a739766310f3e5f53e8b0b8a92f11807",
    },
}

# Public list prices (USD) the cost model uses, with where each comes from. Override any with --price.
PRICES = {
    "cf_per_m_tokens": (0.0017, "Workers AI pricing: @cf/google/embeddinggemma-2, $0.0017 per M input tokens"),
    "cf_neurons_per_m_tokens": (154, "Workers AI pricing: 154 neurons per M input tokens"),
    "cf_free_neurons_per_day": (10_000, "Workers AI pricing: 10,000 neurons a day at no charge"),
    "cf_tokens_per_image": (260, "Workers AI model page: an image is about 260 tokens (used only until a run measures it)"),
    "vectorize_queried_per_m_dims":(0.01, "Vectorize pricing: $0.01 per M queried vector dimensions"),
    "vectorize_stored_per_100m_dims": (0.05, "Vectorize pricing: $0.05 per 100 M stored vector dimensions"),
    "vectorize_queried_included": (50_000_000, "Vectorize pricing: 50 M queried dimensions a month included (Paid)"),
    "vectorize_stored_included": (10_000_000, "Vectorize pricing: 10 M stored dimensions included (Paid)"),
    "r2_per_gb_month": (0.015, "R2 pricing: Standard storage $0.015 per GB-month, egress free"),
    "s3_egress_per_gb": (0.09, "AWS S3 data transfer out to the internet, first 10 TB/month, us-east-1"),
    "gp3_per_gb_month": (0.08, "AWS EBS gp3 $0.08 per GB-month, us-east-1 (universe infra/aws/main.tf)"),
    "replica_hour": (0.2315, "AWS m7a.xlarge on demand, $169/month list, us-east-1 (universe infra/aws/main.tf);"
                     " one engine replica works on about four cores (universe values/hanzo/engine.yaml)"),
    "replica_tokens_per_s": (385, "engine 1.7.104, EmbeddingGemma 2 on four cores in the production pod envelope,"
                             " ~310-token passages (universe values/hanzo/engine.yaml, 2026-10-08)"),
}


# ── small pure helpers (unit-tested) ────────────────────────────────────────────────────────────


def sha256_bytes(b):
    return hashlib.sha256(b).hexdigest()


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def l2_normalize(v):
    n = math.sqrt(sum(x * x for x in v))
    return [x / n for x in v] if n else list(v)


def truncate(v, dim):
    """The leading `dim` values, L2-normalized again (Matryoshka; what `dimensions` returns)."""
    return l2_normalize(v[:dim])


def cosine(a, b):
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    return sum(map(operator.mul, a, b)) / (na * nb) if na and nb else 0.0


def decode_embedding(e):
    """An embedding as floats, from a float list or base64 little-endian float32."""
    if isinstance(e, str):
        raw = base64.b64decode(e)
        return list(struct.unpack(f"<{len(raw) // 4}f", raw))
    return [float(x) for x in e]


def parse_response(obj):
    """(vectors in input order, prompt tokens or None) from an OpenAI-shaped or Cloudflare-wrapped body."""
    if isinstance(obj, dict) and "result" in obj and isinstance(obj["result"], dict):
        if obj.get("success") is False:
            raise ValueError(f"cloudflare error: {obj.get('errors')}")
        obj = obj["result"]
    data = obj.get("data")
    if not isinstance(data, list):
        raise ValueError(f"no data in response: {str(obj)[:300]}")
    rows = sorted(data, key=lambda d: d.get("index", 0))
    vecs = [decode_embedding(d["embedding"]) for d in rows]
    usage = obj.get("usage") or {}
    return vecs, usage.get("prompt_tokens")


def doc_text(policy, doc):
    title, body = doc.get("title") or "", doc["text"]
    if policy == "task":
        return f"title: {title or 'none'} | text: {body}"
    return f"{title} {body}".strip()


def query_text(suite, policy, query):
    return QUERY_TASK[suite] + query["text"] if policy == "task" else query["text"]


def inputs_digest(items):
    """sha256 over the exact strings a side sent, in order: two sides compare only if theirs match."""
    h = hashlib.sha256()
    for s in items:
        b = s.encode() if isinstance(s, str) else s
        h.update(len(b).to_bytes(8, "little"))
        h.update(b)
    return h.hexdigest()


def subsample(corpus, qrels, query_ids, max_docs, seed=807):
    """Every judged document of the kept queries, then the rest in a seeded shuffle, up to max_docs."""
    if not max_docs or max_docs >= len(corpus):
        return corpus
    keep = {d for q in query_ids for d in qrels.get(q, {})}
    others = [d["id"] for d in corpus if d["id"] not in keep]
    random.Random(seed).shuffle(others)
    keep.update(others[: max(0, max_docs - len(keep))])
    return [d for d in corpus if d["id"] in keep]


def ndcg_at(ranked, rels, k):
    dcg = sum(rels.get(d, 0) / math.log2(i + 2) for i, d in enumerate(ranked[:k]))
    ideal = sorted(rels.values(), reverse=True)[:k]
    idcg = sum(r / math.log2(i + 2) for i, r in enumerate(ideal))
    return dcg / idcg if idcg else 0.0


def recall_at(ranked, rels, k):
    relevant = {d for d, r in rels.items() if r > 0}
    return len(relevant & set(ranked[:k])) / len(relevant) if relevant else 0.0


def mrr_at(ranked, rels, k):
    for i, d in enumerate(ranked[:k]):
        if rels.get(d, 0) > 0:
            return 1.0 / (i + 1)
    return 0.0


def percentile(xs, p):
    if not xs:
        return None
    s = sorted(xs)
    i = (len(s) - 1) * p / 100
    lo, hi = math.floor(i), math.ceil(i)
    return s[lo] + (s[hi] - s[lo]) * (i - lo)


def score(qvecs, dvecs, qids, dids, qrels, dims=DIMS, topk=100):
    """Retrieval metrics per dimension. Each width is its own index of that width only."""
    out = {}
    for dim in dims:
        Q = [truncate(v, dim) for v in qvecs]
        D = [truncate(v, dim) for v in dvecs]
        rankings = rank(Q, D, topk)
        per = {"ndcg@10": [], "recall@1": [], "recall@5": [], "recall@10": [], "recall@100": [], "mrr@10": []}
        for qi, qid in enumerate(qids):
            rels = qrels.get(qid, {})
            if not rels:
                continue
            ranked = [dids[j] for j in rankings[qi]]
            per["ndcg@10"].append(ndcg_at(ranked, rels, 10))
            for k in (1, 5, 10, 100):
                per[f"recall@{k}"].append(recall_at(ranked, rels, k))
            per["mrr@10"].append(mrr_at(ranked, rels, 10))
        out[str(dim)] = {m: round(sum(v) / len(v), 5) for m, v in per.items() if v}
        out[str(dim)]["queries"] = len(per["ndcg@10"])
        out[str(dim)]["_rankings"] = rankings
    return out


def rank(Q, D, topk):
    """Indices of the top-k documents per query by inner product (vectors are unit length)."""
    if np is not None:
        q, d = np.asarray(Q, dtype=np.float32), np.asarray(D, dtype=np.float32)
        s = q @ d.T
        k = min(topk, s.shape[1])
        part = np.argpartition(-s, k - 1, axis=1)[:, :k]
        rows = np.arange(s.shape[0])[:, None]
        order = np.argsort(-s[rows, part], axis=1, kind="stable")
        return part[rows, order].tolist()
    out = []
    for qv in Q:
        scores = [sum(map(operator.mul, qv, dv)) for dv in D]
        out.append(heapq.nlargest(min(topk, len(D)), range(len(D)), key=scores.__getitem__))
    return out


def redact_url(url):
    return re.sub(r"/accounts/[^/]+/", "/accounts/{account}/", url)


# ── datasets ────────────────────────────────────────────────────────────────────────────────────


def fetch(url, timeout=120):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    for attempt in range(5):
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.read()
        except urllib.error.HTTPError as e:
            if e.code not in (429, 500, 502, 503, 504) or attempt == 4:
                raise
        except (urllib.error.URLError, TimeoutError):
            if attempt == 4:
                raise
        time.sleep(2 ** attempt)


def hf_file(spec, path):
    return fetch(f"{HF}/datasets/{spec['dataset']}/resolve/{spec['revision']}/{path}")


def hf_rows(dataset, config, split="test"):
    """Every row of a parquet-backed dataset split through the datasets-server JSON API."""
    rows, offset = [], 0
    while True:
        q = urllib.parse.urlencode({"dataset": dataset, "config": config, "split": split,
                                    "offset": offset, "length": 100})
        page = json.loads(fetch(f"{HF_ROWS}?{q}"))
        for r in page["rows"]:
            if r.get("truncated_cells"):
                raise RuntimeError(f"{dataset}/{config}: row {r['row_idx']} came back truncated")
            rows.append(r["row"])
        offset += len(page["rows"])
        if not page["rows"] or offset >= page.get("num_rows_total", 0):
            return rows


def write_jsonl(path, rows):
    with open(path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n")


def read_jsonl(path):
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


def prepare_text(spec, out):
    corpus = [json.loads(line) for line in hf_file(spec, "corpus.jsonl").decode().splitlines() if line.strip()]
    queries = {q["_id"]: q["text"] for q in
               (json.loads(line) for line in hf_file(spec, "queries.jsonl").decode().splitlines() if line.strip())}
    qrels = [json.loads(line) for line in hf_file(spec, "qrels/test.jsonl").decode().splitlines() if line.strip()]
    qids = sorted({str(r["query-id"]) for r in qrels}, key=lambda x: (len(x), x))
    write_jsonl(os.path.join(out, "corpus.jsonl"),
                [{"id": str(d["_id"]), "title": d.get("title") or "", "text": d["text"]} for d in corpus])
    write_jsonl(os.path.join(out, "queries.jsonl"), [{"id": q, "text": queries[q]} for q in qids])
    write_jsonl(os.path.join(out, "qrels.jsonl"),
                [{"qid": str(r["query-id"]), "did": str(r["corpus-id"]), "score": int(float(r["score"]))}
                 for r in qrels])


def prepare_code(spec, out):
    c = spec["config"]
    corpus = sorted(hf_rows(spec["dataset"], f"{c}-corpus"), key=lambda r: r["id"])
    queries = sorted(hf_rows(spec["dataset"], f"{c}-queries"), key=lambda r: r["id"])
    qrels = sorted(hf_rows(spec["dataset"], f"{c}-qrels"), key=lambda r: (r["query-id"], r["corpus-id"]))
    write_jsonl(os.path.join(out, "corpus.jsonl"),
                [{"id": r["id"], "title": r.get("title") or "", "text": r["text"]} for r in corpus])
    write_jsonl(os.path.join(out, "queries.jsonl"), [{"id": r["id"], "text": r["text"]} for r in queries])
    write_jsonl(os.path.join(out, "qrels.jsonl"),
                [{"qid": r["query-id"], "did": r["corpus-id"], "score": int(r.get("score", 1))} for r in qrels])


def prepare_image(spec, out):
    rows = list(csv.DictReader(io.StringIO(hf_file(spec, "test_1k_flickr.csv").decode())))
    archive = zipfile.ZipFile(io.BytesIO(hf_file(spec, "images_flickr_1k_test.zip")))
    members = {os.path.basename(n): n for n in archive.namelist() if n.lower().endswith(".jpg")}
    img_dir = os.path.join(out, "images")
    os.makedirs(img_dir, exist_ok=True)
    corpus, queries, qrels = [], [], []
    for r in sorted(rows, key=lambda r: r["filename"]):
        name = r["filename"]
        if os.path.basename(name) != name or not name.lower().endswith(".jpg"):
            raise RuntimeError(f"unexpected image name {name!r}")
        with open(os.path.join(img_dir, name), "wb") as f:
            f.write(archive.read(members[name]))
        corpus.append({"id": name, "file": f"images/{name}"})
        for k, caption in enumerate(json.loads(r["raw"])):
            qid = f"{name}#{k}"
            queries.append({"id": qid, "text": caption})
            qrels.append({"qid": qid, "did": name, "score": 1})
    write_jsonl(os.path.join(out, "corpus.jsonl"), corpus)
    write_jsonl(os.path.join(out, "queries.jsonl"), queries)
    write_jsonl(os.path.join(out, "qrels.jsonl"), qrels)


def suite_digests(dirpath, suite):
    d = {f: sha256_file(os.path.join(dirpath, f)) for f in ("corpus.jsonl", "queries.jsonl", "qrels.jsonl")}
    if suite == "image":
        h = hashlib.sha256()
        for doc in read_jsonl(os.path.join(dirpath, "corpus.jsonl")):
            h.update(doc["id"].encode() + b"\0" + sha256_file(os.path.join(dirpath, doc["file"])).encode())
        d["images"] = h.hexdigest()
    return d


def cmd_prepare(a):
    for suite in a.suites:
        spec, out = SUITES[suite], os.path.join(a.data, suite)
        os.makedirs(out, exist_ok=True)
        print(f"prepare {suite}: {spec['dataset']}@{spec['revision'][:12]}", file=sys.stderr)
        {"text": prepare_text, "code": prepare_code, "image": prepare_image}[suite](spec, out)
        got = suite_digests(out, suite)
        want = EXPECTED.get(suite) or {}
        drift = {k: v for k, v in got.items() if want and want.get(k) != v}
        if drift and not a.allow_drift:
            sys.exit(f"{suite}: prepared files differ from the pinned digests: {sorted(drift)} "
                     f"(rerun with --allow-drift to accept)")
        with open(os.path.join(out, "manifest.json"), "w") as f:
            json.dump({"suite": suite, **spec, "sha256": got, "pinned": bool(want) and not drift}, f, indent=2)
        print(json.dumps({"suite": suite, "sha256": got}), flush=True)


def load_suite(data, suite, max_docs, max_queries):
    d = os.path.join(data, suite)
    with open(os.path.join(d, "manifest.json")) as f:
        manifest = json.load(f)
    corpus = read_jsonl(os.path.join(d, "corpus.jsonl"))
    queries = read_jsonl(os.path.join(d, "queries.jsonl"))
    qrels = {}
    for r in read_jsonl(os.path.join(d, "qrels.jsonl")):
        qrels.setdefault(r["qid"], {})[r["did"]] = r["score"]
    queries = [q for q in queries if q["id"] in qrels]
    if max_queries:
        queries = queries[:max_queries]
    corpus = subsample(corpus, qrels, [q["id"] for q in queries], max_docs)
    if suite == "image":
        for doc in corpus:
            doc["path"] = os.path.join(d, doc["file"])
    return manifest, corpus, queries, qrels


# ── the two sides ───────────────────────────────────────────────────────────────────────────────


class Side:
    def __init__(self, name, url, token, model, timeout):
        self.name, self.url, self.token, self.model, self.timeout = name, url, token, model, timeout

    def text_body(self, texts, dim):
        body = {"input": texts, "encoding_format": "base64"}
        if dim != 768:
            body["dimensions"] = dim
        if self.name == "hanzo":
            body["model"] = self.model
        return body

    def image_body(self, uris, dim):
        if self.name == "hanzo":  # the engine's own image form; one image per request
            body = {"model": self.model, "input": {"image_url": uris[0]}, "encoding_format": "base64"}
        else:
            body = {"messages": [[{"role": "user", "content": [{"type": "image_url", "image_url": {"url": u}}]}]
                                 for u in uris], "encoding_format": "base64"}
        if dim != 768:
            body["dimensions"] = dim
        return body

    def post(self, body, retries=6):
        raw = json.dumps(body).encode()
        headers = {"Content-Type": "application/json", "User-Agent": UA}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        tries = 0
        while True:
            tries += 1
            t0 = time.perf_counter()
            try:
                req = urllib.request.Request(self.url, data=raw, headers=headers, method="POST")
                with urllib.request.urlopen(req, timeout=self.timeout) as r:
                    payload = r.read()
                dt = time.perf_counter() - t0
                vecs, tokens = parse_response(json.loads(payload))
                if any(not any(v) for v in vecs):  # a 200 is not an embedding: refuse it, never index it
                    raise RuntimeError(f"HTTP 200 with an all-zero vector ({tokens} prompt tokens): not an embedding")
                return {"vecs": vecs, "tokens": tokens, "seconds": dt, "sent": len(raw),
                        "received": len(payload), "tries": tries}
            except urllib.error.HTTPError as e:
                detail = e.read()[:400].decode(errors="replace")
                if e.code in (408, 429, 500, 502, 503, 504) and tries < retries:
                    time.sleep(min(30, 2 ** tries))
                    continue
                raise RuntimeError(f"HTTP {e.code}: {detail}") from None
            except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
                timed_out = isinstance(e, TimeoutError) or isinstance(getattr(e, "reason", None), TimeoutError)
                if timed_out:  # a retry would send the same work to a server already busy with it
                    raise RuntimeError(f"no answer in {self.timeout:.0f}s: raise --timeout or lower --batch") from None
                if tries < retries:
                    time.sleep(min(30, 2 ** tries))
                    continue
                raise RuntimeError(f"{type(e).__name__}: {e}") from None


def embed_all(side, items, make_body, batch, concurrency, label):
    """Embeds `items` in order; returns vectors and the run's throughput record."""
    chunks = [items[i:i + batch] for i in range(0, len(items), batch)]
    results = [None] * len(chunks)
    t0 = time.perf_counter()
    done = 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        futs = {pool.submit(lambda c=c: side.post(make_body(c))): i for i, c in enumerate(chunks)}
        for fut in concurrent.futures.as_completed(futs):
            i = futs[fut]
            results[i] = fut.result()
            if len(results[i]["vecs"]) != len(chunks[i]):
                raise RuntimeError(f"{label}: sent {len(chunks[i])} inputs, got {len(results[i]['vecs'])} vectors")
            done += 1
            if done % max(1, len(chunks) // 10) == 0 or done == len(chunks):
                print(f"  {label}: {done}/{len(chunks)} requests, {time.perf_counter() - t0:.1f}s",
                      file=sys.stderr, flush=True)
    wall = time.perf_counter() - t0
    vecs = [v for r in results for v in r["vecs"]]
    tokens = [r["tokens"] for r in results]
    rec = {
        "inputs": len(items), "requests": len(chunks), "batch": batch, "concurrency": concurrency,
        "wall_s": round(wall, 3), "inputs_per_s": round(len(items) / wall, 3),
        "tokens": sum(tokens) if all(t is not None for t in tokens) else None,
        "bytes_sent": sum(r["sent"] for r in results), "bytes_received": sum(r["received"] for r in results),
        "retries": sum(r["tries"] - 1 for r in results),
        "request_s": summarize([r["seconds"] for r in results]),
    }
    if rec["tokens"]:
        rec["tokens_per_s"] = round(rec["tokens"] / wall, 1)
    return vecs, rec


def summarize(xs):
    return {"n": len(xs), "mean": round(sum(xs) / len(xs), 4) if xs else None,
            **{f"p{p}": round(percentile(xs, p), 4) if xs else None for p in (50, 90, 99)},
            "max": round(max(xs), 4) if xs else None}


def data_uri(path):
    with open(path, "rb") as f:
        raw = f.read()
    return "data:image/jpeg;base64," + base64.b64encode(raw).decode(), len(raw)


def dim_probe(side, items, base_vecs, make_body, batch):
    """Asks the server for each shorter width directly; cosine against truncating its 768 vectors."""
    out = {"768": {"norm_min": round(min(math.sqrt(sum(x * x for x in v)) for v in base_vecs), 6),
                   "norm_max": round(max(math.sqrt(sum(x * x for x in v)) for v in base_vecs), 6)}}
    for dim in DIMS[1:]:
        vecs = [v for i in range(0, len(items), batch) for v in side.post(make_body(items[i:i + batch], dim))["vecs"]]
        if len(vecs) != len(items) or any(len(v) != dim for v in vecs):
            out[str(dim)] = {"error": f"asked for {len(items)} x {dim}, got {sorted({len(v) for v in vecs})}"}
            continue
        cos = [cosine(v, truncate(b, dim)) for v, b in zip(vecs, base_vecs)]
        norms = [math.sqrt(sum(x * x for x in v)) for v in vecs]
        out[str(dim)] = {"n": len(cos), "cos_min": round(min(cos), 7), "norm_min": round(min(norms), 6),
                         "norm_max": round(max(norms), 6)}
    return out


def b64_vector(v):
    return base64.b64encode(struct.pack(f"<{len(v)}f", *v)).decode()


def save_vectors(path, vecs):
    with open(path, "wb") as f:
        for v in vecs:
            f.write(struct.pack(f"<{len(v)}f", *v))


def load_vectors(path, dim=768):
    with open(path, "rb") as f:
        raw = f.read()
    n = len(raw) // (4 * dim)
    flat = struct.unpack(f"<{n * dim}f", raw)
    return [list(flat[i * dim:(i + 1) * dim]) for i in range(n)]


def run_suite(side, a, suite):
    manifest, corpus, queries, qrels = load_suite(a.data, suite, a.max_docs, a.max_queries)
    qids, dids = [q["id"] for q in queries], [d["id"] for d in corpus]
    res = {"dataset": manifest["dataset"], "revision": manifest["revision"], "data_sha256": manifest["sha256"],
           "docs": len(corpus), "queries": len(queries), "policies": {}}
    print(f"{side.name} {suite}: {len(corpus)} docs, {len(queries)} queries", file=sys.stderr, flush=True)

    image_vecs, image_rec = None, None
    if suite == "image":
        uris = [data_uri(d["path"]) for d in corpus]
        res["image_bytes"] = {"raw_total": sum(n for _, n in uris), "raw_mean": round(sum(n for _, n in uris) / len(uris)),
                              "data_uri_total": sum(len(u) for u, _ in uris)}
        uri_list = [u for u, _ in uris]
        try:
            side.post(side.image_body(uri_list[:1], 768))
        except RuntimeError as e:  # measured, not assumed: the same image the other side embeds
            res["unsupported"] = str(e)[:400]
            print(f"  {side.name} {suite}: unsupported: {res['unsupported']}", file=sys.stderr)
            return res
        batch = 1 if side.name == "hanzo" else a.image_batch
        image_vecs, image_rec = embed_all(side, uri_list, lambda c: side.image_body(c, 768), batch,
                                          a.concurrency, f"{suite} images")
        res["inputs_sha256_docs"] = inputs_digest([u.encode() for u in uri_list])
        res["docs_embed"] = image_rec
        res["dim_probe_docs"] = dim_probe(side, uri_list[:a.probe], image_vecs[:a.probe], side.image_body, batch)

    for policy in a.policies:
        outdir = os.path.join(a.out, side.name, suite, policy)
        os.makedirs(outdir, exist_ok=True)
        pres = {}
        q_texts = [query_text(suite, policy, q) for q in queries]
        if suite == "image":
            dvecs = image_vecs
        else:
            d_texts = [doc_text(policy, d) for d in corpus]
            pres["inputs_sha256_docs"] = inputs_digest(d_texts)
            dvecs, pres["docs_embed"] = embed_all(side, d_texts, lambda c: side.text_body(c, 768), a.batch,
                                                  a.concurrency, f"{suite}/{policy} docs")
        pres["inputs_sha256_queries"] = inputs_digest(q_texts)
        qvecs, pres["queries_embed"] = embed_all(side, q_texts, lambda c: side.text_body(c, 768), a.batch,
                                                 a.concurrency, f"{suite}/{policy} queries")
        save_vectors(os.path.join(outdir, "docs.f32"), dvecs)
        save_vectors(os.path.join(outdir, "queries.f32"), qvecs)
        with open(os.path.join(outdir, "sample.json"), "w") as f:
            json.dump({k: [b64_vector(v) for v in vs[:SAMPLE]] for k, vs in (("docs", dvecs), ("queries", qvecs))}, f)

        probe_texts = q_texts[:a.probe] + ([] if suite == "image" else d_texts[:a.probe])
        probe_base = qvecs[:a.probe] + ([] if suite == "image" else dvecs[:a.probe])
        pres["dim_probe"] = dim_probe(side, probe_texts, probe_base, side.text_body, a.batch)

        if policy == a.policies[0] and a.latency:
            lat = []
            for q in q_texts[:a.latency + 3]:
                lat.append(side.post(side.text_body([q], 768))["seconds"])
            pres["query_latency_s"] = summarize(lat[3:])  # three warm-up requests dropped

        metrics = score(qvecs, dvecs, qids, dids, qrels)
        for dim, m in metrics.items():
            rankings = m.pop("_rankings")
            with open(os.path.join(outdir, f"top10_{dim}.json"), "w") as f:  # positions in the shared doc order
                json.dump([r[:10] for r in rankings], f, separators=(",", ":"))
        pres["metrics"] = metrics
        res["policies"][policy] = pres
        print(f"  {suite}/{policy}: " + ", ".join(f"{d}d nDCG@10 {m['ndcg@10']:.4f}" for d, m in metrics.items()),
              file=sys.stderr, flush=True)
    return res


def side_from_args(a):
    if a.side == "cloudflare":
        if a.url:
            url, token = a.url, os.environ.get("HANZO_API_KEY") or os.environ.get("CLOUDFLARE_API_TOKEN")
        else:
            acct = os.environ.get("CLOUDFLARE_ACCOUNT_ID")
            token = os.environ.get("CLOUDFLARE_API_TOKEN")
            if not acct or not token:
                sys.exit("cloudflare: set CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN, or --url with HANZO_API_KEY")
            url = f"https://api.cloudflare.com/client/v4/accounts/{acct}/ai/run/{CF_MODEL}"
        if not token:
            sys.exit("cloudflare: no token (CLOUDFLARE_API_TOKEN or HANZO_API_KEY)")
        return Side("cloudflare", url, token, CF_MODEL, a.timeout)
    url = a.url or "http://127.0.0.1:1234/v1/embeddings"
    return Side("hanzo", url, os.environ.get("HANZO_API_KEY"), a.model or HANZO_MODEL, a.timeout)


def cmd_run(a):
    side = side_from_args(a)
    os.makedirs(a.out, exist_ok=True)
    result = {
        "side": side.name, "url": redact_url(side.url), "model": side.model, "label": a.label,
        "started": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "client": {"python": platform.python_version(), "numpy": bool(np), "cpus": os.cpu_count(),
                   "machine": platform.machine()},
        "settings": {"batch": a.batch, "image_batch": a.image_batch, "concurrency": a.concurrency,
                     "max_docs": a.max_docs, "max_queries": a.max_queries, "probe": a.probe,
                     "latency": a.latency, "policies": a.policies, "dims": list(DIMS)},
        "suites": {},
    }
    path = os.path.join(a.out, f"{side.name}.json")
    for suite in a.suites:
        result["suites"][suite] = run_suite(side, a, suite)
        with open(path, "w") as f:
            json.dump(result, f, indent=1)
    result["finished"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    with open(path, "w") as f:
        json.dump(result, f, indent=1)
    print(path)


# ── compare: agreement, deltas, full cost ───────────────────────────────────────────────────────


def side_vectors(out, side, suite, policy, which):
    """Every vector a run kept, or its committed sample when the full file is not on this machine."""
    d = os.path.join(out, side, suite, policy)
    if os.path.exists(os.path.join(d, f"{which}.f32")):
        return load_vectors(os.path.join(d, f"{which}.f32"))
    with open(os.path.join(d, "sample.json")) as f:
        return [decode_embedding(v) for v in json.load(f)[which]]


def agreement(out, suite, policy, which):
    a = side_vectors(out, "hanzo", suite, policy, which)
    b = side_vectors(out, "cloudflare", suite, policy, which)
    cos = sorted(cosine(x, y) for x, y in zip(a, b))
    return {"n": len(cos), "mean": round(sum(cos) / len(cos), 7), "p1": round(percentile(cos, 1), 7),
            "min": round(cos[0], 7)}


def top10_overlap(out, suite, policy, dim):
    def load(side):
        with open(os.path.join(out, side, suite, policy, f"top10_{dim}.json")) as f:
            return json.load(f)
    a, b = load("hanzo"), load("cloudflare")
    return round(sum(len(set(x) & set(y)) / 10 for x, y in zip(a, b)) / len(a), 4)


def price(a, key):
    return a.price.get(key, PRICES[key][0])


def mean_tokens(rec):
    return (rec.get("tokens") or 0) / rec["inputs"] if rec and rec.get("inputs") else 0.0


def cost_model(a, sides):
    """Full cost for --docs documents and --queries-month queries a month, from measured tokens and bytes.

    Inference does not depend on the width (one forward either way); storage and search do. A rebuild is one
    full re-embed of the corpus: a new prefix policy or model revision is a new space and needs one, a
    narrower width does not when the 768 vectors are kept (the probe shows truncation is what the server
    returns)."""
    infer, store = [], []
    for suite in SUITE_NAMES:
        runs = [s["suites"][suite] for s in sides.values() if suite in s.get("suites", {})]
        src = next((r for r in runs if not r.get("unsupported")), None)
        estimated = False
        if src:
            pol = next(iter(src["policies"].values()), {})
            doc_tokens = mean_tokens(src.get("docs_embed") or pol.get("docs_embed"))
            q_tokens = mean_tokens(pol.get("queries_embed"))
        elif any(r.get("image_bytes") for r in runs):  # nobody embedded the images: the model page's count
            src = next(r for r in runs if r.get("image_bytes"))
            doc_tokens, q_tokens, estimated = price(a, "cf_tokens_per_image"), 0.0, True
        else:
            continue
        n, nq = a.docs, a.queries_month
        owned_ok = not any(s["suites"].get(suite, {}).get("unsupported") for s in sides.values()
                           if s["side"] == "hanzo")
        hours = lambda tokens: tokens / price(a, "replica_tokens_per_s") / 3600  # noqa: E731
        row = {"suite": suite, "estimated": estimated, "doc_tokens": round(doc_tokens, 1), "query_tokens": round(q_tokens, 1),
               "rebuild_cf": round(n * doc_tokens / 1e6 * price(a, "cf_per_m_tokens"), 2),
               "queries_cf": round(nq * q_tokens / 1e6 * price(a, "cf_per_m_tokens"), 4)}
        if owned_ok:
            row["rebuild_owned_replica_h"] = round(hours(n * doc_tokens), 1)
            row["rebuild_owned"] = round(hours(n * doc_tokens) * price(a, "replica_hour"), 2)
            row["queries_owned_replica_h"] = round(hours(nq * q_tokens), 2)
            row["queries_owned"] = round(hours(nq * q_tokens) * price(a, "replica_hour"), 2)
        if src.get("image_bytes"):
            per_doc = src["image_bytes"]["data_uri_total"] / src["docs"]
            row["media_gb_per_rebuild"] = round(n * per_doc / 1e9, 1)
            row["media_s3_to_cf"] = round(n * per_doc / 1e9 * price(a, "s3_egress_per_gb"), 2)
        cf = sides.get("cloudflare", {}).get("suites", {}).get(suite)
        cf_emb = cf and not cf.get("unsupported") and (cf.get("docs_embed") or next(iter(cf["policies"].values()))["docs_embed"])
        if cf_emb:
            row["rebuild_cf_hours_at_measured_rate"] = round(n / cf_emb["inputs_per_s"] / 3600, 1)
        infer.append(row)
    n, nq = a.docs, a.queries_month
    for dim in DIMS:  # storage and search depend on the count and the width, not on what was embedded
        stored, queried = n * dim, (nq + n) * dim
        vectorize = (max(0, queried - price(a, "vectorize_queried_included")) / 1e6
                     * price(a, "vectorize_queried_per_m_dims")
                     + max(0, stored - price(a, "vectorize_stored_included")) / 1e8
                     * price(a, "vectorize_stored_per_100m_dims"))
        gb = n * dim * 4 / 1e9
        store.append({"dim": dim, "vectors_gb": round(gb, 3), "vectorize": round(vectorize, 2),
                      "r2_vectors": round(gb * price(a, "r2_per_gb_month"), 3),
                      # an HNSW graph is about the size of the vectors it links
                      "owned_store": round(2 * gb * price(a, "gp3_per_gb_month"), 2)})
    return {"inference": infer, "storage": store,
            # two replicas, 2 of a 4-core node's CPUs requested each (universe values/hanzo/engine.yaml)
            "owned_floor_month": round(2 * 2 / 4 * 730 * price(a, "replica_hour"), 2)}


def fmt(x, nd=4):
    return "–" if x is None else (f"{x:.{nd}f}" if isinstance(x, float) else str(x))


def cmd_compare(a):
    sides = {}
    for name in ("hanzo", "cloudflare"):
        p = os.path.join(a.out, f"{name}.json")
        if os.path.exists(p):
            with open(p) as f:
                sides[name] = json.load(f)
    if not sides:
        sys.exit(f"no hanzo.json or cloudflare.json in {a.out}")
    lines = ["# EmbeddingGemma 2: Hanzo Engine vs Cloudflare Workers AI", ""]
    for name, s in sides.items():
        lines.append(f"- **{name}**: `{s['url']}` model `{s['model']}`, {s.get('label') or ''} "
                     f"({s['started']}; batch {s['settings']['batch']}, concurrency {s['settings']['concurrency']})")
    both = len(sides) == 2
    report = {"sides": list(sides), "suites": {}}
    for suite in SUITE_NAMES:
        per = {n: s["suites"].get(suite) for n, s in sides.items() if suite in s.get("suites", {})}
        if not per:
            continue
        lines += ["", f"## {suite}"]
        first = next(iter(per.values()))
        lines.append(f"{first['dataset']}@{first['revision'][:12]}: {first['docs']} documents, {first['queries']} queries.")
        for n, r in per.items():
            if r.get("unsupported"):
                lines.append(f"- **{n}: not supported** — `{r['unsupported'][:200]}`")
        live = {n: r for n, r in per.items() if not r.get("unsupported")}
        if not live:
            continue
        if both and len(live) == 2:
            for key in ("docs", "queries", "data_sha256"):
                if live["hanzo"][key] != live["cloudflare"][key]:
                    sys.exit(f"{suite}: sides ran different data ({key}); rerun with the same --max-docs/--max-queries")
        policies = sorted({p for r in live.values() for p in r["policies"]}, key=POLICIES.index)
        metric_keys = ("recall@1", "recall@5", "recall@10", "ndcg@10") if suite == "image" else \
            ("ndcg@10", "recall@10", "recall@100", "mrr@10")
        lines += ["", "| policy | dim | " + " | ".join(f"{n} {m}" for m in metric_keys for n in live) + " |",
                  "|---|---|" + "---|" * (len(metric_keys) * len(live))]
        for policy in policies:
            for dim in DIMS:
                cells = []
                for m in metric_keys:
                    for n in live:
                        cells.append(fmt(live[n]["policies"].get(policy, {}).get("metrics", {}).get(str(dim), {}).get(m)))
                lines.append(f"| {policy} | {dim} | " + " | ".join(cells) + " |")
        if both and len(live) == 2:
            lines += ["", "Vector agreement (cosine between the two sides' vectors for the same input, 768d) and "
                      "top-10 overlap per width:", "", "| policy | docs mean / min | queries mean / min | "
                      + " | ".join(f"top-10 {d}d" for d in DIMS) + " |", "|---|---|---|" + "---|" * len(DIMS)]
            for policy in policies:
                if any(policy not in r["policies"] for r in live.values()):
                    continue
                for key in ("inputs_sha256_queries",) + (() if suite == "image" else ("inputs_sha256_docs",)):
                    if live["hanzo"]["policies"][policy][key] != live["cloudflare"]["policies"][policy][key]:
                        sys.exit(f"{suite}/{policy}: the sides sent different inputs ({key})")
                da = agreement(a.out, suite, policy, "docs")
                qa = agreement(a.out, suite, policy, "queries")
                ov = [top10_overlap(a.out, suite, policy, d) for d in DIMS]
                report["suites"].setdefault(suite, {})[policy] = {"docs": da, "queries": qa, "top10_overlap": ov}
                lines.append(f"| {policy} | {da['mean']:.6f} / {da['min']:.6f} | {qa['mean']:.6f} / {qa['min']:.6f} | "
                             + " | ".join(f"{x:.3f}" for x in ov) + " |")
        lines += ["", "Throughput and latency (first policy; documents embedded at 768):", "",
                  "| side | docs/s | tokens/s | doc tokens | bytes sent | query p50 s | p90 s | p99 s | dims probe min cos |",
                  "|---|---|---|---|---|---|---|---|---|"]
        for n, r in live.items():
            pol = r["policies"][policies[0]]
            emb = r.get("docs_embed") or pol.get("docs_embed") or {}
            lat = pol.get("query_latency_s") or {}
            probe = pol.get("dim_probe") or {}
            pmin = min((v.get("cos_min", 1.0) for k, v in probe.items() if k != "768" and "cos_min" in v), default=None)
            lines.append(f"| {n} | {fmt(emb.get('inputs_per_s'), 2)} | {fmt(emb.get('tokens_per_s'), 1)} | "
                         f"{fmt(emb.get('tokens'))} | {fmt(emb.get('bytes_sent'))} | {fmt(lat.get('p50'))} | "
                         f"{fmt(lat.get('p90'))} | {fmt(lat.get('p99'))} | {fmt(pmin, 7)} |")
    cost = cost_model(a, sides)
    report["cost"] = cost
    if cost["inference"]:
        lines += ["", f"## Full cost, USD: {a.docs:,} documents, {a.queries_month:,} queries a month", "",
                  "Inference (width-independent). Owned = CPU replica-hours at the production rate.", "",
                  "| suite | tokens/doc | tokens/query | rebuild: Workers AI | rebuild: owned (replica-h) | "
                  "rebuild: media S3 to CF (GB) | queries/month: Workers AI | queries/month: owned (replica-h) |",
                  "|---|---|---|---|---|---|---|---|"]
        for r in cost["inference"]:
            owned_r = f"{r['rebuild_owned']:.2f} ({r['rebuild_owned_replica_h']})" if "rebuild_owned" in r else "not supported"
            owned_q = f"{r['queries_owned']:.2f} ({r['queries_owned_replica_h']})" if "queries_owned" in r else "not supported"
            media = f"{r['media_s3_to_cf']:.2f} ({r['media_gb_per_rebuild']})" if "media_s3_to_cf" in r else "0"
            queries_cf = "-" if r["estimated"] else f"{r['queries_cf']:.4f}"
            lines.append(f"| {r['suite']}{' (est.)' if r['estimated'] else ''} | {r['doc_tokens']} | {r['query_tokens'] or '-'} | "
                         f"{r['rebuild_cf']:.2f} | {owned_r} | {media} | {queries_cf} | {owned_q} |")
        lines += ["", f"Owned floor: two always-on replicas at 2 CPUs requested each, {cost['owned_floor_month']:.2f} a "
                  "month whatever the load (shared with the other embedding model in the same process).", "",
                  "Storage and search, a month:", "",
                  "| dim | vectors GB | Vectorize (stored + queried dims) | vectors in R2 | owned store (gp3, x2 for the graph) |",
                  "|---|---|---|---|---|"]
        for r in cost["storage"]:
            lines.append(f"| {r['dim']} | {r['vectors_gb']} | {r['vectorize']:.2f} | {r['r2_vectors']:.3f} | "
                         f"{r['owned_store']:.2f} |")
        lines += ["", "Prices:"] + [f"- `{k}` = {price(a, k)}: {PRICES[k][1]}" for k in PRICES]
    with open(os.path.join(a.out, "report.md"), "w") as f:
        f.write("\n".join(lines) + "\n")
    with open(os.path.join(a.out, "compare.json"), "w") as f:
        json.dump(report, f, indent=1)
    print("\n".join(lines))


def parse_prices(items):
    out = {}
    for it in items or []:
        k, _, v = it.partition("=")
        if k not in PRICES:
            sys.exit(f"--price: unknown key {k!r}; known: {', '.join(PRICES)}")
        out[k] = float(v)
    return out


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    csv_list = lambda allowed: (lambda s: [x for x in s.split(",") if x and (x in allowed or sys.exit(f"unknown {x!r}"))])  # noqa: E731

    pp = sub.add_parser("prepare")
    pp.add_argument("--data", required=True)
    pp.add_argument("--suites", type=csv_list(SUITE_NAMES), default=list(SUITE_NAMES))
    pp.add_argument("--allow-drift", action="store_true")

    pr = sub.add_parser("run")
    pr.add_argument("--side", choices=("hanzo", "cloudflare"), required=True)
    pr.add_argument("--url")
    pr.add_argument("--model")
    pr.add_argument("--data", required=True)
    pr.add_argument("--out", required=True)
    pr.add_argument("--label", default="", help="free text recorded with the run, e.g. the engine version")
    pr.add_argument("--suites", type=csv_list(SUITE_NAMES), default=list(SUITE_NAMES))
    pr.add_argument("--policies", type=csv_list(POLICIES), default=list(POLICIES))
    pr.add_argument("--max-docs", type=int, default=0, help="keep every judged doc plus a seeded sample, up to N")
    pr.add_argument("--max-queries", type=int, default=0)
    pr.add_argument("--batch", type=int, default=32)
    pr.add_argument("--image-batch", type=int, default=8)
    pr.add_argument("--concurrency", type=int, default=4)
    pr.add_argument("--probe", type=int, default=16, help="inputs per kind in the dimensions probe")
    pr.add_argument("--latency", type=int, default=50, help="sequential one-query requests timed")
    pr.add_argument("--timeout", type=float, default=3600)

    pc = sub.add_parser("compare")
    pc.add_argument("--out", required=True)
    pc.add_argument("--docs", type=int, default=1_000_000)
    pc.add_argument("--queries-month", type=int, default=1_000_000)
    pc.add_argument("--price", action="append", help="override a price: key=value")

    a = p.parse_args(argv)
    if a.cmd == "prepare":
        cmd_prepare(a)
    elif a.cmd == "run":
        cmd_run(a)
    else:
        a.price = parse_prices(a.price)
        cmd_compare(a)


if __name__ == "__main__":
    main()
