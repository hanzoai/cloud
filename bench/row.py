"""Append one {label, value} row to the file BENCH_JSON names."""
import json, os, sys

path, label, value = sys.argv[1], sys.argv[2], sys.argv[3]
rows = []
if os.path.exists(path):
    try:
        rows = json.load(open(path))
    except Exception:
        rows = []
rows.append({"label": label, "value": value})
json.dump(rows, open(path, "w"), indent=1)
