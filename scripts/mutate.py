#!/usr/bin/env python3
"""mutate.py [name-filter] — break one guarded property, prove its test goes RED.

A test that stays green under the mutation it claims to guard is not evidence, so
every assertion worth keeping has a row here that breaks the thing it guards.

The scoring is the whole point, because a mutation harness has several ways to
report a pass while proving NOTHING, and this repo has been bitten by two of them:
an anchor whose text drifted silently became a SKIP that read like a pass, and any
non-zero exit scored as a kill, so a mutant that failed to COMPILE read as a kill.
Both are named states here and both are hard failures. Only KILLED counts, and
KILLED means: the mutation applied, the package still built, the named tests
actually RAN, and an ASSERTION failed.

  ANCHOR-MISS  the anchor is gone       — the mutation never applied
  AMBIGUOUS    the anchor is not unique — some other occurrence would be mutated
  NO-OP        old == new               — the mutation changes nothing
  NO-COMPILE   the mutant does not typecheck — an exit code, not a kill
  VACUOUS      -run matched zero tests  — nothing was exercised
  SURVIVED     built, ran, stayed green — the test does not guard it
  KILLED       built, ran, an assertion failed

The compile gate is `go vet`, not `go build`: vet typechecks _test.go files too, and
a mutant that breaks only the test build would otherwise reach the test run and be
scored on the exit code of a build error.

MUTATE_ROOT overrides the tree that gets mutated (default: this repo), which is how
a new assertion is shown SURVIVING on a pristine checkout and KILLED on the branch.
MUTATE_RUN=. widens each row's -run to the whole package, which asks whether ANYTHING
catches the mutant rather than whether its paired test does.
"""
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(os.environ.get("MUTATE_ROOT", Path(__file__).resolve().parents[1]))
RUN = os.environ.get("MUTATE_RUN", "")  # "." = ask the whole package, not just the pair
GO = "/usr/local/go/bin/go"
TAGS = "sqlite_fts5"  # the tag `make test` carries, so the same schema surface builds

# The suite has no boot, so it declares its own dev posture (Makefile DEV_KMS_KEY).
# Without it cek refuses to open a store and failures are pure environment artefact.
ENV = dict(os.environ, PATH="/usr/local/go/bin:" + os.environ.get("PATH", ""))
ENV.setdefault("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")

E = "clients/analytics/event.go"
A = "clients/analytics/analytics.go"
C = "clients/analytics/capture.go"
P = "clients/analytics/public.go"
S = "clients/sites/sites.go"
T = "clients/analytics/team.go"
M = "clients/meet/meet.go"
MT = "clients/meet/meet_test.go"
PA = "./clients/analytics/"
PS = "./clients/sites/"
PM = "./clients/meet/"

# A mutant is (name, edits, test regex, package). edits is a LIST of (file, old,
# new) so a mutation that needs a helper injected alongside it is the same kind of
# thing as one that does not — there is no special case for the second edit.
MUTANTS = [
    ("handle: drop the presented-but-unresolvable 403 branch", [
        (E, '\tif presented(c) {\n\t\treturn zip.ErrForbidden("valid bearer or a resolvable ingest key required")\n\t}\n', '')],
     "TestEveryDoorFailsClosedOnUnresolvableCredential", PA),

    ("routes: register a POST outside the doors loop", [
        (A, '\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))',
            '\tapp.Post("/v1/rogue", cloud.Handle(s, errorsLens))\n\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))')],
     "TestRoutedPostSetIsExactlyTheDoors", PA),

    ("doors: silently drop a declared door", [
        (E, '\t{path: "/v1/tracker", decode: decodeIngest, source: sourceCapture},\n', '')],
     "TestIngestSurfaceIsExactlyTheContract", PA),

    ("doors: rebind a door onto the OTHER wire", [
        (E, '\t{path: "/v1/analytics", decode: decodeIngest, source: sourceCapture},',
            '\t{path: "/v1/analytics", decode: decodeInsights, source: sourceCapture},')],
     "TestIngestSurfaceIsExactlyTheContract", PA),

    ("doors: relabel a door's origin tag", [
        (E, '\t{path: "/v1/tracker", decode: decodeIngest, source: sourceCapture},',
            '\t{path: "/v1/tracker", decode: decodeIngest, source: sourceEvent},')],
     "TestIngestSurfaceIsExactlyTheContract", PA),

    ("routes: resurrect the retired /v1/ingest door", [
        (A, '\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))',
            '\tapp.Post("/v1/ingest", cloud.Handle(s, doors[0].ingest))\n\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))')],
     "TestRetiredDoorIsGoneFromBothSurfaces", PA),

    ("carve: hand sites fewer paths than are routed", [
        (A, '\tfor _, d := range doors {\n\t\tcarve[d.path] = d.anon\n\t}',
            '\tfor _, d := range doors[:1] {\n\t\tcarve[d.path] = d.anon\n\t}')],
     "TestSiteHostCarvesExactlyTheDoors", PA),

    ("sites: widen the carve lookup to a prefix match", [
        (S, '\treturn h, ok && h != nil',
            '\tif ok && h != nil {\n\t\treturn h, true\n\t}\n\tfor p, ph := range analyticsHost {\n\t\tif ph != nil && strings.HasPrefix(c.Path(), p) {\n\t\t\treturn ph, true\n\t\t}\n\t}\n\treturn nil, false')],
     "TestMiddlewareCarvesExactlyTheInstalledSet", PS),

    ("sites: the same prefix widening, seen from analytics", [
        (S, '\treturn h, ok && h != nil',
            '\tif ok && h != nil {\n\t\treturn h, true\n\t}\n\tfor p, ph := range analyticsHost {\n\t\tif ph != nil && strings.HasPrefix(c.Path(), p) {\n\t\t\treturn ph, true\n\t\t}\n\t}\n\treturn nil, false')],
     "TestSiteHostCarvesExactlyTheDoors", PA),

    ("sites: drop the POST line from the carve", [
        (S, '\tif c.Method() != http.MethodPost {\n\t\treturn nil, false\n\t}\n', '')],
     "TestMiddlewareAnalyticsCarveGetServesStatic", PS),

    ("sites: dispatch a present-but-nil carve handler", [
        (S, '\treturn h, ok && h != nil', '\treturn h, ok')],
     "TestMiddlewareNilHandlerIsNotADoor", PS),

    ("sites: carve without requiring a resolved Site", [
        (S, '\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\tif site, ok := s.resolveLivePinned(c.Context(), slug, firstParty); ok {\n\t\t\t\t\treturn h(site.Org, c)\n\t\t\t\t}\n\t\t\t}',
            '\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\tif site, ok := s.resolveLivePinned(c.Context(), slug, firstParty); ok {\n\t\t\t\t\treturn h(site.Org, c)\n\t\t\t\t}\n\t\t\t\treturn h(slug, c)\n\t\t\t}')],
     "TestSiteHostCarveNeedsAResolvedSite", PA),

    ("handle: give the anonymous lane a brand-host tenant at FULL capability", [
        (E, '\treturn publicIngest(c, dec, publicTenant, source)',
            '\tif org, ok := cloud.BrandForHostOK(string(c.Fiber().Request().Host())); ok {\n\t\tevs, err := dec(c.Body())\n\t\tif err != nil {\n\t\t\treturn zip.ErrBadRequest("malformed event payload")\n\t\t}\n\t\treturn ingestDecoded(c, org, source, evs, 0)\n\t}\n\treturn publicIngest(c, dec, publicTenant, source)')],
     "TestEveryDoorProjectsTheAnonymousCaller", PA),

    ("handle: give the credential-less lane a brand-host tenant", [
        (E, '\treturn publicIngest(c, dec, publicTenant, source)',
            '\tif org, ok := cloud.BrandForHostOK(string(c.Fiber().Request().Host())); ok {\n\t\treturn publicIngest(c, dec, org, source)\n\t}\n\treturn publicIngest(c, dec, publicTenant, source)')],
     "TestApiHostAnonymousLaneWritesThePublicTenant", PA),

    ("publicIngest: let the anonymous lane keep every kind", [
        (P, 'var publicKinds = map[string]bool{"pageview": true, "error": true}',
            'var publicKinds = map[string]bool{"pageview": true, "error": true, "event": true, "identify": true, "group": true}')],
     "TestAnonIdentity_RefusedAtEveryDoor|TestEveryDoorProjectsTheAnonymousCaller", PA),

    ("door.anon: file the site's beacon under the public tenant", [
        (E, '\treturn publicIngest(c, d.decode, org, d.source)',
            '\treturn publicIngest(c, d.decode, publicTenant, d.source)')],
     "TestSiteHostLaneWritesTheResolvedSiteOrg", PA),

    # Middleware dispatches the carve from TWO branches with a byte-identical line
    # (sites.go:310 slug host, :321 bound custom domain), so one anchor of that line
    # alone silently mutates only the first and leaves the other unproven. One row per
    # branch, each anchored on the `if` above it, is what makes both facts.
    ("carve: the SLUG host takes the tenant from the caller's header", [
        (S, '\t\t\t\tif site, ok := s.resolveLivePinned(c.Context(), slug, firstParty); ok {\n\t\t\t\t\treturn h(site.Org, c)',
            '\t\t\t\tif site, ok := s.resolveLivePinned(c.Context(), slug, firstParty); ok {\n\t\t\t\t\t_ = site\n\t\t\t\t\treturn h(c.Org(), c)')],
     "TestSiteHostLaneWritesTheResolvedSiteOrg", PA),

    # Guarded in clients/sites, which owns host→org resolution, and NOT in analytics:
    # the analytics test on this host shape proves the carve fires, not who it fires for.
    ("carve: the CUSTOM DOMAIN takes the tenant from the caller's header", [
        (S, '\t\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\t\treturn h(site.Org, c)',
            '\t\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\t\treturn h(c.Org(), c)')],
     "TestMiddlewareAnalyticsCarveCustomDomain", PS),

    ("door.anon: consult handle on the site-host lane", [
        (E, '\treturn publicIngest(c, d.decode, org, d.source)', '\t_ = org\n\treturn handle(c, d.decode, d.source)')],
     "TestSiteHostLaneNeverConsultsHandle", PA),

    ("write core: drop the $source stamp on the way to the row", [
        (C, '\t\te.Properties = withSource(e.Properties, source)', '')],
     "TestEveryDoorStampsItsOwnSource", PA),

    # ── the anon lane's own $source: a second handler, stamped independently ──
    ("door.anon: stamp a CONSTANT source instead of the door's own", [
        (E, '\treturn publicIngest(c, d.decode, org, d.source)',
            '\treturn publicIngest(c, d.decode, org, sourceEvent)')],
     "TestEveryDoorStampsItsOwnSource", PA),

    # ── the write path's seams: silent data loss behind a 200 receipt ─────────
    ("seam: warehouseExec defaults to a no-op that discards every INSERT", [
        (C, '\twarehouseExec  = datastore.Exec',
            '\twarehouseExec  = func(context.Context, string, ...any) error { return nil }')],
     "TestWritePathSeamsDefaultToTheRealThing", PA),

    ("seam: warehouseReady defaults to always-true, removing the gate", [
        (C, '\twarehouseReady = datastore.Ready', '\twarehouseReady = func() bool { return true }')],
     "TestWritePathSeamsDefaultToTheRealThing", PA),

    # The blank var is load-bearing: OrgForKey is capture.go's only use of the cloud
    # package, so substituting it also orphans the import. That made this mutant
    # NO-COMPILE — an exit code the old scoring would have counted as a kill.
    ("seam: resolveKeyOrg defaults to a resolver that admits any key", [
        (C, 'var resolveKeyOrg = cloud.OrgForKey',
            'var resolveKeyOrg = func(context.Context, string) (string, bool) { return "acme", true }\n\nvar _ = cloud.OrgForKey')],
     "TestWritePathSeamsDefaultToTheRealThing", PA),

    # ── the PII scrub's CALL SITE, not the scrub ──────────────────────────────
    ("write core: store RAW properties, skipping the PII scrub", [
        (C, '\t\tproperties:     scrubProps(e.Properties),', '\t\tproperties:     rawProps(e.Properties),'),
        (C, 'func scrubProps(p map[string]any) string {',
            'func rawProps(p map[string]any) string {\n'
            '\tif len(p) == 0 {\n\t\treturn ""\n\t}\n'
            '\tb, err := json.Marshal(p)\n\tif err != nil {\n\t\treturn ""\n\t}\n\treturn string(b)\n}\n\n'
            'func scrubProps(p map[string]any) string {')],
     "TestStoredPropertiesAreScrubbed", PA),

    # ── the first-party org pin, on the carve's own call site ─────────────────
    ("sites: the CARVE resolves UNPINNED on a first-party host", [
        (S, '\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\tif site, ok := s.resolveLivePinned(c.Context(), slug, firstParty); ok {',
            '\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\tif site, ok := s.resolveLive(c.Context(), slug); ok {')],
     "TestMount_HostCarve_FirstPartyHostResolvesPinned", PA),

    # ── the warehouse schema: retention and the tenant boundary ───────────────
    # These four anchor on the FIX, so on a pristine checkout they report ANCHOR-MISS
    # rather than SURVIVED — the text they revert does not exist there, and neither do
    # the tests. KILLED here is what says the assertion, not luck, is doing the work.
    ("schema: measure retention from the CALLER's timestamp again", [
        (C, 'TTL ingested_at + INTERVAL 2 YEAR`', 'TTL timestamp + INTERVAL 2 YEAR`')],
     "TestRetentionIsNotARequestParameter", PA),

    ("schema: let the wire SET the column retention is measured from", [
        (C, '\t"properties", "library", "library_version",',
            '\t"properties", "library", "library_version", "ingested_at",')],
     "TestRetentionIsNotARequestParameter", PA),

    ("schema: drop PARTITION BY — back to one shared 'all' partition", [
        (C, '\tPARTITION BY (tenant_id, toYYYYMM(timestamp))\n', '')],
     "TestTenantIsThePartitionBoundary", PA),

    # Partitioning by month ALONE still prunes by time, so a test that only asked
    # "is there a PARTITION BY" would pass. The tenant half is the isolation half.
    #
    # Anchored through `\n\tORDER BY` on purpose: the bare clause appears TWICE in
    # capture.go — in the DDL and in the comment explaining it — and the unqualified
    # anchor scored AMBIGUOUS, which is the harness refusing to mutate a site it cannot
    # name uniquely. Reaching into the next DDL line is what makes it one site.
    ("schema: partition by month only, dropping the tenant boundary", [
        (C, '\tPARTITION BY (tenant_id, toYYYYMM(timestamp))\n\tORDER BY',
            '\tPARTITION BY toYYYYMM(timestamp)\n\tORDER BY')],
     "TestTenantIsThePartitionBoundary", PA),

    ("clamp: remove the PAST bound, restoring the unbounded key range", [
        (C, 'if ts.After(now.Add(maxClockSkew)) || ts.Before(now.Add(-maxBackdate)) {',
            'if ts.After(now.Add(maxClockSkew)) {')],
     "TestBackdatedTimestampIsClamped", PA),

    # The same removal, asked from the live door instead of the unit: the team wire's
    # epoch-MILLIS is the reachable way to 1970 (`"timestamp":1`), and teamTime is
    # deliberately not where it is stopped.
    ("clamp: the team wire reaches 1970 through the write core", [
        (C, 'if ts.After(now.Add(maxClockSkew)) || ts.Before(now.Add(-maxBackdate)) {',
            'if ts.After(now.Add(maxClockSkew)) {')],
     "TestTeamEpochMillisCannotReach1970", PA),

    # Guarding the guard: teamTime returning "" for a ZERO millis is what keeps the
    # absent-timestamp case off the epoch, and it is a separate fact from the clamp.
    ("clamp: teamTime renders a ZERO millis as the epoch instead of empty", [
        (T, '\tif ms <= 0 {\n\t\treturn ""\n\t}\n', '')],
     "TestTeamTimestampAbsentClampsToNow", PA),

    # ── meet's unauthenticated replies: the leak set, and its vacuity guard ───
    # TWO edits, and both are needed to prove what the NEW element adds. Leaking the
    # reason verbatim is already caught by the fragment list ("keys.yaml"), so a
    # single-edit mutant would be killed by the OLD assertion too and would prove
    # nothing. Rewording the reason so no fragment appears in it is what isolates the
    # whole-reason element — this mutant SURVIVES on a pristine checkout (where the
    # leak set held t.TempDir(), which can never match) and is KILLED here.
    ("meet: leak a REWORDED reason from the unauthenticated health surface", [
        (M, '\t\tres["status"], res["ready"] = "degraded", false',
            '\t\tres["status"], res["ready"], res["reason"] = "degraded", false, s.State.reason'),
        (M, 'fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) declares no api key", path)',
            'fmt.Errorf("the office has nothing to sign with")')],
     "TestHealthLeaksNothingUnauthenticated", PM),

    ("meet: leak a REWORDED reason from the unauthenticated getToken 503", [
        (M, 'return zip.Errorf(http.StatusServiceUnavailable, "meet: the office is not configured")',
            'return zip.Errorf(http.StatusServiceUnavailable, "meet: the office is not configured: %s", st.reason)'),
        (M, 'fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) declares no api key", path)',
            'fmt.Errorf("the office has nothing to sign with")')],
     "TestUnconfiguredReasonNeverReachesTheCaller", PM),

    # A leak test whose fixture is CONFIGURED has no reason to withhold and asserts
    # nothing — the exact shape of the t.TempDir() element it replaced. Mutating the
    # fixture must trip the guard, not pass quietly.
    ("meet: configure the fixture, so the leak test has nothing to catch", [
        (MT, '\tpath := keyFileWith(t, "")\n\tapp := mountWithKeyFile(t, teamSecret, path)',
            '\tpath := keyFileWith(t, keyBody(apiKey, apiSecret))\n\tapp := mountWithKeyFile(t, teamSecret, path)')],
     "TestUnconfiguredReasonNeverReachesTheCaller", PM),
]

RUN_RE = re.compile(r"^=== RUN\s+(\S+)", re.M)
FAIL_RE = re.compile(r"^\s*--- FAIL: (\S+)", re.M)


def apply(edits):
    """Apply every edit, or report the state that says why none were applied."""
    for path, old, new in edits:
        src = (ROOT / path).read_text()
        n = src.count(old)
        if n == 0:
            return "ANCHOR-MISS", f"anchor absent from {path} — the mutation never applied"
        if n > 1:
            return "AMBIGUOUS", f"anchor occurs {n}x in {path} — it no longer names one site"
        if old == new:
            return "NO-OP", f"old == new in {path} — the mutation changes nothing"
    for path, old, new in edits:
        p = ROOT / path
        p.write_text(p.read_text().replace(old, new, 1))
    return None, None


def score(name, edits, test, pkg):
    files = {path for path, _, _ in edits}
    for f in files:
        shutil.copy2(ROOT / f, ROOT / (f + ".mutbak"))
    try:
        state, detail = apply(edits)
        if state:
            return state, detail

        # 1. does it TYPECHECK, test files included? an exit code is not a kill.
        vet = subprocess.run([GO, "vet", "-tags", TAGS, pkg], cwd=ROOT, env=ENV,
                             capture_output=True, text=True, timeout=900)
        if vet.returncode != 0:
            return "NO-COMPILE", " / ".join(l.strip() for l in vet.stderr.splitlines()[:3])[:200]

        # 2. run the paired tests VERBOSE, so "they ran" is observed, not assumed.
        # MUTATE_RUN=. widens that to the whole package, which asks the stronger
        # question: does anything AT ALL catch this, or only the test we paired it with.
        p = subprocess.run([GO, "test", "-tags", TAGS, pkg, "-run", RUN or test, "-count=1", "-v"],
                           cwd=ROOT, env=ENV, capture_output=True, text=True, timeout=900)
        out = p.stdout + p.stderr
        ran = set(RUN_RE.findall(out))
        if not ran:
            return "VACUOUS", f"-run {test!r} matched ZERO tests"
        if p.returncode == 0:
            return "SURVIVED", f"{len(ran)} test(s) ran and stayed GREEN — nothing guards this"
        fails = FAIL_RE.findall(out)
        if fails:
            return "KILLED", f"{len(ran)} ran, RED: {', '.join(sorted(set(fails))[:4])}"
        # A panic is a kill too, and it prints NO `--- FAIL` summary because it takes
        # the test binary down first — so it must be recognised explicitly or it lands
        # in the fallback below and reads as a build failure. Said out loud in the
        # detail, because a crash proves the mutant is DETECTABLE where an assertion
        # proves a named test detects it.
        if "panic:" in out:
            return "KILLED", f"{len(ran)} ran, RED by PANIC (the mutant crashes the request)"
        return "NO-COMPILE", "non-zero exit with no --- FAIL and no panic — not a test failure"
    finally:
        for f in files:
            shutil.move(ROOT / (f + ".mutbak"), ROOT / f)


if __name__ == "__main__":
    only = sys.argv[1] if len(sys.argv) > 1 else ""
    rows = [m for m in MUTANTS if only.lower() in m[0].lower()]
    if not rows:
        sys.exit(f"no mutant matches {only!r}")
    print(f"mutating {ROOT}\n")
    bad = 0
    for m in rows:
        state, detail = score(*m)
        if state != "KILLED":
            bad += 1
        print(f"{state:12} {m[0]}\n{'':12} {detail}", flush=True)
    print(f"\n{len(rows) - bad}/{len(rows)} KILLED "
          f"(applied + typechecked + tests RAN + an assertion failed)")
    sys.exit(1 if bad else 0)
