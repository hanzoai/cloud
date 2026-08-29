# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package sbom

struct SbomHealth {
    Datastore bool @0
    Service   text @8
    Status    text @16
    Table     text @24
}

struct SbomIngest {
    ImageDigest text  @0
    ImageRef    text  @8
    SourceRepo  text  @16
    GitSha      text  @24
    Format      text  @32
    Document    bytes @40
}

struct SbomIngested {
    ComponentCount i64  @0
    ImageDigest    text @8
}

struct SbomRef {
    Ref text @0
}

struct SbomView {
    ImageDigest    text        @0
    ImageRef       text        @8
    SourceRepo     text        @16
    GitSha         text        @24
    IngestedAt     text        @32
    ComponentCount i64         @40
    Truncated      bool        @48
    Components     list<bytes> @56
}

interface sbom {
    # Resolve returns everything inside one container image, addressed by its digest
    # or by its image ref.
    # Each component comes back with its name, version, type, package URL and license.
    # This read is GLOBAL, not tenant-scoped, and deliberately so: a bill of materials
    # belongs to a content-addressed digest rather than to an org, so every caller
    # deploying the same image resolves the same components, and nothing tenant-owned
    # is exposed by it. It still requires an attested caller — global is not public —
    # and answers 403 without one. Ingest is the closed half of the pair.
    # A miss is not the end of the lookup. The registry is the source of truth, so an
    # unmaterialized ref is pulled from the SBOM attached to that image, persisted, and
    # answered from the store — the first read of a freshly built image pays for the
    # pull, later ones do not. The pull reads OUR registries and nothing else, which is
    # what makes one shared answer trustworthy for every tenant: an attached document
    # is whoever controls that repository speaking, so a ref outside them answers 404
    # rather than a stranger's account of what is in their image. A bare digest with no
    # repository is not pullable and answers an honest 404, as does a ref with no
    # attached document. Repeated ingests collapse to the latest, components come back
    # ordered by type then name, and a result over 5000 components is capped with
    # `truncated` set. When the datastore is not connected the answer is 503 rather
    # than a fabricated empty image.
    get_sbom_by_wildcard1(req: SbomRef) returns (rep: SbomView)
    # Health is a pure liveness probe: the service is up; datastore reflects whether
    # the datastore store is connected. Not JWT-gated, always 200 (a disconnected
    # datastore is degraded-but-alive; the data endpoints report that as 503).
    get_sbom_health() returns (rep: SbomHealth)
    # Ingest persists a CycloneDX SBOM's components keyed by image digest. Gated to a
    # validated SuperAdmin (owner == AdminOrg) — the canonical cloud super-admin
    # check, which the build fleet / CI carries. Re-ingest is idempotent: rows share
    # the (digest, name, version, purl) ORDER BY, so ReplacingMergeTree keeps the
    # latest by ingested_at (and resolve reads FINAL).
    post_sbom(req: SbomIngest) returns (rep: SbomIngested)
}

# ---------------------------------------------------------------------
# 3 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   SbomView.Components  sbom.SbomComponent (list element)
