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

E = "apps/analytics/event.go"
A = "apps/analytics/analytics.go"
C = "apps/analytics/capture.go"
P = "apps/analytics/public.go"
S = "apps/sites/sites.go"
T = "apps/analytics/team.go"
M = "apps/meet/meet.go"
MT = "apps/meet/meet_test.go"
PA = "./apps/analytics/"
PS = "./apps/sites/"
PM = "./apps/meet/"
H = "cmd/cloud/main.go"
PH = "./cmd/cloud/"
LB = "apps/label/label.go"
LT = "apps/label/typed.go"
LF = "apps/label/fact.go"
LS = "apps/label/store.go"
LM = "apps/label/mirror.go"
LR = "apps/label/resolve.go"
PL = "./apps/label/"
RB = "resource_billing.go"
PRB = "."
CL = "cmd/closure/main.go"
PCL = "./cmd/closure/"
RT = "apps/risk/typed.go"
OD = "orgdb.go"
PC = "."
RL = "apps/risk/learn.go"
RG = "apps/risk/ring.go"
PR = "./apps/risk/"
ML = "apps/ml/ml.go"
PML = "./apps/ml/"

# A mutant is (name, edits, test regex, package). edits is a LIST of (file, old,
# new) so a mutation that needs a helper injected alongside it is the same kind of
# thing as one that does not — there is no special case for the second edit.
MUTANTS = [
    ("handle: drop the presented-but-unresolvable 403 branch", [
        (E, '\tif presented(c) {\n\t\treturn zip.ErrForbidden("valid bearer or a resolvable ingest key required")\n\t}\n', '')],
     "TestEveryDoorFailsClosedOnUnresolvableCredential", PA),

    ("routes: register a POST outside the endpoints loop", [
        (A, '\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))',
            '\tapp.Post("/v1/rogue", cloud.Handle(s, errorsLens))\n\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))')],
     "TestRoutedPostSetIsExactlyTheDoors", PA),

    ("endpoints: silently drop a declared endpoint", [
        (E, '\t{path: "/v1/todo", decode: decodeIngest, source: sourceCapture},\n', '')],
     "TestIngestSurfaceIsExactlyTheContract", PA),

    ("endpoints: rebind an endpoint onto the OTHER wire", [
        (E, '\t{path: "/v1/analytics", decode: decodeIngest, source: sourceCapture},',
            '\t{path: "/v1/analytics", decode: decodeInsights, source: sourceCapture},')],
     "TestIngestSurfaceIsExactlyTheContract", PA),

    ("endpoints: relabel an endpoint's origin tag", [
        (E, '\t{path: "/v1/todo", decode: decodeIngest, source: sourceCapture},',
            '\t{path: "/v1/todo", decode: decodeIngest, source: sourceEvent},')],
     "TestIngestSurfaceIsExactlyTheContract", PA),

    ("routes: resurrect the retired /v1/ingest endpoint", [
        (A, '\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))',
            '\tapp.Post("/v1/ingest", cloud.Handle(s, endpoints[0].ingest))\n\tapp.Get("/v1/errors", cloud.Handle(s, errorsLens))')],
     "TestRetiredDoorIsGoneFromBothSurfaces", PA),

    ("carve: hand sites fewer paths than are routed", [
        (A, '\tfor _, d := range endpoints {\n\t\tcarve[d.path] = d.anon\n\t}',
            '\tfor _, d := range endpoints[:1] {\n\t\tcarve[d.path] = d.anon\n\t}')],
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

    ("endpoint.anon: file the site's beacon under the public tenant", [
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

    # Guarded in apps/sites, which owns host→org resolution, and NOT in analytics:
    # the analytics test on this host shape proves the carve fires, not who it fires for.
    ("carve: the CUSTOM DOMAIN takes the tenant from the caller's header", [
        (S, '\t\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\t\treturn h(site.Org, c)',
            '\t\t\t\tif h, ok := analyticsIngest(c); ok {\n\t\t\t\t\treturn h(c.Org(), c)')],
     "TestMiddlewareAnalyticsCarveCustomDomain", PS),

    ("endpoint.anon: consult handle on the site-host lane", [
        (E, '\treturn publicIngest(c, d.decode, org, d.source)', '\t_ = org\n\treturn handle(c, d.decode, d.source)')],
     "TestSiteHostLaneNeverConsultsHandle", PA),

    ("write core: drop the $source stamp on the way to the row", [
        (C, '\t\te.Properties = withSource(e.Properties, source)', '')],
     "TestEveryDoorStampsItsOwnSource", PA),

    # ── the anon lane's own $source: a second handler, stamped independently ──
    ("endpoint.anon: stamp a CONSTANT source instead of the endpoint's own", [
        (E, '\treturn publicIngest(c, d.decode, org, d.source)',
            '\treturn publicIngest(c, d.decode, org, sourceEvent)')],
     "TestEveryDoorStampsItsOwnSource", PA),

    # ── the write path's clients: silent data loss behind a 200 receipt ─────────
    ("client: warehouseExec defaults to a no-op that discards every INSERT", [
        (C, '\twarehouseExec  = datastore.Exec',
            '\twarehouseExec  = func(context.Context, string, ...any) error { return nil }')],
     "TestWritePathClientsDefaultToTheRealThing", PA),

    ("client: warehouseReady defaults to always-true, removing the gate", [
        (C, '\twarehouseReady = datastore.Ready', '\twarehouseReady = func() bool { return true }')],
     "TestWritePathClientsDefaultToTheRealThing", PA),

    # The blank var is load-bearing: OrgForKey is capture.go's only use of the cloud
    # package, so substituting it also orphans the import. That made this mutant
    # NO-COMPILE — an exit code the old scoring would have counted as a kill.
    ("client: resolveKeyOrg defaults to a resolver that admits any key", [
        (C, 'var resolveKeyOrg = cloud.OrgForKey',
            'var resolveKeyOrg = func(context.Context, string) (string, bool) { return "acme", true }\n\nvar _ = cloud.OrgForKey')],
     "TestWritePathClientsDefaultToTheRealThing", PA),

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

    # The same removal, asked from the live endpoint instead of the unit: the team wire's
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

    # The 2026-07-29 outage, as four mutations. One child that could not open a
    # SQLite file returned an error from zip.Load, the host escalated it to
    # os.Exit(1), and api.hanzo.ai + cloud.hanzo.ai were 502/503 for 25 minutes.
    # Each row breaks one of the four properties that fix rests on, and each names
    # the failure it reintroduces — the last two matter most, because both are ways
    # of "fixing" the outage that would trade it for a silent one.
    ("host: fail HARD on any plugin that will not start (the 25-minute outage)", [
        (H, '\tif a.Required {\n\t\treturn fmt.Errorf("%s is required here and would not start: %w", a.Name, err)\n\t}',
            '\tif true {\n\t\treturn fmt.Errorf("%s is required here and would not start: %w", a.Name, err)\n\t}')],
     "TestADeadSubsystemDoesNotTakeTheHostDown", PH),

    # Without the second Load the prefix is never registered, so the request falls
    # through to the console's "/" catch-all — how /v1/meet/health answered
    # 200 text/html over a subsystem that was not there.
    ("host: leave an absent subsystem's prefix unregistered", [
        (H, '\tp.Lazy = true\n\tif err := app.Add(zip.Load(p, a.Prefixes...)); err != nil {\n\t\treturn fmt.Errorf("%s: mounting it absent failed too: %w", a.Name, err)\n\t}\n\treturn nil',
            '\treturn nil')],
     "TestADeadSubsystemDoesNotTakeTheHostDown|TestAnAbsentPrefixBeatsTheConsoleCatchAll", PH),

    ("host: log the absence but do not report it (silent degradation)", [
        (H, '\tabsent[a.Name] = err.Error()\n', '')],
     "TestAbsenceIsObservable", PH),

    # Failing liveness for an optional plugin recreates the outage one layer up:
    # K8s restarts a pod that is serving every other subsystem correctly.
    ("host: fail liveness when any subsystem is absent", [
        (H, '\t\t\tout["absent"] = a\n\t\t}\n\t\treturn c.JSON(200, out)',
            '\t\t\tout["absent"] = a\n\t\t\treturn c.JSON(503, out)\n\t\t}\n\t\treturn c.JSON(200, out)')],
     "TestAbsenceIsObservable", PH),

    # ── the ground-truth plane: durability, delivery order, and the leakage guard ──
    #
    # These rows anchor on the FIX, so on a checkout that predates it they report
    # ANCHOR-MISS rather than SURVIVED — the text they revert does not exist there,
    # and neither do the tests. KILLED here is what says the assertion is doing the
    # work. Each row reintroduces exactly one defect the plane was held for.

    # DURABILITY. cloud is strategy Recreate at one replica: an acknowledged record
    # that was never shipped is not merely at risk, the successor hydrates the older
    # durable snapshot OVER it. `_ = sent` keeps the mutant compiling, which is the
    # difference between a kill and an exit code.
    ("label: acknowledge a record that was never shipped to its durable object", [
        (LT, '\tif out.Recorded > 0 || sent > 0 {\n'
             '\t\tif err := o.s.State.ship(sc.ns); err != nil {\n'
             '\t\t\to.s.Log.Error("label: the record was written and could not be shipped",\n'
             '\t\t\t\t"tenant", sc.tenant.String(), "err", err)\n'
             '\t\t\treturn nil, zip.Errorf(http.StatusServiceUnavailable,\n'
             '\t\t\t\t"the record was not acknowledged as durable, so it is not acknowledged at all; retry (every write here is idempotent on the assertion\'s content): %v", err)\n'
             '\t\t}\n\t}\n',
             '\t_ = sent\n')],
     "TestAnAcknowledgedRecordSurvivesATakeover", PL),

    # A DEPOSED writer's ship is refused at a stale round with NO error — Sync
    # answers (false, nil). Checking the error alone acknowledges a record written
    # on a pod whose file the next reader never opens: two divergent copies of one
    # tenant's compliance record. TestAWriteOnANonOwnerFailsClosed does NOT guard
    # this line (a replica that never held the lease gets ErrNotOwner and is caught
    # by the error check), which is why the deposition test exists.
    ("label: an unacked ship is a shrug rather than a refusal", [
        (LB, '\tif !acked {\n\t\treturn fmt.Errorf("this replica is not the elected writer for the tenant, so the write is not acknowledged")\n\t}\n',
             '\t_ = acked\n')],
     "TestADeposedWriterDoesNotAcknowledge", PL),

    ("label: acknowledge a disposal that was never shipped", [
        (LT, '\t\tif err := o.s.State.ship(sc.ns); err != nil {\n'
             '\t\t\to.s.Log.Error("label: records were disposed of and the disposal could not be shipped",\n'
             '\t\t\t\t"tenant", sc.tenant.String(), "err", err)\n'
             '\t\t\treturn nil, zip.Errorf(http.StatusServiceUnavailable,\n'
             '\t\t\t\t"the disposal was not acknowledged as durable, so it is not acknowledged at all; retry: %v", err)\n'
             '\t\t}\n', '')],
     "TestADisposalIsShippedBeforeItIsAcknowledged", PL),

    ("label: acknowledge a litigation hold that was never shipped", [
        (LT, '\tif changed > 0 {\n'
             '\t\t// SHIP BEFORE ACK. A hold that a rollout forgets is a record disposed of\n'
             '\t\t// while somebody believed it was preserved.\n'
             '\t\tif err := o.s.State.ship(sc.ns); err != nil {\n'
             '\t\t\to.s.Log.Error("label: the hold was written and could not be shipped",\n'
             '\t\t\t\t"tenant", sc.tenant.String(), "err", err)\n'
             '\t\t\treturn nil, zip.Errorf(http.StatusServiceUnavailable,\n'
             '\t\t\t\t"the hold was not acknowledged as durable, so it is not acknowledged at all; retry: %v", err)\n'
             '\t\t}\n\t}\n', '')],
     "TestAHoldIsShippedBeforeItIsAcknowledged", PL),

    # DELIVERY ORDER. The cursor back on (wrote, id) over a write clock truncated to
    # the second: a row that commits after a concurrent delivery has read, whose
    # digest sorts lower inside the same second, is already behind the mark. It is
    # never mirrored, the mark only moves forward so no retry reaches it, and
    # pending() answers zero because it asks the same predicate.
    ("label: the delivery cursor is a clock again, so it steps over a concurrent write", [
        (LS, 'func (c cursor) after() (string, []any) { return "seq > ?", []any{int64(c)} }',
             'func (c cursor) after() (string, []any) { return "wrote > ?", []any{int64(c)} }'),
        (LS, 'FROM assert WHERE `+where+` ORDER BY seq ASC LIMIT ?`',
             'FROM assert WHERE `+where+` ORDER BY wrote ASC, id ASC LIMIT ?`'),
        (LM, '\tto := cursor(batch[len(batch)-1].Seq)',
             '\tto := cursor(batch[len(batch)-1].Wrote.Unix())')],
     "TestTheCursorCannotStepOverAWriteItNeverSaw|TestConcurrentWritesAreAllDelivered", PL),

    # LEAKAGE. `seen` is the filer's claim, bounded only by At <= Seen <= now+skew.
    # A dispute filed today with seen == at is then knowable a year before the row
    # existed, and a backtest standing two days after the event resolves it.
    ("label: the leakage guard takes the filer's word for when it was knowable", [
        (LF, '\tf.Knowable = later(f.Seen, f.Wrote)', '\tf.Knowable = f.Seen')],
     "TestTheGuardDoesNotTakeTheFilersWordForIt", PL),

    ("label: the within-rank tie-break reads the caller's declared instant", [
        (LR, '\tif !a.Knowable.Equal(b.Knowable) {\n\t\treturn a.Knowable.After(b.Knowable)\n\t}',
             '\tif !a.Seen.Equal(b.Seen) {\n\t\treturn a.Seen.After(b.Seen)\n\t}')],
     "TestTheWinnerWithinARankIsDecidedByAServerObservedInstant", PL),

    # The warehouse half of the same guard: a materialiser joining there would
    # resolve under a rule the record plane had already rejected.
    ("label: the warehouse applies the horizon to the declared instant", [
        (LM, '    AND knowable <= at + ?', '    AND seen <= at + ?')],
     "TestTheColumnarOrderingNamesEverySource", PL),

    ("label: the derived copy drops the server-observed instant entirely", [
        (LM, '\tknowable    DateTime,\n', ''),
        (LM, '\t\t\tt.String(), string(f.Kind), f.Subject, f.At, f.Seen, f.Knowable,',
             '\t\t\tt.String(), string(f.Kind), f.Subject, f.At, f.Seen,'),
        (LM, '\tconst width = 12', '\tconst width = 11'),
        (LM, '\t\tvalues = append(values, "(?,?,?,?,?,?,?,?,?,?,?,?)")',
             '\t\tvalues = append(values, "(?,?,?,?,?,?,?,?,?,?,?)")'),
        (LM, '(org, kind, subject, at, seen, knowable, disposition, source, evidence, "by", confidence, id)',
             '(org, kind, subject, at, seen, disposition, source, evidence, "by", confidence, id)')],
     "TestTheDerivedInstantReachesTheWarehouse", PL),

    # THE TRAINING GATE. A 90-day window running to NOW under a 120-day horizon can
    # hold no matured event, so the op documented as the gate on training answered
    # zero on its own defaults however much ground truth the tenant held.
    ("label: the default coverage window runs to now, so nothing in it can mature", [
        (LT, '\t\tto = now.Add(-horizonFor)', '\t\tto = now')],
     "TestTheTrainingGateAnswersOnItsOwnDefaults", PL),

    # `matured` is the DENOMINATOR an operator divides `judged` by. Dropping the
    # cohort whose assertions all arrived after its own as-of makes the denominator
    # exclude exactly the numerator's complement.
    ("label: matured counts only what was labelled", [
        (LR, '\t\tc.Label, c.Labelled = Resolve(by[k], c.AsOf)\n\t\tout = append(out, c)',
             '\t\tc.Label, c.Labelled = Resolve(by[k], c.AsOf)\n\t\tif !c.Labelled {\n\t\t\tcontinue\n\t\t}\n\t\tout = append(out, c)')],
     "TestCoverageCountsWhatMaturedAndWhoWon", PL),

    # A hold requested and not applied is a compliance control that reports success.
    ("label: a litigation hold on an existing record is silently dropped", [
        (LT, '\tchanged, present, err := st.setHold(ctx, ids, in.Hold)',
             '\tchanged, present, err := 0, len(ids), error(nil)')],
     "TestAHoldCanBePlacedOnARecordThatExists", PL),

    # PARTITION CARDINALITY. `<brand>/<org>` is the only unbounded-cardinality
    # partition key the warehouse would carry: directories, part metadata and merge
    # scheduling all grow with the customer count on a shared single-pod engine.
    ("label: partition the shared warehouse table by tenant again", [
        (LM, 'PARTITION BY toYYYYMM(at)', 'PARTITION BY org')],
     "TestThePartitionKeyIsBoundedInCardinality", PL),

    # An event named twice reads its own row twice, so the resolution lists the
    # winner as a contrary claim and a materialiser gets duplicate training rows.
    ("label: an event named twice is resolved twice, and becomes its own conflict", [
        (LT, '\t\tkey := eventKey(ev.Kind, ev.Subject, ev.At)\n'
             '\t\tif _, dup := named[key]; dup {\n\t\t\tcontinue\n\t\t}\n'
             '\t\tnamed[key] = struct{}{}\n\t\twant = append(want, ev)',
             '\t\t_ = named\n\t\twant = append(want, ev)')],
     "TestAnAssertionIsNotItsOwnConflict", PL),
    # ── THE ADDRESS IS THE PRODUCT ───────────────────────────────────────────
    #
    # openapi.Product reads an operation's product tag off the first /v1 segment
    # and nothing else, so an address under /v1/ml publishes these compliance ops
    # as part of the live KServe model-SERVING product. The floor ratchet reads
    # that as growth (ml: 7 -> 14) because it refuses only a shrink.
    ("label: address the ground-truth plane inside the model-serving product", [
        (LT, '\tzip.Post(zapp, "/v1/risk/labels", o.label,',
             '\tzip.Post(zapp, "/v1/ml/labels", o.label,')],
     "TestEveryAddressFilesIntoOneProductAndItIsRisk", PL),

    # An operation id is the SDK method name and the CLI command, so a wrong
    # prefix survives a right address and tells every generated caller that this
    # op belongs to a product it is not part of.
    ("label: name an operation for the model-serving product", [
        (LT, 'zip.WithOperationID("riskLabelCoverage")',
             'zip.WithOperationID("mlLabelCoverage")')],
     "TestTheOperationIDsCarryTheProduct", PL),

    # ── A COUNT OVER CALLER-SIZED VALUES IS NOT A BOUND ──────────────────────
    #
    # maxResolve bounds the EVENTS at 500. Without a ceiling on the subject,
    # nothing bounds the bytes but the edge's BodyLimit — and each subject is
    # copied into a dedupe key, a grouping key and one bound parameter per event
    # in a statement against a single-writer file.
    ("label: the resolve endpoint takes a subject of any size", [
        (LT, '\t\tsubject, err := admitSubject(e.Subject)\n'
             '\t\tif err != nil {\n'
             '\t\t\treturn nil, zip.Errorf(http.StatusBadRequest, "subjects[%d]: %v", i, err)\n'
             '\t\t}',
             '\t\tsubject := strings.TrimSpace(e.Subject)')],
     "TestNoCountBoundStandsWithoutAByteBound", PL),

    # The read filter becomes a bound parameter against the tenant's own file, so
    # an unbounded one is kilobytes in a statement looking for a value the write
    # endpoint could never have stored.
    ("label: the read filter binds a subject of any size", [
        (LT, '\tif strings.TrimSpace(in.Subject) != "" {\n'
             '\t\tif q.Subject, err = admitSubject(in.Subject); err != nil {\n'
             '\t\t\treturn nil, zip.Errorf(http.StatusBadRequest, "subject: %v", err)\n'
             '\t\t}\n\t}',
             '\tq.Subject = in.Subject')],
     "TestNoCountBoundStandsWithoutAByteBound", PL),

    # A filter outside the closed vocabulary can only ever match zero rows, so
    # admitting it charges the caller for a scan and answers [] — and an open
    # field has no byte bound at all.
    ("label: the read filter admits a kind outside the vocabulary", [
        (LT, '\tif strings.TrimSpace(in.Kind) != "" {\n'
             '\t\tif q.Kind, err = admitKind(in.Kind); err != nil {\n'
             '\t\t\treturn nil, zip.Errorf(http.StatusBadRequest, "kind: %v", err)\n'
             '\t\t}\n\t}',
             '\tq.Kind = Kind(in.Kind)')],
     "TestNoCountBoundStandsWithoutAByteBound", PL),

    # An instant is measured BEFORE the parser walks it and before %q renders it
    # into a refusal and the log line beside it. Removing the measurement puts the
    # caller's own kilobytes in the response.
    ("label: an instant is rendered into its refusal before it is measured", [
        (LT, '\tif len(s) > instantMax {\n'
             '\t\treturn time.Time{}, fmt.Errorf("an instant is %d bytes and the bound is %d", len(s), instantMax)\n'
             '\t}\n',
             '')],
     "TestNoCountBoundStandsWithoutAByteBound", PL),

    # THE STRUCTURAL HALF: a new caller-sized field on an endpoint, with no ceiling.
    # This is the shape the defect actually arrived in — the write endpoint bounded
    # its subject, the read endpoints added later did not, and nothing compared them.
    ("label: a new caller-sized field arrives on an endpoint with no ceiling", [
        (LT, '\tBefore string `json:"before"`\n}',
             '\tBefore string `json:"before"`\n\tReason string `json:"reason,omitempty"`\n}')],
     "TestEveryCallerSizedFieldDeclaresACeiling", PL),

    # ── A HOLD MID-SWEEP MUST KEEP THE RECORD IN BOTH PLANES ────────────────
    #
    # The copy is swept first so nothing is orphaned in the warehouse. A record
    # the delete then declines to remove has already been swept, its seq is behind
    # the delivery cursor, and deliver() asks the cursor rather than the world — so
    # no retry re-sends it and pending() answers zero. A hole in the answer key
    # reads as an honest customer, and the row is the one somebody is litigating.
    ("label: a hold that arrives mid-sweep loses the row from the answer key", [
        (LT, '\t\tif len(kept) > 0 {\n\t\t\tfacts, err := st.byIDs(ctx, kept)',
             '\t\tif false {\n\t\t\tfacts, err := st.byIDs(ctx, kept)')],
     "TestAHoldPlacedDuringASweepKeepsTheRecordInBothPlanes", PL),

    # A repair that cannot be made must fail the request. Answering 200 tells a
    # tenant its litigation hold held while the answer key quietly lost the row.
    ("label: a repair the derived copy refuses is a shrug rather than a refusal", [
        (LT, '\t\t\t\treturn nil, zip.Errorf(http.StatusServiceUnavailable,\n'
             '\t\t\t\t\t"%d records were placed under litigation hold during this sweep',
             '\t\t\t\t_ = zip.Errorf(http.StatusServiceUnavailable,\n'
             '\t\t\t\t\t"%d records were placed under litigation hold during this sweep')],
     "TestARepairTheDerivedCopyRefusesIsNotAcknowledged", PL),

    # A compliance report that says it deleted a record it is still holding is the
    # wrong answer to the only question the report is asked.
    ("label: the retention report counts what was identified, not what was disposed of", [
        (LT, 'Disposed: len(expired) - len(kept),',
             'Disposed: len(expired),')],
     "TestAHoldPlacedDuringASweepKeepsTheRecordInBothPlanes", PL),

    # ── THE PUBLISHED RULE NAMES THE FIELD THE RESOLVER READS ───────────────
    #
    # The op exists so a caller can reproduce a contested resolution. `seen` and
    # `knowable` are equal for a live pipeline and differ for exactly the
    # backfilled history the derivation exists to hold back, so a rule published
    # against `seen` is checkable and wrong.
    ("label: the published precedence rule names the filer's own instant again", [
        (LT, '\t\t\t"knowable: within one rank, the assertion that became KNOWABLE latest wins',
             '\t\t\t"seen: within one rank, the assertion that became KNOWABLE latest wins')],
     "TestVocabularyPublishesTheRuleThatIsEnforced", PL),

    # ── THE CALLER IS RESOLVED BEFORE THE MONEY PLANE IS ASKED ───────────────
    #
    # An empty org reaches Gate from any handler whose tenant check and whose
    # principal.Ledger disagree, and both of Gate's branches then answer a
    # question about IDENTITY in the vocabulary of MONEY: 503 "Billing
    # temporarily unavailable" co-resident, and 400 `field "subject" is
    # required` over the peer plane — naming a field that appears in no
    # published request schema, so no caller can ever satisfy it.
    ("gate: ask the money plane to price a spend for a nameless subject", [
        (RB, '\tif org == "" {\n\t\treturn ErrNoLedger\n\t}\n', '')],
     "TestGate_RefusesAnEmptyLedgerAsIdentityNotAsMoney|TestGate_FailOpenNeverMakesAnUnidentifiedCallerFree", PRB),

    # There is deliberately NO "move the guard below fail-open" row. Fail-open
    # lives INSIDE metering.Authorize and gatePeer, so every placement the guard
    # could take within Gate already precedes it: the mutation is a semantic
    # no-op and scored SURVIVED, which would have been a permanently red gate
    # guarding nothing. The property it was meant to state — fail-open never
    # makes an unidentified caller free — is carried by the row above, which
    # takes TestGate_FailOpenNeverMakesAnUnidentifiedCallerFree red as well.

    # denial is the ONE decision behind a refused Gate and both renderings read
    # it, so dropping the identity case re-launders an identity refusal back
    # into a fault of the biller for every one of Gate's callers at once.
    ("gate: render an identity refusal as a fault of the biller", [
        (RB, '\tif errors.Is(err, ErrNoLedger) {\n'
             '\t\treturn http.StatusForbidden, "forbidden", ErrNoLedger.Error()\n\t}\n', '')],
     "TestGate_RefusesAnEmptyLedgerAsIdentityNotAsMoney", PRB),

    # There is deliberately NO "delete ops.gate's empty-ledger guard" row either,
    # and its absence is the evidence that the class above is actually closed.
    # That mutation was written and scored SURVIVED: with Gate refusing an empty
    # org, deleting the risk-level guard still answers 403 with the same code and
    # the same sentence, because the defect is no longer representable one layer
    # down. What the surface guard still owns is the ENVELOPE — it refuses
    # through zip, so /v1/risk answers the flat {status,code,error}, where the
    # Gate path renders the money wire's nested {error:{code,message}}. When that
    # split is settled the surface guard is pure duplication and should go.

    # The positive half: a guard widened to refuse everyone makes the priced-op
    # assertions green by never billing anyone, which is a free surface.
    ("risk: widen the empty-ledger guard until it refuses every caller", [
        (RT, '\tif ledger == "" {', '\tif true {')],
     "TestPricedOps_StillReachTheMoneyPlaneForARealPrincipal", PR),

    # ── org store lifecycle ──────────────────────────────────────────────────
    # CloseAll was a RESET: it emptied the maps, so the next For() re-opened the
    # file. Every caller is a Shutdown path, so a request still in flight during a
    # rollout resurrected the store — re-hydrating and re-claiming the fence lease
    # the SUCCESSOR pod was claiming. Two live writers for one org.
    ("orgstore: CloseAll goes back to being a reset, not a close", [
        (OD, '\tc.closed = true\n\tc.byNS = map[namespace.Namespace]T{}',
             '\tc.byNS = map[namespace.Namespace]T{}')],
     "TestOrgStoreCloseAllIsTerminal", PC),
    ("orgstore: For stops refusing after close (the guard, not the flag)", [
        (OD, '\tif c.closed {\n\t\tc.mu.Unlock()\n\t\treturn zero, fmt.Errorf("%w: %s/%s", ErrStoreClosed, ns, c.subsystem)\n\t}\n', '')],
     "TestOrgStoreCloseAllIsTerminal", PC),
    ("orgstore: the cross-org sweep stops refusing after close", [
        (OD, '\tc.mu.Lock()\n\tshut := c.closed\n\tc.mu.Unlock()\n\tif shut {\n\t\treturn fmt.Errorf("%w: %s sweep", ErrStoreClosed, c.subsystem)\n\t}\n', '')],
     "TestOrgStoreCloseAllIsTerminal", PC),

    # ── risk: the rollout ────────────────────────────────────────────────────
    # close() was `p.stop(); p.wg.Wait()` with every save BEHIND it. The wait was
    # unbounded and the process gets a 30s window, while ONE search's durable Sync
    # is bounded at 30s on its own — so the wait outlived the window, the pod was
    # killed, and NOT ONE tenant's model had been written down. Every tenant
    # returns to warming, and a warming model refuses to score, which reads clean.
    ("risk: the rollout waits for background work with no bound (original)", [
        (RL, '\tdrained := p.drain(ctx)\n', '\tp.wg.Wait()\n\tdrained := true\n')],
     "TestRollout_", PR),
    ("risk: the saves happen only if the drain finished", [
        (RL, '\tvar errs []error\n\tfor _, r := range all {\n\t\tif err := p.save(r); err != nil {\n\t\t\terrs = append(errs, err)\n\t\t}\n\t}\n',
             '\tvar errs []error\n\tif drained {\n\t\tfor _, r := range all {\n\t\t\tif err := p.save(r); err != nil {\n\t\t\t\terrs = append(errs, err)\n\t\t\t}\n\t\t}\n\t}\n')],
     "TestRollout_", PR),
    ("risk: the drain ignores the caller's shutdown window", [
        (RL, '\tcase <-timer.C:\n\t\treturn false\n\tcase <-ctx.Done():\n\t\treturn false\n', '\tcase <-timer.C:\n\t\treturn false\n')],
     "TestRollout_", PR),
    ("risk: an incomplete drain becomes silent", [
        (RL, '\t\terrs = append(errs, ErrDrainIncomplete)\n', '')],
     "TestRollout_", PR),

    # ── risk: the search bound ───────────────────────────────────────────────
    # "ONE RUN PER TENANT" checked the slot and SET it two warehouse operations
    # later. Check-then-act: 16 concurrent callers all passed the check and all
    # rolled the tenant's source planes and read its ENTIRE history first.
    ("risk: the search slot is claimed after the expensive work (original)", [
        (RL, '\tid := runID()\n\tif err := p.claim(t, id); err != nil {\n\t\treturn report{}, err\n\t}\n',
             '\tid := runID()\n\tp.mu.Lock()\n\tif held, running := p.running[t]; running {\n\t\tp.mu.Unlock()\n\t\treturn report{}, zip.ErrConflict("a search is already running for this organisation: " + held.ID)\n\t}\n\tp.mu.Unlock()\n')],
     "TestSearch_OneTenantsConcurrencyDoesNotMultiplyTheExpensiveRead", PR),
    # ── risk: the search pays for the surface it reads ───────────────────────
    # The surface half — rolling up to four source planes and reading the window
    # back — ran BEFORE ANY GATE AT ALL. A caller with no balance drove the whole
    # warehouse cost, was refused at the very end, and paid for none of it, as
    # often as it cared to ask. The gate has to sit above the work it prices.
    ("risk: the surface is gated AFTER the warehouse it pays for (original)", [
        (RL, '\tsurface, err := price("search", windowScreens(lookback))\n\tif err != nil {\n\t\treturn report{}, err\n\t}\n',
             '\tvar surface func(int)\n'),
        (RL, '\tsurface(windowScreens(lookback))\n\tif err != nil {\n\t\treturn report{}, err\n\t}\n',
             '\tsurface, gerr := price("search", windowScreens(lookback))\n\tif gerr != nil {\n\t\treturn report{}, gerr\n\t}\n\tsurface(windowScreens(lookback))\n\tif err != nil {\n\t\treturn report{}, err\n\t}\n')],
     "TestSearch_ARefusedCallerNeverReachesTheWarehouse", PR),
    ("risk: only the grid is priced, so the surface read is free", [
        (RL, '\tsurface, err := price("search", windowScreens(lookback))\n\tif err != nil {\n\t\treturn report{}, err\n\t}\n', ''),
        (RL, '\tsurface(windowScreens(lookback))\n', '')],
     "TestSearch_BothHalvesArePricedForWhatTheyAre", PR),

    ("risk: a refused search never releases its claim", [
        (RL, '\tstarted := false\n\tdefer func() {\n\t\tif !started {\n\t\t\tp.unclaim(t)\n\t\t}\n\t}()\n', '\tstarted := false\n\t_ = started\n'),
        (RL, '\tp.settle(t, pending)\n\tstarted = true\n', '\tp.settle(t, pending)\n')],
     "TestSearch_ARefusedRunReleasesTheSlot", PR),

    # ── risk: the strain report ──────────────────────────────────────────────
    # velocity caps PER SHARD (five keys against a census ceiling of 320), so the
    # store drops a tenant's subjects long before the flat census notices.
    # Measured: 200 subjects in, store holds 198, lost=0, SATURATED=false — two of
    # that org's own subjects read as "has done nothing" and nothing said so.
    ("risk: strain ignores the store's own shedding (original)", [
        (RG, 'Saturated: r.lost > 0 || r.shed > 0 || r.order.Len() >= ringKeyCeiling',
             'Saturated: r.lost > 0 || r.order.Len() >= ringKeyCeiling')],
     "TestStrain_", PR),
    ("risk: the forgotten COUNT ignores the store's own shedding", [
        (RG, 'Forgotten: r.lost + int64(r.shed),', 'Forgotten: r.lost,')],
     "TestStrain_", PR),
    ("risk: reconcile stops measuring the store/census shortfall", [
        (RG, '\tif short := r.order.Len() - r.live; short > r.shed {\n\t\tr.shed = short\n\t}\n', '')],
     "TestStrain_", PR),
    ("risk: forgetting becomes a gauge that falls back to zero", [
        (RG, '\tif short := r.order.Len() - r.live; short > r.shed {\n\t\tr.shed = short\n\t}\n',
             '\tr.shed = r.order.Len() - r.live\n')],
     "TestStrain_ForgettingIsNotUndone", PR),

    # ── risk: the resident bound ─────────────────────────────────────────────
    # Nothing held this mechanism AT ALL: evict could be made to return nil
    # unconditionally and the whole suite stayed green. It is what keeps 64 ×
    # (8 MiB of rings + its model) inside a 9 GiB GOMEMLIMIT on a ONE-replica
    # Recreate deployment, so disarming it is an OOM and an OOM is a total outage.
    # Disarmed via the CONDITION, not an early return: `return nil` after the guard
    # is unreachable code, which go vet rejects — so it would score NO-COMPILE and
    # prove nothing. The bound that never trips is the honest mutant.
    ("risk: the resident bound is disarmed (residents grow without limit)", [
        (RL, 'func (p *plane) evict() *resident {\n\tif len(p.res) < maxResident {',
             'func (p *plane) evict() *resident {\n\tif len(p.res) >= 0 {')],
     "TestResident_TheBoundHasAnOperatingPoint", PR),
    ("risk: past the bound the next organisation is REFUSED, not served", [
        (RL, '\tp.mu.Lock()\n\tp.built++\n\tgone := p.evict()',
             '\tp.mu.Lock()\n\tif len(p.res) >= maxResident {\n\t\tp.mu.Unlock()\n\t\treturn nil, zip.Errorf(429, "the plane is full")\n\t}\n\tp.built++\n\tgone := p.evict()')],
     "TestResident_TheBoundHasAnOperatingPoint", PR),
    ("risk: an evicted organisation is dropped without writing its state down", [
        (RL, '\t\tif err := p.save(gone); err != nil {', '\t\tif err := error(nil); err != nil {')],
     "TestResident_TheBoundHasAnOperatingPoint", PR),

    # ── ml: the serving plane's CAPACITY ─────────────────────────────────────
    # The probe read only "is the CRD served", and kserve admits an
    # InferenceService no runtime supports and then never schedules it — so the
    # cluster answered 200 while every deploy hung. The cluster carried twelve
    # ClusterServingRuntimes, ten for backends nothing had ever deployed, and
    # purging them is a legitimate act: doing it INVISIBLY is the defect. The
    # first mutant is the state the probe shipped in.
    ("ml: the serving probe stops reading capacity (CRD served == healthy)", [
        (ML, '\t\tif capacity.Resource != "" {', '\t\tif false {')],
     "TestServingHealthReportsRuntimeCapacity", PML),
    ("ml: an unreadable runtime list is folded into the count as zero", [
        (ML, '\t\t\t\tres[capacity.Resource], allOK = err.Error(), false',
             '\t\t\t\tres[capacity.Resource], allOK = 0, false')],
     "TestServingHealthSeparatesAnUnreadableRuntimeListFromAnEmptyOne", PML),
    ("ml: the runtime coordinate is read at the InferenceService's version", [
        (ML, 'runtimeGVR = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: "clusterservingruntimes"}',
             'runtimeGVR = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1beta1", Resource: "clusterservingruntimes"}')],
     "TestGVRs", PML),

    # ── closure: the witness that says what a document was generated FROM ────
    # A go.mod bump silently invalidates a committed document, and nothing in the
    # tree could see it: hanzoai/iam v1.34.21 -> v1.34.29 added EnableCodeSignin,
    # the commit touched go.mod and apps/iam only, and plugin/iam/openapi.json went
    # stale on main. Every row here breaks one property that detection rests on.
    ("closure: witness the main module too", [
        (CL, '\treturn p != nil && p.Module != nil && p.Module.Path != mainModule',
             '\treturn p != nil && p.Module != nil')],
     "TestMainModuleSourceIsNotWitnessed", PCL),

    ("closure: witness the module VERSION instead of its source", [
        (CL, '\th := sha256.New()\n\tfor _, f := range files {',
             '\th := sha256.New()\n\tfmt.Fprint(h, p.Module.Version)\n\tfor _, f := range files[:0] {')],
     "TestVersionAloneIsNotStaleness", PCL),

    ("closure: stop recording the module versions the message names", [
        (CL, '\t\t\tw.Modules[d.Module.Path] = d.Module.Version\n', '')],
     "TestVersionAloneIsNotStaleness", PCL),

    ("closure: report only the FIRST stale app (the masking this replaces)", [
        (CL, '\tsort.Strings(stale)\n',
             '\tsort.Strings(stale)\n\tif len(stale) > 1 {\n\t\tstale = stale[:1]\n\t}\n')],
     "TestEveryStaleAppIsReportedInOnePass", PCL),

    ("closure: name the fleet sweep however little moved", [
        (CL, '\tif len(stale)*3 > total {', '\tif true {')],
     "TestReportCarriesTheScopedRepair", PCL),

    ("closure: hash contents without filenames, so a rename is invisible", [
        (CL, '\t\tfmt.Fprintf(h, "%s\\x00%d\\x00", f, len(b))',
             '\t\tfmt.Fprintf(h, "%d\\x00", len(b))')],
     "TestARenameMovesTheDigest", PCL),

    ("closure: witness an app that has no document", [
        (CL, '\t\tif _, err := os.Stat(filepath.Join(root, "plugin", app, "openapi.json")); err != nil {\n\t\t\tcontinue // no document to be stale\n\t\t}\n', '')],
     "TestAppWithoutADocumentIsNotWitnessed", PCL),

    ("closure: witness an app that cannot regenerate its own document", [
        (CL, '\t\tif !can[app] {\n\t\t\tcontinue\n\t\t}\n', '')],
     "TestAnAppThatCannotDescribeItselfIsNotWitnessed", PCL),

    ("closure: default the describable set instead of refusing", [
        (CL, '\tif len(describable) == 0 {', '\tif false {')],
     "TestWritingTheWitnessRefusesWithoutTheDescribableSet", PCL),

    ("closure: let an empty comparison report green", [
        (CL, '\tif len(have.Apps) == 0 {\n\t\treturn fmt.Errorf("no app documents were compared — the gate has nothing to check, which is a defect in the gate and never a pass")\n\t}\n\n', '')],
     "TestAnEmptyComparisonIsNotAPass", PCL),

    ("closure: hash an unplaceable package to a constant instead of failing", [
        (CL, '\tif p.Dir == "" {\n\t\treturn "", fmt.Errorf("%s: the toolchain could not locate this package — run `go mod download` (a missing module cannot be witnessed, and hashing nothing would compare equal)", p.ImportPath)\n\t}\n\n', '')],
     "TestAnUnresolvedDependencyIsAnErrorNotADigest", PCL),

    ("closure: walk only DIRECT imports, never the closure", [
        (CL, '\t\tif p := byPath[path]; p != nil {\n\t\t\tfor _, imp := range p.Imports {\n\t\t\t\twalk(imp)\n\t\t\t}\n\t\t}\n', '')],
     "TestReachWalksTransitivelyAndTerminates", PCL),

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
    # A LEFTOVER BACKUP MEANS THE TREE IS ALREADY MUTATED. The restore is in a
    # finally, which does not run when the process is killed — a CI timeout, a
    # ^C — so an interrupted run leaves the mutant in the tree and the original
    # in .mutbak. Copying over that backup then destroys the only clean copy,
    # and the run reports ANCHOR-MISS on a tree it has silently corrupted. Refuse
    # instead, and say how to recover.
    for f in files:
        bak = ROOT / (f + ".mutbak")
        if bak.exists():
            sys.exit(f"{f}.mutbak exists — an earlier run was interrupted and {f} is still MUTATED.\n"
                     f"Recover the original first:  mv {bak} {ROOT / f}")
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
