#!/usr/bin/env python3
"""Split the published OpenAPI document into one spec per service head.

The whole document is 3,663,758 code points. snakeyaml — which every
JVM-family generator loads a spec through — refuses anything past 3,000,000,
so Java and Kotlin clients cannot be generated from it at all. Splitting is
not a packaging preference; it is what makes those two languages possible.

A head is the segment after /v1 (/v1/iam/... -> iam), which is already how the
API is organised, so the split follows a boundary that exists rather than
inventing one. Each spec carries the transitive closure of the schemas its own
paths reference — a subset that dropped a $ref would generate a client that
does not compile, which is worse than one that is merely large.

Largest head after the split is o11y at ~602KB. All 199 clear the ceiling.

    python3 scripts/split-openapi.py https://api.hanzo.ai/v1/openapi.json out/
"""
import json, sys, pathlib, urllib.request, collections

CEILING = 3_000_000  # snakeyaml's hard limit, in code points


def head_of(path: str) -> str:
    """The service a path belongs to: the segment after /v1.

    Two shapes do not name a service and would otherwise produce a file that
    cannot be written or a spec nobody wants. A bare "/" or "/v1" yields the
    empty string, which writes to ".json" -- a hidden file that silently
    replaced whichever head sorted next to it. And a templated first segment
    ("/v1/{org}/...") is a PARAMETER, not a service; grouping by it invents a
    package named after a variable. Both fall back to "root".
    """
    parts = [p for p in path.strip("/").split("/") if p]
    head = parts[1] if parts and parts[0] == "v1" and len(parts) > 1 else (parts[0] if parts else "")
    if not head or head.startswith("{"):
        return "root"
    return head


def collect_refs(node, into: set) -> None:
    """Every schema name reachable from node, one level."""
    if isinstance(node, dict):
        ref = node.get("$ref")
        if isinstance(ref, str) and ref.startswith("#/components/schemas/"):
            into.add(ref.rsplit("/", 1)[1])
        for value in node.values():
            collect_refs(value, into)
    elif isinstance(node, list):
        for value in node:
            collect_refs(value, into)


def closure(seeds: set, schemas: dict) -> set:
    """Follow $refs until nothing new appears — schemas reference each other."""
    seen, pending = set(), set(seeds)
    while pending - seen:
        for name in list(pending - seen):
            seen.add(name)
            if name in schemas:
                collect_refs(schemas[name], pending)
    return {n for n in seen if n in schemas}


def main() -> int:
    src = sys.argv[1] if len(sys.argv) > 1 else "https://api.hanzo.ai/v1/openapi.json"
    out = pathlib.Path(sys.argv[2] if len(sys.argv) > 2 else "out")
    raw = (urllib.request.urlopen(src, timeout=60).read()
           if src.startswith("http") else pathlib.Path(src).read_bytes())
    doc = json.loads(raw)

    whole = len(raw.decode())
    print(f"source {whole:,} code points"
          f"{'  OVER the ceiling — this is why we split' if whole > CEILING else ''}")

    schemas = doc.get("components", {}).get("schemas", {})
    groups = collections.defaultdict(dict)
    for path, item in doc.get("paths", {}).items():
        groups[head_of(path)][path] = item

    out.mkdir(parents=True, exist_ok=True)
    over = []
    for head, paths in sorted(groups.items()):
        seeds = set()
        collect_refs(paths, seeds)
        kept = closure(seeds, schemas)
        spec = {
            "openapi": doc.get("openapi", "3.1.0"),
            "info": {
                "title": f"Hanzo {head}",
                "version": doc.get("info", {}).get("version", "1"),
            },
            "servers": doc.get("servers", [{"url": "https://api.hanzo.ai"}]),
            "paths": paths,
            "components": {"schemas": {k: schemas[k] for k in sorted(kept)}},
        }
        text = json.dumps(spec, indent=2, sort_keys=True)
        (out / f"{head}.json").write_text(text)
        if len(text) > CEILING:
            over.append((head, len(text)))

    print(f"wrote {len(groups)} specs to {out}/")
    if over:
        # A head that alone exceeds the ceiling needs splitting further; say so
        # rather than leaving a spec that silently fails in the generator.
        for head, size in over:
            print(f"  STILL OVER: {head} {size:,}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
