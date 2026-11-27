# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package research

struct Action {
    ID           text  @0
    Hash         text  @8
    Action       bytes @16
    DecisionID   text  @24
    Assignee     text  @32
    Status       text  @40
    AuthorizedBy text  @48
    AuthorizedAt text  @56
    Time         text  @64
    By           text  @72
}

struct Commitment {
    ID             text @0
    DecisionID     text @8
    EvaluationID   text @16
    EvaluationHash text @24
    Selection      text @32
    Note           text @40
    By             text @48
    Time           text @56
}

struct Comparison {
    A          bytes       @0
    B          bytes       @8
    Comparable bool        @16
    Blocks     list<text>  @24
    Config     list<bytes> @32
    Deltas     list<bytes> @40
}

struct Decision {
    ID         text        @0
    DecisionID text        @8
    Decision   bytes       @16
    Charter    bytes       @24
    Program    bytes       @32
    Version    i64         @40
    Supersedes text        @48
    Depends    list<bytes> @56
    Time       text        @64
    By         text        @72
    Visibility text        @80
}

struct Evaluation {
    ID             text        @0
    Hash           text        @8
    DecisionID     text        @16
    EvaluationRun  bytes       @24
    Package        bytes       @32
    Recommendation bytes       @40
    Depends        list<bytes> @48
    Time           text        @56
    By             text        @64
    Visibility     text        @72
}

struct Evidence {
    ID         text        @0
    URI        text        @8
    Hash       text        @16
    Kind       text        @24
    Source     bytes       @32
    Links      list<bytes> @40
    Content    bytes       @48
    Time       text        @56
    Version    i64         @64
    By         text        @72
    Visibility text        @80
}

struct EvolutionCandidate {
    Project          text        @0
    CandidateID      text        @8
    ParentIDs        list<text>  @16
    SpecVersion      text        @24
    MutationSurface  text        @32
    ArtifactDigest   text        @40
    DiffRef          text        @48
    Proposer         text        @56
    Contributors     list<bytes> @64
    CreatedAt        text        @72
    GateRef          text        @80
    EvidenceRefs     list<text>  @88
    Signature        text        @96
    Attestations     list<bytes> @104
    ProductFitness   bytes       @112
    EvolutionFitness bytes       @120
    Status           text        @128
    WasQualified     bool        @136
    QualifiedBy      text        @144
    QualifiedAt      text        @152
    RollbackTarget   text        @160
    RetractedBy      text        @168
    Seq              i64         @176
}

struct GrantRequest {
    Project     text       @0
    Kind        text       @8
    ID          text       @16
    SHA256      text       @24
    Visibility  list<text> @32
    Trainable   list<bool> @40
    Publishable list<bool> @48
}

struct HypothesisRecord {
    Project        text       @0
    HypothesisID   text       @8
    Statement      text       @16
    NullHypothesis text       @24
    GateRef        text       @32
    DatasetRefs    list<text> @40
    Budget         bytes      @48
    Proposer       text       @56
    RegisteredAt   text       @64
    Seq            i64        @72
}

struct IngestRequest {
    Experiments list<bytes> @0
    Attempts    list<bytes> @8
}

struct Or_github_com_hanzoai_cloud_apps_research_runsOut_github_com_hanzoai_cloud_apps_research_runsSummary_ {
    A bytes @0
    B bytes @8
}

struct RegressionRecord {
    Project         text @0
    RegressionID    text @8
    SourceKind      text @16
    ErrorSignature  text @24
    Summary         text @32
    FailingRunID    text @40
    GateTestRef     text @48
    FixCandidateRef text @56
    Status          text @64
    ReportedBy      text @72
    CreatedAt       text @80
    UpdatedAt       text @88
    Seq             i64  @96
}

struct ResearchArtifact {
    SHA256           text  @0
    ContentSHA256    text  @8
    ArtifactID       text  @16
    MediaType        text  @24
    Canonicalization text  @32
    UncompressedSize i64   @40
    Content          text  @48
    Kind             text  @56
    Ref              text  @64
    RunID            text  @72
    Project          text  @80
    Visibility       text  @88
    RetentionClass   text  @96
    GitSHA           text  @104
    GitBranch        text  @112
    GitDirty         bool  @120
    LibVersions      bytes @128
    ReceivedAt       i64   @136
    TS               i64   @144
}

struct ResearchClaim {
    Project         text        @0
    ClaimID         text        @8
    HypothesisID    text        @16
    RunID           text        @24
    CandidateRef    text        @32
    Mode            text        @40
    Metric          text        @48
    Value           f64         @56
    EffectSize      f64         @64
    ConfidenceLo    list<f64>   @72
    ConfidenceHi    list<f64>   @80
    Proposer        text        @88
    Contributors    list<bytes> @96
    EvidenceRefs    list<text>  @104
    Status          text        @112
    WasReplicated   bool        @120
    Replications    list<bytes> @128
    CounterEvidence bytes       @136
    CreatedAt       text        @144
    UpdatedAt       text        @152
    Seq             i64         @160
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

struct StandardManifest {
    SpecVersion     text        @0
    Component       text        @8
    Version         text        @16
    ImplementedHIPs list<bytes> @24
    Capabilities    list<text>  @32
}

struct actionsIn {
    ID       text @0
    Decision text @8
    Status   text @16
}

struct actionsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct artifactOut {
    SHA256           text @0
    ContentSHA256    text @8
    Ref              text @16
    Kind             text @24
    ArtifactID       text @32
    MediaType        text @40
    UncompressedSize i64  @48
    ReceivedAt       i64  @56
    Created          bool @64
    RolledUp         bool @65
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

struct authorizeIn {
    ID   text @0
    Hash text @8
}

struct benchmarkBatch {
    Benchmarks list<bytes> @0
}

struct benchmarksIn {
    Project text @0
    ID      text @8
}

struct benchmarksOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct candidatesIn {
    Project         text @0
    ID              text @8
    MutationSurface text @16
    Status          text @24
}

struct candidatesOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct claimsIn {
    Project      text @0
    ID           text @8
    HypothesisID text @16
    Status       text @24
}

struct claimsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct commitmentsIn {
    Decision text @0
}

struct commitmentsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct compareIn {
    A       text @0
    B       text @8
    Project text @16
}

struct decisionsIn {
    ID       text @0
    Decision text @8
    Org      text @16
}

struct decisionsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct dependentsIn {
    URI  text @0
    Hash text @8
}

struct dependentsOut {
    URI         text        @0
    Newest      text        @8
    Decisions   list<bytes> @16
    Evaluations list<bytes> @24
}

struct evaluationsIn {
    ID       text @0
    Decision text @8
    Org      text @16
}

struct evaluationsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct evidenceIn {
    URI text @0
    Org text @8
}

struct evidenceOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct experimentVersionsOut {
    Project  text        @0
    ID       text        @8
    Versions list<bytes> @16
}

struct experimentsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct grantOut {
    Updated i64 @0
}

struct hypothesesIn {
    Project text @0
    ID      text @8
}

struct hypothesesOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct idIn {
    ID text @0
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
    Tag     text @16
}

struct paperBatch {
    Papers list<bytes> @0
}

struct papersIn {
    Project text @0
    ID      text @8
    Study   text @16
    Cites   text @24
}

struct papersOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct projectsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct qualifyCandidateIn {
    ID               text       @0
    EvidenceRefs     list<text> @8
    ProductFitness   bytes      @16
    EvolutionFitness bytes      @24
    Promote          bool       @32
    RollbackTarget   text       @40
    Signature        text       @48
}

struct recordBatch {
    Project   text @0
    Recorded  i64  @8
    Canonical i64  @16
}

struct regressionsIn {
    Project text @0
    Status  text @8
}

struct regressionsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct replicateClaimIn {
    ID           text      @0
    RunID        text      @8
    Value        list<f64> @16
    ConfidenceLo list<f64> @24
    ConfidenceHi list<f64> @32
    Tolerance    list<f64> @40
}

struct retractIn {
    ID          text @0
    EvidenceRef text @8
    RunID       text @16
    Summary     text @24
}

struct runBatch {
    Runs list<bytes> @0
}

struct runsIn {
    Project    text @0
    ID         text @8
    Key        text @16
    Benchmark  text @24
    System     text @32
    Study      text @40
    Completion text @48
    Since      text @56
    Until      text @64
    Org        text @72
    Category   text @80
    Metric     text @88
    View       text @96
    Limit      i64  @104
    Cursor     text @112
}

struct studiesIn {
    Project text @0
    ID      text @8
}

struct studiesOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct studyBatch {
    Studies list<bytes> @0
}

struct sysmlIn {
    Project  text        @0
    Commit   text        @8
    Elements list<bytes> @16
}

struct totalsIn {
    Project text @0
}

struct versionsPathIn {
    ID      text @0
    Project text @8
}

struct versionsQueryIn {
    ID      text @0
    Project text @8
}

struct xpIn {
    Project   text @0
    Principal text @8
    Domain    text @16
}

interface research {
    # Answers the org's actions, newest first, each in its latest
    # state with its hash. ?decision=, ?status= and ?id= narrow it. Actions are never
    # public.
    get_research_actions(req: actionsIn) returns (rep: actionsOut)
    # Returns the caller org's research-diary feed newest-first —
    # the snapshots and reports tied to its runs, as metadata and content addresses;
    # the bytes themselves are fetched by hash. ?run= narrows to one run, ?project=
    # to one project (default the caller's project scope), and ?since= to a unix second.
    get_research_artifacts(req: artifactsIn) returns (rep: artifactsOut)
    # Returns the caller org's benchmark definitions — what
    # each task is, which dataset it draws on under which licence, the splits it
    # declares and the metric its authors score it by. ?id= narrows to one, which is
    # how one definition is read; ?project= narrows to one project.
    get_research_benchmarks(req: benchmarksIn) returns (rep: benchmarksOut)
    # Returns the org's canonical EvolutionCandidates,
    # newest sequence number first.
    get_research_candidates(req: candidatesIn) returns (rep: candidatesOut)
    # Computes the multi-objective non-dominated Pareto
    # frontier over active EvolutionCandidates with zero safety violations
    # (HIP-1401 §4.1), maximizing verified_success and robustness while minimizing
    # cost_per_success_usd and latency_p50_ms.
    get_research_candidates_frontier(req: candidatesIn) returns (rep: candidatesOut)
    # Returns the org's canonical ResearchClaims newest-first.
    get_research_claims(req: claimsIn) returns (rep: claimsOut)
    # Answers the org's commitments, newest first. ?decision=
    # narrows to one decision. Commitments are never public.
    get_research_commitments(req: commitmentsIn) returns (rep: commitmentsOut)
    # Reads two runs side by side and states FIRST the reasons
    # they may not be compared — a different benchmark, a different split, different
    # dataset bytes, or either run not established as complete. It then names every
    # configuration field they disagree about, and only then the per-metric change
    # over what both of them actually measured, with whether the reported intervals
    # overlap.
    # This is the operation the record exists to serve. Putting two numbers next to
    # each other is already the claim that they are numbers of the same thing, and
    # that claim is what goes wrong: a delta between a run frozen at one commit and
    # a run frozen at another, or between a finished run and a checkpoint, is a
    # difference in something other than the system under test. The answer here
    # cannot be read without seeing which.
    get_research_compare(req: compareIn) returns (rep: Comparison)
    # Answers every version of the org's decisions, newest first, each
    # with its ordinal and the version it supersedes. ?decision= narrows to one DDT
    # decision and ?id= to one version. A caller with no org of its own reads the
    # decisions the org ?org= names granted public, and the 404 an unserved address
    # gets until that org has granted one.
    get_research_decisions(req: decisionsIn) returns (rep: decisionsOut)
    # Answers the org's evaluations, newest first, the latest filing
    # of each Evaluation Run. ?decision= narrows to one DDT decision, and ?id= reads
    # one filing, whether or not a later filing of its run replaced it. A caller with
    # no org of its own reads the evaluations the org ?org= names granted public, and
    # the 404 an unserved address gets until that org has granted one.
    get_research_evaluations(req: evaluationsIn) returns (rep: evaluationsOut)
    # Answers one uri's versions, newest first (?uri=), or every uri's
    # newest version, without content. A caller with no org of its own reads the
    # evidence the org ?org= names granted public, and the 404 an unserved address
    # gets until that org has granted some; a version filed after a grant is private
    # until it is granted too.
    get_research_evidence(req: evidenceIn) returns (rep: evidenceOut)
    # Answers one version of a piece of evidence by its record id, with
    # its content.
    get_research_evidence_by_id(req: idIn) returns (rep: Evidence)
    # Answers the decisions and evaluations whose depends names a
    # uri (?hash= narrows to those that read that version), each with the hash it
    # read and stale: true when that hash is not the uri's newest version. A
    # decision names a uri without a version unless its DDT record gives one, so it
    # is never stale by hash; its evaluations are.
    get_research_evidence_dependents(req: dependentsIn) returns (rep: dependentsOut)
    # Returns the caller org's CANONICAL experiments — the deterministic
    # deduped view over the versioned history. With no ?project= it reads the org's
    # whole set across projects (the ops board's cross-project view, since a project is
    # a sub-scope of the one tenant); ?project= narrows to one, ?kind= to one
    # discriminator, and ?tag= to one label.
    get_research_experiments(req: listIn) returns (rep: experimentsOut)
    # Returns every retained version of an experiment stable id
    # addressed via URL-escaped `:id` in `/v1/research/experiments/:id/versions`,
    # ordered ascending by server `seq`.
    get_research_experiments_by_id_versions(req: versionsPathIn) returns (rep: experimentVersionsOut)
    # Returns every retained version of an experiment stable id
    # (`?id=<stable_id>`), ordered ascending by server `seq` so anyone can audit that a
    # `planning` or `running` pre-registration preceded its `complete` measurement.
    get_research_experiments_versions(req: versionsQueryIn) returns (rep: experimentVersionsOut)
    # Returns the org's pre-registered HypothesisRecords newest-first,
    # each carrying its server-assigned sequence number so confirmatory run ordering
    # is auditable.
    get_research_hypotheses(req: hypothesesIn) returns (rep: hypothesesOut)
    # Returns the caller org's papers and the runs each cites.
    # ?id= narrows to one, ?study= to the papers reporting one study, and ?cites= to
    # the papers resting on one run — which is how a correction finds everything it
    # invalidates.
    get_research_papers(req: papersIn) returns (rep: papersOut)
    # Returns every research project in the caller's org with its
    # real totals — canonical and retained side by side — which is the ops board's
    # "every project + real totals" view.
    get_research_projects() returns (rep: projectsOut)
    # Returns the org's canonical HIP-1404 RegressionRecords
    # newest-first.
    get_research_regressions(req: regressionsIn) returns (rep: regressionsOut)
    # Returns the caller org's recorded executions with their
    # derived completion. ?id= narrows to one, which is how a single run is read;
    # ?benchmark=, ?system=, ?study=, ?since= and ?until= narrow the rest; and
    # ?completion=partial is the query this record was built to answer — which runs
    # were reported before they finished.
    # ?key= is the read a harness makes before it spends: every run carries `key`,
    # the sha256 of what decides its number (benchmark, split, system, version, k,
    # temperature, max_tokens, embedder, reader and the four digests), and a
    # complete run under the key it is about to measure is a measurement already
    # held. With ?completion=complete beside it the answer is empty or the hit.
    # Every listing also answers how much of itself is recorded: `completion` counts
    # the matched runs by the four completion values, and `gaps` names each field
    # some matched run leaves unstated with how many leave it unstated. Both are
    # computed over the runs the filter matched, so narrowing the listing narrows
    # the account of it.
    # ?id= and ?benchmark= each take a comma-separated list, any of which a run may
    # match. ?category= and ?metric= narrow each run's measures to the slices and
    # quantities named, and drop no run. ?view=summary serves each run with what a
    # page draws and nothing a harness re-runs it from. The listing comes in pages
    # of ?limit= runs (1 to 200, 100 when it names none) in its order, project then
    # id: `total` counts every matched run, and `next`, passed back as ?cursor= with
    # the same query, reads the page after; the last page carries none.
    # A caller with no org of its own, no credential at all, reads the runs the org
    # `?org=` names granted public (POST /v1/research/grants) and no others. Until
    # that org has published a run the answer is the 404 an address nobody serves
    # gets, so the read tells nobody which orgs use research; after, a private run is
    # absent from the listing exactly as a run never recorded is. That read serves an
    # Enso or Zen run without its version, notes, prompt, embedder or reader, an
    # enso.* benchmark's runs without notes, and no run of the base models those
    # families and Kai are built from; its `key` is derived over what it serves. The
    # org's own read is the full record.
    get_research_runs(req: runsIn) returns (rep: Or_github_com_hanzoai_cloud_apps_research_runsOut_github_com_hanzoai_cloud_apps_research_runsSummary_)
    # Returns the HIP-1400 component conformance manifest for
    # the research, evolution, evidence, and XP plane.
    get_research_standard() returns (rep: StandardManifest)
    # Returns the caller org's studies — the question each set
    # of runs was run to answer and what it found. ?id= narrows to one.
    get_research_studies(req: studiesIn) returns (rep: studiesOut)
    # Returns the caller org's headline aggregate plus a per-kind
    # breakdown — the observatory's poll target. Canonical and retained counts travel
    # together, so a deduped view never reads as loss. ?project= narrows to one project.
    get_research_totals(req: totalsIn) returns (rep: ResearchTotals)
    # Files a DDT Action as a DRAFT, with who it is assigned to. Anyone
    # may: Kai, an agent, a person. The DDT Action's own status, when it has one, is
    # DRAFT, and it carries no authorizedBy or authorizedAt (422); an action is
    # authorized by a person, never filed so. Filing it again
    # with other content or another assignee files its next state, with a new hash;
    # an action a person has authorized is not replaced by a draft (409).
    post_research_actions(req: Action) returns (rep: Action)
    # Moves an action to AUTHORIZED, the state the person read: the
    # request names that state's hash, and an action that has changed since — refiled
    # by an agent, or authorized by someone else — is refused 409. The read and the
    # write are one transaction, so of two people authorizing one draft at once one
    # is answered and the other refused 409. It writes AUTHORIZED into the stored DDT
    # Action and stamps the person who authorized it and when. Only a person signed
    # in with their own token, in their own org, may: an API key, an application or
    # an agent acting for someone is refused 403. The draft it replaces is kept.
    post_research_actions_by_id_authorize(req: authorizeIn) returns (rep: Action)
    # Records one research-diary artifact — a board snapshot,
    # report, manifest, harness, evidence, or result — CONTENT-ADDRESSED inside the trust
    # boundary. The caller submits the bytes as base64 `content`; the SERVER hashes them
    # and THAT hash is the identity and the ref, so the address can never be poisoned by
    # a client-asserted one. Client-supplied `sha256` or `content_sha256`, if present,
    # must match the bytes (422 otherwise). Empty `kind` defaults to `report`. Re-posting
    # the same bytes is a no-op that reports created=false.
    post_research_artifacts(req: ResearchArtifact) returns (rep: artifactOut)
    # Records benchmark definitions — the TASK a run is
    # measured against, with its dataset, that dataset's licence and origin, its
    # declared splits, its official metric and how to cite it. A definition is
    # independent of anyone's run and is recorded on its own: re-sending identical
    # content appends nothing, and changing any of it appends a new version beside
    # the old one rather than replacing it.
    post_research_benchmarks(req: benchmarkBatch) returns (rep: recordBatch)
    # Records a new EvolutionCandidate node in the
    # evolutionary lineage DAG (HIP-1401 §§1–2). Mutations outside the five
    # permitted surfaces (such as sandbox isolation, audit hashing, or evaluation
    # gates) are deterministically rejected 422. Every new candidate enters as
    # proposed and requires independent qualification to earn XP or promote.
    post_research_candidates(req: EvolutionCandidate) returns (rep: EvolutionCandidate)
    # Records independent holdout qualification (and
    # optional promotion) for an EvolutionCandidate (HIP-1401 §5). The qualifier
    # principal MUST be distinct from the proposer and contributors, safety MUST
    # equal 1.0 (zero violations), and a rollback target is pinned automatically.
    post_research_candidates_by_id_qualify(req: qualifyCandidateIn) returns (rep: EvolutionCandidate)
    # Files CounterEvidence retracting a previously
    # proposed or qualified EvolutionCandidate and reversing any derived XP
    # (HIP-1401 §5.2 & HIP-1402 §3).
    post_research_candidates_by_id_retract(req: retractIn) returns (rep: EvolutionCandidate)
    # Records a scientific claim backed by a completed Run
    # (HIP-1403 §1.2 & §3). A confirmatory claim MUST reference an existing
    # HypothesisRecord whose sequence number strictly precedes the supporting Run's
    # sequence number, and begins in the unreplicated state.
    post_research_claims(req: ResearchClaim) returns (rep: ResearchClaim)
    # Verifies an unreplicated confirmatory ResearchClaim
    # using an independent completed Run (HIP-1403 §3.1). Self-replication by the
    # proposer or any listed contributor is refused 403, and exploratory claims
    # cannot be replicated into confirmatory truth.
    post_research_claims_by_id_replicate(req: replicateClaimIn) returns (rep: ResearchClaim)
    # Files CounterEvidence against a ResearchClaim, marking it
    # retracted while preserving the full historical version chain and reversing any
    # derived science XP (HIP-1403 §4.2 & HIP-1402 §3).
    post_research_claims_by_id_retract(req: retractIn) returns (rep: ResearchClaim)
    # Commit records a person's commitment of a decision to an option, resting on
    # an evaluation filing when one is named; the server records that filing's hash,
    # and the filing stays readable by its id whatever is filed after it. Only a
    # person signed in with their own token, in their own org, may: an API key, an
    # application or an agent acting for someone is refused 403, as DDT's
    # agentPermissions.mayNotCreate DecisionCommitment says. The decision must be
    # filed, and a named evaluation filed for it.
    post_research_commitments(req: Commitment) returns (rep: Commitment)
    # Files a DDT-1.0 Decision record, with its charter and the program
    # bound from it when there are. The server checks the record's schemaVersion and
    # id, and derives depends from every evidence uri it names. Content that is the
    # decision id's newest version answers that version; any other content files its
    # next version, which names the one it supersedes — a revert to an older
    # version's content too, decided in one transaction against the newest, as
    # evidence versions are. A version that changes
    # the filed record's lifecycleState, its agentPermissions, or a member its
    # agentPermissions.mayNotChange names is a person's to file: an API key, an
    # application or an agent is refused 403, as DDT's agentPermissions say.
    post_research_decisions(req: Decision) returns (rep: Decision)
    # Files a DDT Evaluation Run with the Decision Package Kai
    # produced. The server checks that the run has an id; with a package, that the
    # package's hash is its content hash as Kai computes it (every member but hash
    # and trace), that the run's outputs.hash is that hash, so the package filed is
    # the one Kai sealed, that the run's inputs, when sent, are what the package
    # read, and that the run's outputs.readiness states the package's verdict. A run
    # is bound to one package: a run id beginning RUN_KAI_ is Kai's name for its
    # package's hash, and a filing naming another package hash under a run id already
    # filed is refused 409 — a new package is a new run. It lifts the recommendation
    # as Kai's DDT export does: the package's selection, ranking and flips, and
    # readiness, its verdict on the policy outputs.readiness names; depends is every
    # uri the package read (the run's inputs without one), with the hash it read. The
    # same run filed with the same content is the evaluation filed; with other
    # content it is another evaluation, and the earlier one is kept and readable by
    # its id.
    post_research_evaluations(req: Evaluation) returns (rep: Evaluation)
    # Files one version of a piece of evidence with its content. The
    # server hashes the content over its canonical JSON and keeps it in the
    # hash-addressed blob store; a hash sent beside it must agree, and evidence
    # without content is refused. Content whose hash is the uri's newest is that
    # version, answered as it stands; any other content is the uri's next version —
    # a revert to an older version's content too — and the decisions and evaluations
    # resting on the version it replaces are named on the bus as research.reconsider.
    # The answer is the version, with its ordinal for the uri and its content.
    post_research_evidence(req: Evidence) returns (rep: Evidence)
    # FileSysML files a SysML v2 model's elements as evidence, in one write. Each
    # element that is not a relationship becomes one `element` record, uri
    # sysml://<project>@<commit>#<@id>, hashed over its canonical JSON, the element
    # itself its content. A Satisfy, Verify, Derive, Refine or Dependency
    # relationship becomes a link on its source element's record (satisfies,
    # verifies, derives, refines, dependsOn) to its target's uri; other
    # relationships file nothing. The project may not hold `@` or `#` and the commit
    # is lowercase hex, so a uri names one element of one import. Every element that
    # changed publishes, together, one research.reconsider. The answer is the
    # records filed, without content.
    post_research_evidence_sysml(req: sysmlIn) returns (rep: evidenceOut)
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
    # the caller's project scope. Granting a decision, an evaluation or evidence (the
    # decision thread's kinds) is a person's act: any caller that is not a person
    # signed in as themselves, in their own org, is refused 403.
    post_research_grants(req: GrantRequest) returns (rep: grantOut)
    # RegisterHypothesis pre-registers one falsifiable HypothesisRecord (HIP-1403 §1)
    # before confirmatory runs begin. Re-submitting identical hypothesis content is
    # idempotent and preserves the original sequence number and timestamp.
    post_research_hypotheses(req: HypothesisRecord) returns (rep: HypothesisRecord)
    # Records writing that CITES runs. The edge points one way:
    # a paper names the run ids its claims rest on, and no run record is altered by
    # being cited — so a paper's numbers can be checked against the completion of
    # the executions they came from.
    post_research_papers(req: paperBatch) returns (rep: recordBatch)
    # Captures or advances a HIP-1404 error-to-fix
    # regression record. Supplying gate_test_ref advances status to gated; supplying
    # a qualified fix_candidate_ref advances status to fixed.
    post_research_regressions(req: RegressionRecord) returns (rep: RegressionRecord)
    # Records executions: what ran, against which task and split,
    # under which configuration, frozen at which commit, over which dataset bytes,
    # with the numbers it produced and — the field the rest is in service of —
    # whether it reached the end of the questions it set out to answer.
    # Completion is DERIVED from `questions`, `answered` and `ended`, never taken
    # from the body. A record that omits a count leaves completion `unknown` rather
    # than reading as finished, which is the whole difference between this and a
    # harness checkpoint published as a result. Consent is forced private: recording
    # a run grants no training or publication right, which is a separate call.
    post_research_runs(req: runBatch) returns (rep: recordBatch)
    # Records the questions runs were run to answer, and the
    # findings they reached. A study is what keeps a run from being read as a
    # standalone score.
    post_research_studies(req: studyBatch) returns (rep: recordBatch)
}

# ---------------------------------------------------------------------
# 46 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_research_xp  xpOut.Balances  []research.XPBalance  (no wire form)
#
# opaque (49) — crosses, arrives without its name:
#   Comparison.A  research.Run
#   Comparison.B  research.Run
#   Comparison.Config  research.Difference (list element)
#   Comparison.Deltas  research.Delta (list element)
#   Decision.Charter  research.Charter
#   Decision.Depends  research.Dependency (list element)
#   Decision.Program  research.Program
#   Evaluation.Depends  research.Dependency (list element)
#   Evaluation.Recommendation  research.Recommendation
#   Evidence.Links  research.Link (list element)
#   Evidence.Source  research.Source
#   EvolutionCandidate.Attestations  research.CandidateAttestation (list element)
#   EvolutionCandidate.Contributors  research.AttributionShare (list element)
#   EvolutionCandidate.EvolutionFitness  research.EvolutionFitness
#   EvolutionCandidate.ProductFitness  research.ProductFitness
#   HypothesisRecord.Budget  research.HypothesisBudget
#   IngestRequest.Attempts  research.Attempt (list element)
#   IngestRequest.Experiments  research.Experiment (list element)
#   Or_github_com_hanzoai_cloud_apps_research_runsOut_github_com_hanzoai_cloud_apps_research_runsSummary_.A  research.runsOut
#   Or_github_com_hanzoai_cloud_apps_research_runsOut_github_com_hanzoai_cloud_apps_research_runsSummary_.B  research.runsSummary
#   ResearchClaim.Contributors  research.AttributionShare (list element)
#   ResearchClaim.CounterEvidence  research.CounterEvidence
#   ResearchClaim.Replications  research.ClaimReplication (list element)
#   ResearchTotals.ByKind  research.KindTotal (list element)
#   StandardManifest.ImplementedHIPs  research.ImplementedHIP (list element)
#   actionsOut.Data  research.Action (list element)
#   artifactsOut.Data  research.ResearchArtifact (list element)
#   benchmarkBatch.Benchmarks  research.ResearchBenchmark (list element)
#   benchmarksOut.Data  research.ResearchBenchmark (list element)
#   candidatesOut.Data  research.EvolutionCandidate (list element)
#   claimsOut.Data  research.ResearchClaim (list element)
#   commitmentsOut.Data  research.Commitment (list element)
#   decisionsOut.Data  research.Decision (list element)
#   dependentsOut.Decisions  research.Dependent (list element)
#   dependentsOut.Evaluations  research.Dependent (list element)
#   evaluationsOut.Data  research.Evaluation (list element)
#   evidenceOut.Data  research.Evidence (list element)
#   experimentVersionsOut.Versions  research.ExperimentVersion (list element)
#   experimentsOut.Data  research.Experiment (list element)
#   hypothesesOut.Data  research.HypothesisRecord (list element)
#   paperBatch.Papers  research.Paper (list element)
#   papersOut.Data  research.Paper (list element)
#   projectsOut.Data  research.ProjectSummary (list element)
#   qualifyCandidateIn.EvolutionFitness  research.EvolutionFitness
#   qualifyCandidateIn.ProductFitness  research.ProductFitness
#   regressionsOut.Data  research.RegressionRecord (list element)
#   runBatch.Runs  research.Run (list element)
#   studiesOut.Data  research.Study (list element)
#   studyBatch.Studies  research.Study (list element)
