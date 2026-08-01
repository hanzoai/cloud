#!/usr/bin/env python3
"""Does the DEPLOYED api answer every address this document publishes?

The document is a build-time weave of each app's own projection of its own
router (plugin/embed.go). `surface-check` proves that weave equals the source.
Neither proves the thing a caller actually needs: that the address is reachable
in production. Three things break that and nothing else checks any of them —

  a stale subset      the binary serves /v1/billing/gpu/eligibility and
                      publishes /v1/billing/gpu-eligibility, from ONE build
  a missing mount     manifest/apps.go is a third, hand-maintained source of
                      truth and must be a superset of what each app registers
  an edge interceptor a worker in front of the origin answering /v1/models*
                      and /v1/pricing* that no repo in this fleet can see

so this asks the deployed host, once per release, and refuses a NEW dark
address.

404 IS DARK, WHOEVER SAYS IT. The question is not "did the origin's router
miss" — it is "can a caller reach the address this document publishes", and an
edge worker that intercepts the prefix and 404s makes the address exactly as
uncallable as a missing route. The responder is reported (`server:`) because it
names the OWNER to fix, but it never changes the verdict.

WHAT IS PROBED, and why the shape of the probe is the measurement. Only LITERAL
(param-free) path keys, and only GET/HEAD. A parameterised path probed with a
made-up value returns a correct resource-404 from a working handler — that
mistake produced five "dead routes" that were all fine — and a POST with no body
either mutates or 400s, so neither answers the routing question. 401 and 403 are
proof the route exists: authorization ran, which means routing reached a
handler. So it needs NO credential: a token would only change which of
{200,401,403} comes back, and none of that is the question.

  reach.py <openapi.yaml> <base-url> <ratchet-file>

THE RATCHET. Every line in the ratchet file is `METHOD /path`: an address this
document publishes that production does not route, each one a named defect owned
by someone. The file may only SHRINK. A 404 that is not in it fails the release;
a line in it that now answers is a line to delete. That is what keeps a defect
being closed from blocking every release while it is open, without letting a new
one in — the same shape as the wildcard floor, and unlike an allowlist it names
what is wrong instead of hiding that anything is.
"""

import concurrent.futures as futures
import sys
import urllib.error
import urllib.request

TIMEOUT = 20


def literal_ops(doc):
    """(METHOD, path) for every path key with no {param} and no wildcard."""
    out = []
    for path, item in (doc.get("paths") or {}).items():
        if "{" in path or not path.startswith("/"):
            continue
        for method in item or {}:
            if method.lower() in ("get", "head"):
                out.append((method.upper(), path))
    return sorted(set(out))


def probe(base, method, path):
    req = urllib.request.Request(base.rstrip("/") + path, method=method)
    req.add_header("accept", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            return r.status, b"", r.headers.get("server", "")
    except urllib.error.HTTPError as e:
        return e.code, (e.read() or b"")[:200], e.headers.get("server", "")
    except Exception as e:  # DNS, TLS, timeout — not a routing answer
        return 0, str(e).encode()[:200], ""


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    spec, base, ratchet_path = sys.argv[1:]

    try:
        import yaml  # PyYAML when present (the forge runner has it)

        doc = yaml.safe_load(open(spec))
    except ImportError:
        import json

        doc = json.load(open(spec))

    ops = literal_ops(doc)
    ratchet = {
        line.strip()
        for line in open(ratchet_path)
        if line.strip() and not line.startswith("#")
    }

    dark = {}
    with futures.ThreadPoolExecutor(max_workers=16) as pool:
        jobs = {pool.submit(probe, base, m, p): (m, p) for m, p in ops}
        for job in futures.as_completed(jobs):
            m, p = jobs[job]
            code, body, server = job.result()
            if code == 404:
                dark[f"{m} {p}"] = server or "?"

    new = sorted(set(dark) - ratchet)
    healed = sorted(ratchet - set(dark))

    print(f"reach: {len(ops)} literal addresses probed against {base}")
    print(f"reach: {len(dark)} dark, {len(ratchet)} on the ratchet")

    for line in healed:
        print(f"  HEALED  {line} — delete this line from {ratchet_path}")
    for line in new:
        print(f"  DARK    {line}  (answered by {dark[line]})")

    if new:
        print(
            f"\n::error::{len(new)} address(es) this document publishes are not routed by "
            f"{base}. The document is the contract every SDK, the MCP tool list and the CLI "
            f"are generated from, so an address nothing answers is a method every client "
            f"ships and no caller can use. Fix the owner — a stale subset regenerates, a "
            f"missing prefix goes in manifest/apps.go, an edge interceptor gives the path "
            f"back to the origin that can describe it."
        )
        sys.exit(1)

    if healed:
        print(
            f"\n::error::{len(healed)} ratchet line(s) now answer. The ratchet may only "
            f"shrink, and it shrinks by being edited: delete them from {ratchet_path} and "
            f"commit. A ratchet nobody prunes becomes an allowlist."
        )
        sys.exit(1)

    print("reach: every published address is routed, and nothing is dark that was not")


if __name__ == "__main__":
    main()
