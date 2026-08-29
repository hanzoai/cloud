# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package research

struct GrantRequest {
    Project     text @0
    ID          text @8
    SHA256      text @16
    Visibility  text @24
    Trainable   bool @32
    Publishable bool @33
}

struct IngestRequest {
    Experiments list<bytes> @0
    Attempts    list<bytes> @8
}

struct ResearchArtifact {
    SHA256         text  @0
    Content        text  @8
    Kind           text  @16
    Ref            text  @24
    RunID          text  @32
    Project        text  @40
    Visibility     text  @48
    RetentionClass text  @56
    GitSHA         text  @64
    GitBranch      text  @72
    GitDirty       bool  @80
    LibVersions    bytes @88
    TS             i64   @96
}

struct ResearchTotals {
    Project             text        @0
    Projects            i64         @8
    Experiments         i64         @16
    ExperimentsRetained i64         @24
    Attempts            i64         @32
    AttemptsRetained    i64         @40
    Models              i64         @48
    Benchmarks          i64         @56
    CostUSD             f64         @64
    ByKind              list<bytes> @72
}

struct artifactOut {
    SHA256   text @0
    Ref      text @8
    Created  bool @16
    RolledUp bool @17
}

struct artifactsIn {
    Project text @0
    Run     text @8
    Since   i64  @16
}

struct artifactsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct experimentsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct grantOut {
    Updated i64 @0
}

struct ingestOut {
    Project              text @0
    ExperimentsIngested  i64  @8
    AttemptsIngested     i64  @16
    CanonicalExperiments i64  @24
    ExperimentsRetained  i64  @32
    CanonicalAttempts    i64  @40
    AttemptsRetained     i64  @48
    RolledUp             bool @56
}

struct listIn {
    Project text @0
    Kind    text @8
}

struct projectsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct totalsIn {
    Project text @0
}

interface research {
    # Returns the caller org's research-diary feed newest-first —
    # the snapshots and reports tied to its runs, as metadata and content addresses;
    # the bytes themselves are fetched by hash. ?run= narrows to one run, ?project=
    # to one project (default the caller's project scope), and ?since= to a unix second.
    get_research_artifacts(req: artifactsIn) returns (rep: artifactsOut)
    # Returns the caller org's CANONICAL experiments — the deterministic
    # deduped view over the versioned history. With no ?project= it reads the org's
    # whole set across projects (the ops board's cross-project view, since a project is
    # a sub-scope of the one tenant); ?project= narrows to one and ?kind= to one
    # discriminator.
    get_research_experiments(req: listIn) returns (rep: experimentsOut)
    # Returns every research project in the caller's org with its
    # real totals — canonical and retained side by side — which is the ops board's
    # "every project + real totals" view.
    get_research_projects() returns (rep: projectsOut)
    # Returns the caller org's headline aggregate plus a per-kind
    # breakdown — the observatory's poll target. Canonical and retained counts travel
    # together, so a deduped view never reads as loss. ?project= narrows to one project.
    get_research_totals(req: totalsIn) returns (rep: ResearchTotals)
    # Records one research-diary artifact — a board snapshot or a
    # generated report — CONTENT-ADDRESSED inside the trust boundary. The caller submits
    # the bytes as base64 `content`; the SERVER hashes them and THAT hash is the identity
    # and the ref, so the address can never be poisoned by a client-asserted one. A
    # client-supplied sha256, if present, must match the bytes. The project is the
    # SERVER's value and visibility is forced private. Re-posting the same bytes is a
    # no-op that reports created=false.
    post_research_artifacts(req: ResearchArtifact) returns (rep: artifactOut)
    # Appends one batch of experiment and attempt versions to the
    # caller org's evidence store, idempotently by content, then rolls it up to the
    # analytics plane best-effort. The project is the SERVER's value and visibility is
    # forced private — an upload grants no training or publication right, which is a
    # separate call. A run carrying a BYO endpoint is SSRF-checked before the store is
    # touched. The answer carries BOTH the canonical (deduped) and retained (full
    # history) counts, so a caller sees the versioned truth rather than a dedup that
    # reads as loss.
    post_research_experiments(req: IngestRequest) returns (rep: ingestOut)
    # Records the SEPARATE authorization an upload never
    # implies: a record's visibility (private, org or public) and, for a run, its
    # training and commons-publication consent. Address a run by its stable id or an
    # artifact by its sha256; an artifact grant sets visibility only. The ORG is the
    # tenant boundary and comes from the validated principal, so a caller can only ever
    # grant within its own org; `project` locates WHICH record inside it and defaults to
    # the caller's project scope.
    post_research_grants(req: GrantRequest) returns (rep: grantOut)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   IngestRequest.Attempts  research.Attempt (list element)
#   IngestRequest.Experiments  research.Experiment (list element)
#   ResearchTotals.ByKind  research.KindTotal (list element)
#   artifactsOut.Data  research.ResearchArtifact (list element)
#   experimentsOut.Data  research.Experiment (list element)
#   projectsOut.Data  research.ProjectSummary (list element)
