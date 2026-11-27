#!/usr/bin/env python3
"""Reproducibly build the content-free LiveBench evaluation corpus for hanzoai/cloud#59.

Downloads pinned HuggingFace LiveBench parquet revisions (Apache-2.0 repository),
verifies exact SHA-256 digests, joins objective per-model ground-truth scores for
11 models present in hanzoai/ai (conf/models.yaml + controllers/model_pricing.go),
and invokes the Go binary `ensoroutellmsim -mode extract-features` so the output
JSONL contains zero third-party prompt text and bit-identical Go FeatureVec values.
"""

import argparse
import hashlib
import json
import subprocess
import sys
import tempfile
import urllib.request
from collections import defaultdict
from pathlib import Path

PINNED_PARQUETS = {
    "model_judgment.parquet": {
        "url": "https://huggingface.co/datasets/livebench/model_judgment/resolve/9704e5da7bfb/data/leaderboard-00000-of-00001.parquet",
        "sha256": "35ad896970151776145c96b31c5ddb3a2749ea9a1d91e6b7f1a4c4c04735182a",
    },
    "coding.parquet": {
        "url": "https://huggingface.co/datasets/livebench/coding/resolve/a958549fdd8a/data/test-00000-of-00001.parquet",
        "sha256": "5f02d01fb21672f5d84169f940adab46ff3ca09b9159fd42fdd289bc9be23502",
    },
    "language.parquet": {
        "url": "https://huggingface.co/datasets/livebench/language/resolve/3ada32a2e53d/data/test-00000-of-00001.parquet",
        "sha256": "76ba142afd242ca02d6baa8bb737608d2b416674f311d8f9d798b4e3908a499c",
    },
    "instruction_following.parquet": {
        "url": "https://huggingface.co/datasets/livebench/instruction_following/resolve/0868379c4b5c/data/test-00000-of-00001.parquet",
        "sha256": "a9bb97bbaf8788142c310bcb33d50e2f6f5df8cbd8b8c3db677816b06f0f4f25",
    },
}

MODEL_MAP = {
    "mistral-small-2503": "mistral-small",
    "gpt-4o-mini-2024-07-18": "gpt-4o-mini",
    "deepseek-r1-distill-llama-70b": "deepseek-r1-distill-70b",
    "llama-3.3-70b-instruct-turbo": "llama-3.3-70b",
    "claude-3-5-haiku-20241022": "claude-3-5-haiku",
    "deepseek-v3-0324": "fireworks/deepseek-v3",
    "o3-mini-2025-01-31-medium": "o3-mini",
    "gpt-4o-2024-11-20": "gpt-4o",
    "deepseek-r1": "fireworks/deepseek-r1",
    "claude-3-7-sonnet-20250219-base": "claude-3-7-sonnet",
    "o1-2024-12-17-high": "o1",
}


def download_and_verify(cache_dir: Path) -> None:
    cache_dir.mkdir(parents=True, exist_ok=True)
    for name, spec in PINNED_PARQUETS.items():
        path = cache_dir / name
        if not path.exists():
            req = urllib.request.Request(spec["url"], headers={"User-Agent": "hanzo-cloud-eval/1.0"})
            with urllib.request.urlopen(req, timeout=60) as resp:
                path.write_bytes(resp.read())
        got = hashlib.sha256(path.read_bytes()).hexdigest()
        if got != spec["sha256"]:
            raise RuntimeError(f"SHA-256 mismatch for {name}: got {got}, want {spec['sha256']}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Build content-free LiveBench evaluation JSONL.")
    parser.add_argument("--cache-dir", required=True, help="Directory for cached LiveBench parquets")
    parser.add_argument("--out", required=True, help="Output content-free features JSONL path")
    parser.add_argument("--go-bin", default="go", help="Go toolchain binary")
    args = parser.parse_args()

    try:
        import pyarrow.parquet as pq
    except ImportError as exc:
        print("pyarrow is required: pip install pyarrow", file=sys.stderr)
        return 2

    cache_dir = Path(args.cache_dir)
    download_and_verify(cache_dir)

    mj = pq.read_table(cache_dir / "model_judgment.parquet").to_pylist()
    q_meta = {}
    for fname in ("coding.parquet", "language.parquet", "instruction_following.parquet"):
        for r in pq.read_table(cache_dir / fname).to_pylist():
            turns = r.get("turns") or []
            q_meta[r["question_id"]] = {
                "category": r.get("category", ""),
                "task": r.get("task", ""),
                "prompt": turns[0] if turns else "",
            }

    scores_by_q = defaultdict(dict)
    for row in mj:
        m = row["model"]
        if m in MODEL_MAP:
            scores_by_q[row["question_id"]][MODEL_MAP[m]] = float(row["score"])

    complete = sorted(q for q, s in scores_by_q.items() if len(s) == len(MODEL_MAP) and q in q_meta)
    with tempfile.NamedTemporaryFile("w", suffix=".jsonl", encoding="utf-8", delete=False) as tmp:
        raw_path = Path(tmp.name)
        for q in complete:
            meta = q_meta[q]
            rec = {
                "query_id": q,
                "dataset": "livebench",
                "category": meta["category"],
                "task": meta["task"],
                "prompt": meta["prompt"],
                "est_input_tokens": max(1, (len(meta["prompt"].encode("utf-8")) + 3) // 4),
                "est_output_tokens": 512 if meta["category"] == "coding" else 256,
                "arm_outcomes": {k: round(scores_by_q[q][k], 6) for k in sorted(scores_by_q[q])},
            }
            tmp.write(json.dumps(rec, ensure_ascii=False) + "\n")

    repo_root = Path(__file__).resolve().parents[3]
    try:
        subprocess.run(
            [args.go_bin, "run", "./research/ensoroutellmsim", "-mode", "extract-features", "-in", str(raw_path), "-out", str(Path(args.out).resolve())],
            cwd=str(repo_root),
            check=True,
        )
    finally:
        raw_path.unlink(missing_ok=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
