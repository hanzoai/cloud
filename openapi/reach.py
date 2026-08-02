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

WHAT IS PROBED, and why the shape of the probe is the measurement. GET and HEAD
only: a POST with no body either mutates or 400s, so it never answers the routing
question. 401 and 403 are proof the route exists — authorization ran, which means
routing reached a handler — so this needs NO credential. A token would only change
which of {200,401,403} comes back, and none of that is the question.

BOTH LITERAL AND PARAMETERISED addresses, which is new and is most of the
document: 776 of the 1208 path keys are literal, so probing only those left 432
path keys — 36% of everything published — checked by nothing at all. They were
skipped for a real reason: a parameterised path probed with a made-up value
returns a correct resource-404 from a working handler, and mistaking those for
dead routes produced five "defects" that were all fine. But a resource-404 and a
router miss are DISTINGUISHABLE, so the reason to skip them does not survive
contact with the discriminator:

  zip/fiber's router-miss body is exactly `404 page not found`. Anything else —
  JSON, or a handler's own string — proves a HANDLER RAN, which proves the route
  exists and only the made-up id did not.

So the verdict rule has two branches, because a literal address and a
parameterised one are asked the same question with different evidence available:

  literal        ANY 404 is dark. There is no made-up value to blame, and the
                 stricter rule is the one that caught the edge worker — whose
                 404 carried a JSON body and would pass a body test.
  parameterised  Only the router-miss body is dark. A handler's 404 is the route
                 working.

WILDCARDS ARE NOT PROBED. A `{wildcardN}` key is a catch-all, so filling it with
a sentinel asks about a path nothing was ever meant to serve — the answer is a
router miss by construction and says nothing about the catch-all. Measured: they
are the ONLY two "misses" a naive parameterised sweep reports.

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
import re
import sys
import urllib.error
import urllib.request

TIMEOUT = 20

# The value substituted for every {param}. Deliberately unmistakable: whatever
# comes back, a reader can tell the id was the probe's and not a real one.
SENTINEL = "zzz-reach-probe"

# zip/fiber's router-miss body, exactly. This one string is the whole reason a
# parameterised address can be probed at all.
ROUTER_MISS = b"404 page not found"


def probed_ops(doc):
    """(METHOD, published-path, url-to-probe) for every address worth asking about.

    Wildcard keys are dropped, not filled: see the module docstring.
    """
    out = []
    for path, item in (doc.get("paths") or {}).items():
        if not path.startswith("/") or "{wildcard" in path:
            continue
        for method in item or {}:
            if method.lower() in ("get", "head"):
                out.append(
                    (method.upper(), path, re.sub(r"\{[^}]+\}", SENTINEL, path))
                )
    return sorted(set(out))


def probe(base, method, url):
    req = urllib.request.Request(base.rstrip("/") + url, method=method)
    req.add_header("accept", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            return r.status, b"", r.headers.get("server", "")
    except urllib.error.HTTPError as e:
        return e.code, (e.read() or b"")[:200], e.headers.get("server", "")
    except Exception as e:  # DNS, TLS, timeout — not a routing answer
        return 0, str(e).encode()[:200], ""


def is_dark(published, code, body):
    """Did this answer prove the address is UNROUTED?

    The two branches are the docstring's, and the asymmetry is deliberate: a
    literal address has no made-up value to blame, so any 404 condemns it — the
    rule that caught an edge worker whose 404 carried a JSON body.
    """
    if code != 404:
        return False
    if "{" not in published:
        return True
    return body.strip() == ROUTER_MISS


def check_instrument():
    """Prove the oracle before trusting it. Runs on every real run, costs nothing.

    The whole gate now rests on one string, so the string gets a test — and it
    lives HERE rather than in a test file nobody runs, because a measuring
    instrument that is not checked at the moment of measuring is an instrument
    nobody checks. The case that matters most is row 2: an edge worker's 404
    carried a JSON body, and a body-only rule would have called it healthy.
    """
    doc = {"paths": {"/v1/lit": {"get": {}}, "/v1/x/{wildcard1}": {"get": {}}}}
    assert probed_ops(doc) == [("GET", "/v1/lit", "/v1/lit")], "wildcards must drop"

    handler_404 = b'{"status":404,"error":"no such repository"}'
    for published, code, body, want, why in (
        ("/v1/lit", 404, ROUTER_MISS, True, "literal, router miss"),
        ("/v1/lit", 404, handler_404, True, "literal, ANY 404 is dark"),
        ("/v1/lit", 401, b"", False, "401 proves routing reached a handler"),
        ("/v1/t/{id}", 404, ROUTER_MISS + b"\n", True, "parameterised, router miss"),
        ("/v1/t/{id}", 404, handler_404, False, "parameterised, the id was made up"),
    ):
        got = is_dark(published, code, body)
        assert got is want, f"is_dark({why}) = {got}, want {want}"


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    check_instrument()
    spec, base, ratchet_path = sys.argv[1:]

    # PyYAML is REQUIRED, not optional. The runner does not ship it, and the
    # json fallback that used to sit here could never have helped: this is
    # always called on openapi.yaml, and json.load on YAML fails with
    #
    #   json.decoder.JSONDecodeError: Expecting value: line 1 column 1 (char 0)
    #
    # which names neither the file nor the missing package, so a missing
    # dependency read as a corrupt spec. Fail on the real reason instead.
    try:
        import yaml
    except ImportError:
        sys.exit(
            "reach.py needs PyYAML to read the OpenAPI spec; install it "
            "(pip install pyyaml) before running this check"
        )

    doc = yaml.safe_load(open(spec))

    ops = probed_ops(doc)
    ratchet = {
        line.strip()
        for line in open(ratchet_path)
        if line.strip() and not line.startswith("#")
    }

    dark = {}
    with futures.ThreadPoolExecutor(max_workers=16) as pool:
        jobs = {pool.submit(probe, base, m, u): (m, p) for m, p, u in ops}
        for job in futures.as_completed(jobs):
            m, p = jobs[job]
            code, body, server = job.result()
            if is_dark(p, code, body):
                dark[f"{m} {p}"] = server or "?"

    new = sorted(set(dark) - ratchet)
    healed = sorted(ratchet - set(dark))

    literal = sum(1 for _, p, _ in ops if "{" not in p)
    print(
        f"reach: {len(ops)} addresses probed against {base} "
        f"({literal} literal, {len(ops) - literal} parameterised)"
    )
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
