# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package dataset

struct riskDataset {
    Name      text  @0
    Version   i64   @8
    At        text  @16
    By        text  @24
    Status    text  @32
    Running   bool  @40
    Refusal   text  @48
    Digest    text  @56
    Spec      bytes @64
    Counts    bytes @72
    Share     i64   @80
    Truncated bool  @88
    Oversize  i64   @96
}

struct riskDatasetDisposal {
    Dataset  text @0
    Versions i64  @8
    Rows     i64  @16
}

struct riskDatasetDisposeIn {
    Name text @0
}

struct riskDatasetList {
    Items list<bytes> @0
}

struct riskDatasetRef {
    Name text @0
}

struct riskDatasetRows {
    Dataset text        @0
    Version i64         @8
    Digest  text        @16
    Dims    list<text>  @24
    Offset  i64         @32
    Limit   i64         @40
    Rows    list<bytes> @48
}

struct riskDatasetSpec {
    Name    text       @0
    Kind    text       @8
    Dims    list<text> @16
    From    text       @24
    To      text       @32
    Horizon i64        @40
    Cuts    list<text> @48
    Seed    text       @56
    Rows    i64        @64
}

struct riskDatasetVersions {
    Name  text        @0
    Items list<bytes> @8
}

struct riskExportIn {
    Name    text @0
    Version i64  @8
    Split   text @16
    Offset  i64  @24
    Limit   i64  @32
}

struct riskLineage {
    Dataset      text @0
    Version      i64  @8
    Source       text @16
    From         text @24
    To           text @32
    Rows         i64  @40
    Subjects     i64  @48
    Share        i64  @56
    Oversize     i64  @64
    Holds        i64  @72
    Retention    text @80
    Digest       text @88
    Reproducible bool @96
    Refusal      text @104
}

struct riskLineageIn {
    Name    text @0
    Version i64  @8
}

struct riskMaterializeIn {
    Name text @0
}

interface dataset {
    # Declares the next version of a dataset from a bound query over
    # this org's own feature surface.
    # It mints a VERSION and writes no rows: a version is declared, then materialised
    # once, then never rewritten. Version numbers are monotone and never reused, so
    # "version 3 of signups" means one thing forever — which is the whole reason a
    # model can cite one.
    # The window is bounded by the source's retention, the horizon by a year, the
    # rows by the plane's cap, and the number of datasets and versions per org by
    # their own limits. Every refusal names which bound it hit.
    riskCreateDataset(req: riskDatasetSpec) returns (rep: riskDataset)
    # Dataset describes every version of one dataset, newest first — the whole
    # history, because the point of a version is that the older ones are still there
    # and a model fitted last quarter cites one of them.
    # A name this org does not own answers 404, exactly as an unknown name does, so a
    # probe learns nothing about another tenant's datasets.
    riskDataset(req: riskDatasetRef) returns (rep: riskDatasetVersions)
    # Shows where a version's rows came from and whether that can
    # still be demonstrated.
    # The answer is MEASURED, not recalled: the plane asks the source the same
    # bounded question again and compares it to the fingerprint taken when the
    # version was built. Anything but exact agreement is reported as drift — the
    # source is fed by a rollup that runs behind the events, so "it holds more now"
    # is the ordinary case and it means re-running the spec would not reproduce this
    # version. An admitted gap is actionable; an unfalsifiable claim is not.
    # IT IS A PRICED, BOUNDED READ, because it is the same statement a
    # materialisation is charged for: an exact distinct-count over up to 400 days of
    # this org's feature surface. It takes the org's ONE source-scan slot, so a
    # tenant looping it spends one scan and not a thousand; it counts against the
    # plane's ceiling, so the fleet's warehouse is bounded too; and it runs under
    # this plane's own deadline rather than the caller's patience.
    riskDatasetLineage(req: riskLineageIn) returns (rep: riskLineage)
    # Datasets lists this org's datasets, each with its newest version. An org that
    # has declared none gets an empty list; a store that cannot be reached gets a
    # refusal, never an empty list, because the two read identically and only one of
    # them is true.
    riskDatasets() returns (rep: riskDatasetList)
    # Disposes of one dataset and every version of it: the rows are
    # dropped and the register is marked with what went.
    # This is the ONLY expiry in this plane. Neither table carries a TTL, deliberately:
    # a table TTL is a fleet-wide clock no tenant can hold longer or shorten, which is
    # the opposite of a retention decision belonging to the tenant whose records they
    # are. The drop is a partition drop on (org, dataset), so the tenant is the first
    # component of the thing being dropped and a disposal cannot be spelled across one.
    # The BYTES are what goes. The register keeps one `disposed` row per version — the
    # name, the number, the spec, the digest and who disposed of it when — for two
    # reasons: a retention obligation is answered by a record of the deletion, not by
    # silence; and version numbers must stay monotone, so that after `orders` is
    # disposed of and declared again the next version is 4 and not 1. A number that
    # could be reused would make every citation of `orders v3` ambiguous forever.
    # It is not reversible and there is no soft state in between. A version a model
    # cited has no rows once this returns, and every read of it says so.
    riskDeleteDataset(req: riskDatasetDisposeIn) returns (rep: riskDatasetDisposal)
    # Reads a published version's rows back, one bounded page at a
    # time, in the version's own stable row order.
    # Only a published version can be exported. Rows written by an attempt that never
    # completed are inert — no register row names them — and they are disposed of with
    # the dataset.
    riskExportDataset(req: riskExportIn) returns (rep: riskDatasetRows)
    # Builds the declared version into immutable rows and answers
    # 202 as soon as the attempt is on record.
    # It never holds the request open for the work: a materialisation is a bounded
    # warehouse scan, and letting an HTTP client's timeout be a data plane's timeout
    # is how one tenant's retry loop becomes everyone's outage. ONE materialisation
    # runs per org at a time; a second is refused rather than queued, because a queue
    # admits the same work later and the honest answer to "again" while one is
    # running is that one is running.
    # Only a DECLARED version is admitted. A published version is immutable, and a
    # version whose earlier attempt did not complete is never re-attempted — that
    # would union two runs' rows under one number and make the digest a lie. In both
    # cases the answer is to declare a new version, which is what a second run over a
    # moving source honestly is.
    riskMaterializeDataset(req: riskMaterializeIn) returns (rep: riskDataset)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (5) — crosses, arrives without its name:
#   riskDataset.Counts  dataset.riskSplitCounts
#   riskDataset.Spec  dataset.riskDatasetSpec
#   riskDatasetList.Items  dataset.riskDataset (list element)
#   riskDatasetRows.Rows  dataset.riskDatasetRow (list element)
#   riskDatasetVersions.Items  dataset.riskDataset (list element)
