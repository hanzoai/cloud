#!/usr/bin/env python3
"""embed_bench.py: its metrics, its wire handling, and a whole run against two fake servers.

The fakes embed deterministically (hashed character trigrams, 768 wide), honour `dimensions` and
`encoding_format` the way both real servers document, and differ where the real ones do: Cloudflare wraps
the body in {"result", "success"} and takes images as `messages`; the engine takes text only.

  python3 docs/research/embeddinggemma2-workers-ai/embed_bench_test.py
"""
import base64
import hashlib
import http.server
import io
import json
import math
import os
import shutil
import socketserver
import struct
import sys
import tempfile
import threading
import unittest
from contextlib import redirect_stderr, redirect_stdout

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import embed_bench as eb  # noqa: E402


def fake_vector(item):
    v = [0.0] * 768
    s = item if isinstance(item, str) else json.dumps(item, sort_keys=True)
    s = s.split("query: ")[-1].split("text: ")[-1].lower()
    for i in range(len(s) - 2):
        h = hashlib.sha256(s[i:i + 3].encode()).digest()
        sign = 1.0 if h[2] & 1 else -1.0
        v[int.from_bytes(h[:2], "little") % 768] += sign
        v[h[3] % 128] += sign / 2  # every width keeps a share, so a truncation is never all zero
    return eb.l2_normalize(v) if any(v) else [1.0] + [0.0] * 767


class Fake(http.server.BaseHTTPRequestHandler):
    side = "hanzo"
    seen = []

    def log_message(self, *a):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        Fake.seen.append(body)
        if "messages" in body:
            if self.side == "hanzo":
                return self.reply(422, {"error": "missing field `input`"})
            items = [json.dumps(conv) for conv in body["messages"]]
        elif isinstance(body["input"], dict):  # what engine 079edc61 answers an image for this model
            return self.reply(200, {"object": "list", "usage": {"prompt_tokens": 1, "total_tokens": 1},
                                    "data": [{"object": "embedding", "index": 0, "embedding": [0.0] * 768}]})
        else:
            items = body["input"] if isinstance(body["input"], list) else [body["input"]]
        dim = body.get("dimensions", 768)
        data = []
        for i, it in enumerate(items):
            v = eb.truncate(fake_vector(it), dim)
            if body.get("encoding_format") == "base64":
                v = base64.b64encode(struct.pack(f"<{dim}f", *v)).decode()
            data.append({"object": "embedding", "index": i, "embedding": v})
        out = {"object": "list", "data": list(reversed(data)),
               "usage": {"prompt_tokens": sum(len(str(x)) // 4 + 1 for x in items)}}
        if self.side == "cloudflare":
            out = {"result": out, "success": True, "errors": [], "messages": []}
        self.reply(200, out)

    def reply(self, code, obj):
        raw = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


class Server(http.server.ThreadingHTTPServer):
    def server_bind(self):  # HTTPServer's own asks reverse DNS (getfqdn), which can take seconds
        socketserver.TCPServer.server_bind(self)
        self.server_name, self.server_port = "127.0.0.1", self.server_address[1]


def serve(side):
    handler = type(f"Fake_{side}", (Fake,), {"side": side})
    srv = Server(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}/v1/embeddings"


class Pure(unittest.TestCase):
    def test_truncate_renormalizes(self):
        v = eb.truncate([3.0, 4.0, 12.0], 2)
        self.assertAlmostEqual(v[0], 0.6)
        self.assertAlmostEqual(v[1], 0.8)

    def test_metrics(self):
        rels = {"a": 1, "b": 1}
        self.assertEqual(eb.recall_at(["a", "x", "b"], rels, 1), 0.5)
        self.assertEqual(eb.recall_at(["a", "x", "b"], rels, 3), 1.0)
        self.assertEqual(eb.mrr_at(["x", "b"], rels, 10), 0.5)
        ideal = 1 + 1 / math.log2(3)
        self.assertAlmostEqual(eb.ndcg_at(["a", "x", "b"], rels, 10), (1 + 1 / 2) / ideal)
        self.assertEqual(eb.ndcg_at(["x"], rels, 10), 0.0)

    def test_decode_base64_and_floats(self):
        raw = base64.b64encode(struct.pack("<3f", 1.0, -2.0, 0.5)).decode()
        self.assertEqual(eb.decode_embedding(raw), [1.0, -2.0, 0.5])
        self.assertEqual(eb.decode_embedding([1, 2]), [1.0, 2.0])

    def test_parse_both_shapes(self):
        body = {"data": [{"index": 1, "embedding": [2.0]}, {"index": 0, "embedding": [1.0]}],
                "usage": {"prompt_tokens": 7}}
        self.assertEqual(eb.parse_response(body), ([[1.0], [2.0]], 7))
        self.assertEqual(eb.parse_response({"result": body, "success": True}), ([[1.0], [2.0]], 7))
        with self.assertRaises(ValueError):
            eb.parse_response({"result": {}, "success": False, "errors": ["no"]})

    def test_policies_write_the_model_card_prompts(self):
        doc = {"title": "", "text": "def f(): pass"}
        self.assertEqual(eb.doc_text("task", doc), "title: none | text: def f(): pass")
        self.assertEqual(eb.doc_text("none", doc), "def f(): pass")
        self.assertEqual(eb.doc_text("task", {"title": "T", "text": "x"}), "title: T | text: x")
        self.assertEqual(eb.doc_text("none", {"title": "T", "text": "x"}), "T x")
        self.assertEqual(eb.query_text("text", "task", {"text": "q"}), "task: fact checking | query: q")
        self.assertEqual(eb.query_text("code", "none", {"text": "q"}), "q")

    def test_subsample_keeps_judged_docs_and_is_seeded(self):
        corpus = [{"id": str(i)} for i in range(100)]
        qrels = {"q": {"7": 1, "93": 1}}
        a = eb.subsample(corpus, qrels, ["q"], 10)
        self.assertEqual(len(a), 10)
        self.assertTrue({"7", "93"} <= {d["id"] for d in a})
        self.assertEqual(a, eb.subsample(corpus, qrels, ["q"], 10))
        self.assertEqual(eb.subsample(corpus, qrels, ["q"], 0), corpus)

    def test_inputs_digest_is_order_and_boundary_sensitive(self):
        self.assertNotEqual(eb.inputs_digest(["ab", "c"]), eb.inputs_digest(["a", "bc"]))
        self.assertNotEqual(eb.inputs_digest(["a", "b"]), eb.inputs_digest(["b", "a"]))

    def test_rank_pure_python_matches_numpy(self):
        Q = [eb.l2_normalize([1, 0, 1]), eb.l2_normalize([0, 1, 0])]
        D = [eb.l2_normalize(v) for v in ([1, 0, 0], [0, 1, 0.1], [1, 0, 1], [0, 0, 1])]
        np_saved = eb.np
        try:
            eb.np = None
            pure = eb.rank(Q, D, 3)
        finally:
            eb.np = np_saved
        self.assertEqual(pure[0][0], 2)
        self.assertEqual(pure[1][0], 1)
        if eb.np is not None:
            self.assertEqual(eb.rank(Q, D, 3), pure)

    def test_redact_url(self):
        self.assertEqual(eb.redact_url("https://api.cloudflare.com/client/v4/accounts/abc123/ai/run/@cf/x"),
                         "https://api.cloudflare.com/client/v4/accounts/{account}/ai/run/@cf/x")


def write_suite(root, suite, corpus, queries, qrels):
    d = os.path.join(root, suite)
    os.makedirs(d, exist_ok=True)
    eb.write_jsonl(os.path.join(d, "corpus.jsonl"), corpus)
    eb.write_jsonl(os.path.join(d, "queries.jsonl"), queries)
    eb.write_jsonl(os.path.join(d, "qrels.jsonl"), qrels)
    with open(os.path.join(d, "manifest.json"), "w") as f:
        json.dump({"suite": suite, **eb.SUITES[suite], "sha256": eb.suite_digests(d, suite)}, f)


class EndToEnd(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        root = cls.tmp.name
        words = ["solar flare", "protein folding", "binary search", "hash map", "neural network",
                 "photosynthesis", "black hole", "garbage collector", "vaccine trial", "plate tectonics"]
        corpus = [{"id": f"d{i}", "title": w.title(), "text": f"An essay about {w} and nothing else."}
                  for i, w in enumerate(words)]
        queries = [{"id": f"q{i}", "text": f"what is {w}"} for i, w in enumerate(words)]
        qrels = [{"qid": f"q{i}", "did": f"d{i}", "score": 1} for i in range(len(words))]
        write_suite(root, "text", corpus, queries, qrels)
        img_dir = os.path.join(root, "image", "images")
        os.makedirs(img_dir)
        icorpus = []
        for i, w in enumerate(words[:4]):
            with open(os.path.join(img_dir, f"{i}.jpg"), "wb") as f:
                f.write(f"jpeg bytes of {w}".encode())
            icorpus.append({"id": f"{i}.jpg", "file": f"images/{i}.jpg"})
        write_suite(root, "image", icorpus, [{"id": f"{i}.jpg#0", "text": w} for i, w in enumerate(words[:4])],
                    [{"qid": f"{i}.jpg#0", "did": f"{i}.jpg", "score": 1} for i in range(4)])
        cls.data, cls.out = root, os.path.join(root, "out")
        cls.servers = {}
        for side in ("hanzo", "cloudflare"):
            srv, url = serve(side)
            cls.servers[side] = srv
            os.environ["CLOUDFLARE_API_TOKEN"] = "test-token"
            with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
                eb.main(["run", "--side", side, "--url", url, "--data", root, "--out", cls.out,
                         "--suites", "text,image", "--batch", "4", "--probe", "3", "--latency", "2"])

    @classmethod
    def tearDownClass(cls):
        for srv in cls.servers.values():
            srv.shutdown()
            srv.server_close()
        cls.tmp.cleanup()

    def load(self, side):
        with open(os.path.join(self.out, f"{side}.json")) as f:
            return json.load(f)

    def test_each_side_scores_every_policy_and_width(self):
        for side in ("hanzo", "cloudflare"):
            text = self.load(side)["suites"]["text"]
            self.assertEqual(sorted(text["policies"]), ["none", "task"])
            for pol in text["policies"].values():
                self.assertEqual(sorted(pol["metrics"], key=int), ["128", "256", "512", "768"])
                self.assertGreater(pol["metrics"]["768"]["recall@10"], 0.9)
                self.assertEqual(pol["metrics"]["768"]["queries"], 10)

    def test_dimension_probe_matches_truncation(self):
        probe = self.load("cloudflare")["suites"]["text"]["policies"]["task"]["dim_probe"]
        for dim in ("512", "256", "128"):
            self.assertGreater(probe[dim]["cos_min"], 0.99999)
            self.assertAlmostEqual(probe[dim]["norm_min"], 1.0, places=5)

    def test_images_measured_on_cloudflare_and_refused_by_the_engine(self):
        self.assertIn("all-zero vector", self.load("hanzo")["suites"]["image"]["unsupported"])
        img = self.load("cloudflare")["suites"]["image"]
        self.assertNotIn("unsupported", img)
        self.assertEqual(img["docs_embed"]["inputs"], 4)
        self.assertEqual(img["policies"]["task"]["metrics"]["768"]["queries"], 4)

    def test_cloudflare_never_gets_a_model_field_and_both_send_the_same_text(self):
        cf = [b for b in Fake.seen if "model" not in b]
        self.assertTrue(cf)
        h, c = self.load("hanzo")["suites"]["text"], self.load("cloudflare")["suites"]["text"]
        for pol in ("task", "none"):
            for key in ("inputs_sha256_docs", "inputs_sha256_queries"):
                self.assertEqual(h["policies"][pol][key], c["policies"][pol][key])

    def test_compare_agrees_and_costs(self):
        buf = io.StringIO()
        with redirect_stdout(buf):
            eb.main(["compare", "--out", self.out, "--docs", "1000000", "--queries-month", "1000000"])
        with open(os.path.join(self.out, "compare.json")) as f:
            rep = json.load(f)
        for pol in ("task", "none"):
            self.assertGreater(rep["suites"]["text"][pol]["docs"]["min"], 0.99999)
            self.assertEqual(rep["suites"]["text"][pol]["top10_overlap"], [1.0, 1.0, 1.0, 1.0])
        infer = {r["suite"]: r for r in rep["cost"]["inference"]}
        self.assertIn("rebuild_owned", infer["text"])
        self.assertNotIn("rebuild_owned", infer["image"])
        self.assertGreater(infer["image"]["media_s3_to_cf"], 0)
        store = rep["cost"]["storage"]
        self.assertEqual([r["dim"] for r in store], [768, 512, 256, 128])
        self.assertAlmostEqual(store[0]["vectors_gb"], 3.072)
        self.assertIn("not supported", buf.getvalue())

    def test_compare_from_the_committed_files_alone(self):
        with tempfile.TemporaryDirectory() as t:
            out = os.path.join(t, "out")
            shutil.copytree(self.out, out, ignore=shutil.ignore_patterns("*.f32"))
            with redirect_stdout(io.StringIO()):
                eb.main(["compare", "--out", out])
            with open(os.path.join(out, "compare.json")) as f:
                rep = json.load(f)
        agree = rep["suites"]["text"]["task"]
        self.assertEqual(agree["docs"]["n"], 10)
        self.assertGreater(agree["queries"]["min"], 0.99999)
        self.assertEqual(agree["top10_overlap"], [1.0, 1.0, 1.0, 1.0])

    def test_compare_with_one_side_estimates_the_images(self):
        with tempfile.TemporaryDirectory() as t:
            out = os.path.join(t, "out")
            shutil.copytree(self.out, out, ignore=shutil.ignore_patterns("cloudflare*"))
            buf = io.StringIO()
            with redirect_stdout(buf):
                eb.main(["compare", "--out", out])
            with open(os.path.join(out, "compare.json")) as f:
                infer = {r["suite"]: r for r in json.load(f)["cost"]["inference"]}
        self.assertTrue(infer["image"]["estimated"])
        self.assertEqual(infer["image"]["doc_tokens"], 260)
        self.assertGreater(infer["image"]["media_s3_to_cf"], 0)
        self.assertFalse(infer["text"]["estimated"])
        self.assertIn("image (est.)", buf.getvalue())

    def test_compare_refuses_different_inputs(self):
        path = os.path.join(self.out, "cloudflare.json")
        with open(path) as f:
            saved = f.read()
        doc = json.loads(saved)
        doc["suites"]["text"]["policies"]["task"]["inputs_sha256_queries"] = "0" * 64
        try:
            with open(path, "w") as f:
                json.dump(doc, f)
            with self.assertRaises(SystemExit), redirect_stdout(io.StringIO()):
                eb.main(["compare", "--out", self.out])
        finally:
            with open(path, "w") as f:
                f.write(saved)


if __name__ == "__main__":
    unittest.main()
