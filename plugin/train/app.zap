# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package train

struct Capability {
    Name        text        @0
    Question    bytes       @8
    State       text        @16
    Version     i64         @24
    Installed   i64         @32
    Corrections list<text>  @40
    Versions    list<bytes> @48
    Live        bytes       @56
    By          text        @64
    Created     i64         @72
    Updated     i64         @80
}

struct CapabilityWeights {
    Name           text @0
    Version        i64  @8
    SHA256         text @16
    BaseSHA256     text @24
    Representation text @32
    Form           text @40
    Bytes          i64  @48
    Tar            text @56
}

struct Capture {
    On            bool @0
    RetentionDays i64  @8
}

struct Object {
    SHA256    text       @0
    Kind      text       @8
    Name      text       @16
    Size      i64        @24
    State     text       @32
    Jobs      list<text> @40
    Published bool       @48
    Stored    i64        @56
    Expires   i64        @64
    URL       text       @72
}

struct artifactsOut {
    Data list<bytes> @0
}

struct capabilitiesOut {
    Data list<bytes> @0
}

struct captureIn {
    On bool @0
}

struct catalog {
    Data list<bytes> @0
}

struct claimIn {
    Machine  text       @0
    Platform bool       @8
    Bases    list<text> @16
    Devices  list<text> @24
    Supports bytes      @32
}

struct clientIn {
    ID text @0
}

struct clientList {
    Clients list<bytes> @0
}

struct correctionsIn {
    Question text @0
}

struct correctionsOut {
    Data      list<bytes> @0
    Questions list<bytes> @8
}

struct createClientIn {
    BaseModel  text  @0
    LoraConfig bytes @8
    Adaptation bytes @16
    Protect    bytes @24
}

struct createIn {
    Name        text        @0
    Corrections list<text>  @8
    Question    bytes       @16
    Examples    list<bytes> @24
    Train       bool        @32
}

struct createPolicyIn {
    Name     text  @0
    Document bytes @8
    Note     text  @16
}

struct deletedOut {
    SHA256  text @0
    Deleted bool @8
}

struct eventsIn {
    ID    text @0
    After i64  @8
    Wait  i64  @16
}

struct eventsOut {
    Data   list<bytes> @0
    Next   i64         @8
    Status text        @16
}

struct forwardIn {
    ID   text        @0
    Data list<bytes> @8
}

struct idIn {
    ID text @0
}

struct listIn {
    Status text @0
    Limit  i64  @8
}

struct nameIn {
    Name text @0
}

struct objectsOut {
    Data list<bytes> @0
}

struct optimIn {
    ID         text  @0
    AdamParams bytes @8
}

struct policyDiffIn {
    Name text @0
    From i64  @8
    To   i64  @16
}

struct policyIn {
    Name text @0
}

struct policyVersionIn {
    Name    text @0
    Version i64  @8
}

struct promotePolicyIn {
    Name    text @0
    Version i64  @8
    Base    i64  @16
    Next    text @24
    Note    text @32
}

struct publishCapIn {
    Name    text @0
    Version i64  @8
}

struct publishedIn {
    Revision text @0
}

struct reportIn {
    ID     text        @0
    Task   text        @8
    Lease  text        @16
    Events list<bytes> @24
}

struct reportOut {
    Cancel bool @0
    Held   i64  @8
    Status text @16
    Seq    i64  @24
}

struct rollbackPolicyIn {
    Name   text @0
    Base   i64  @8
    Origin bool @16
    Next   text @24
    Note   text @32
}

struct sampleIn {
    ID             text       @0
    Prompt         list<text> @8
    Tokens         list<u32>  @16
    SamplingParams bytes      @24
    NumSamples     list<i64>  @32
}

struct saveIn {
    ID     text  @0
    Name   text  @8
    Output bytes @16
}

struct shaIn {
    SHA256 text @0
}

struct trainIn {
    Name        text        @0
    Corrections list<text>  @8
    Examples    list<bytes> @16
}

struct unmadeOut {
    Name    text @0
    Deleted bool @8
}

struct uploadIn {
    Kind   text @0
    Name   text @8
    SHA256 text @16
    Size   i64  @24
}

interface train {
    # Answers whether the org's Kai decisions keep their states for its
    # capabilities to learn from. Off until the org turns it on or files its first
    # correction.
    capabilitiesCapture() returns (rep: Capture)
    # Answers every correction of the org's decisions, newest first,
    # each captured (it can teach) or missing (its decision was made with capture
    # off), and the questions they ask, each counted: what a capability is made from.
    capabilitiesCorrections(req: correctionsIn) returns (rep: correctionsOut)
    # Makes a capability in draft from captured corrections of one
    # question — or cold-start from labeled (state, answer) examples of Question,
    # encoded once over Kai's shared representation — which becomes the route it
    # will be installed behind. When Train is true, it immediately runs a CPU lesson
    # and returns the evaluated or publishable capability. Refused: a name taken by
    # another capability or by a published job's output (409 name_taken),
    # corrections of two questions or not captured (400), and a question another of
    # the org's capabilities answers (409 route_conflict).
    capabilitiesCreate(req: createIn) returns (rep: Capability)
    # Deletes a capability and its versions; their artifacts go
    # with the org's retention. Refused 409 installed while a version is in the set:
    # disable it first.
    capabilitiesDelete(req: nameIn) returns (rep: unmadeOut)
    # Takes a capability's installed version out of the org's
    # published set: its questions are answered by base Kai, bit for bit, from the
    # next decision. Its versions stay, and any one that passed can be published
    # again.
    capabilitiesDisable(req: nameIn) returns (rep: Capability)
    # Answers one capability: its versions with their evaluations,
    # newest first, and what its installed version did live.
    capabilitiesGet(req: nameIn) returns (rep: Capability)
    # Answers the org's capabilities by name, each with its state,
    # its latest and installed versions, and what the installed one did live.
    capabilitiesList() returns (rep: capabilitiesOut)
    # Installs a version in the org's published set, where ai
    # attaches it to the org's decisions: the version's evaluation must have passed,
    # and it must have been taught over the base Kai serves now. Refused 409:
    # not_publishable (no passing version, or that one did not pass),
    # needs_validation (Kai's base moved since it was taught: train again),
    # route_conflict (a published job answers its question).
    capabilitiesPublish(req: publishCapIn) returns (rep: Capability)
    # Puts the version installed before the installed one back
    # in the org's published set: exactly that version's artifact, nothing retrained.
    # Refused 409 no_previous when none was installed before it, and
    # needs_validation when that one was taught over another base than Kai serves.
    capabilitiesRollback(req: nameIn) returns (rep: Capability)
    # Turns capture on or off for the org's Kai decisions from the next
    # one. Off keeps what was captured until its retention ends.
    capabilitiesSetCapture(req: captureIn) returns (rep: Capture)
    # Teaches a capability a new version from its corrections, on
    # the CPU over the states its org's decisions captured: no row is encoded again.
    # When Examples are provided, they are encoded once over Kai's shared
    # representation and any new mistakes are added to its corrections before the
    # lesson runs. The smallest correction that passes evaluation is kept
    # (calibration, then a head, then a boost and veto pair); its evaluation, held
    # out against base Kai, says what it repaired and damaged, how often it fires,
    # that every other answer is base Kai's bit for bit, and what it costs. A
    # version that passed is publishable; one that did not is evaluated, with the
    # gate's reasons. An installed capability keeps serving its installed version
    # until a new one is published.
    capabilitiesTrain(req: trainIn) returns (rep: Capability)
    # Answers a capability's .tar weights (base64) and shared
    # representation metadata for its installed or newest passing version, so the
    # org can export or open-source its trained adapter.
    capabilitiesWeights(req: nameIn) returns (rep: CapabilityWeights)
    # Deletes one of the org's objects — an upload or a job's
    # output — from the store: every artifact naming it then reads deleted, and its
    # storage is no longer billed. A published object is 409 published, and one a
    # job that has not ended reads or produced is 409 in_use. Another org's sha256
    # is not found, exactly as an unknown one is.
    delete_train_artifacts_by_sha256(req: shaIn) returns (rep: deletedOut)
    # Drops one of the org's clients and frees its memory on the engine.
    delete_train_clients_by_id(req: clientIn)
    # Answers the org's objects — its uploads and its jobs'
    # outputs, one per sha256 — with what produced each, whether it is published and
    # when retention deletes it.
    get_train_artifacts() returns (rep: objectsOut)
    # Answers one of the org's objects, as the list does, with an
    # address that fetches its bytes for ten minutes once it is stored: how an
    # executor reads an `artifact:<sha256>` dataset its job names, and how the org
    # downloads what it uploaded. Another org's sha256 is not found, exactly as an
    # unknown one is.
    get_train_artifacts_by_sha256(req: shaIn) returns (rep: Object)
    # Answers the org's live clients as the engine reports them. A
    # client the engine no longer holds is dropped.
    get_train_clients() returns (rep: clientList)
    # Answers one of the org's clients with its loss history.
    get_train_clients_by_id(req: clientIn)
    # Answers a job's artifacts — checkpoint, capability, lora,
    # basis or merged, each named by the SHA-256 of its bytes — with a download
    # address that lives ten minutes for each one stored. One deleted, by the org or
    # by retention, reads deleted and has none.
    get_train_jobs_by_id_artifacts(req: idIn) returns (rep: artifactsOut)
    # Answers a job's events after a cursor — status changes, the
    # coordinator's address, metrics, log lines, artifacts and the result — waiting up
    # to `wait` seconds for the first new one, so a caller following a run holds one
    # request at a time.
    get_train_jobs_by_id_events(req: eventsIn) returns (rep: eventsOut)
    # Answers each base the caller can train today, in one shape:
    # the base, the methods it trains by (adaptations), where it runs (runs: Hanzo's
    # devices or the org's machines) with what one device-second costs there in
    # nano-USD, then the resources that run it (nouns), its executor and devices, and
    # the protections, objective terms, outputs and dataset schemes it takes. Bases
    # are ordered by name, a base's runs by place, then price. A create is validated
    # against the same rows, and a noun the caller cannot start is not listed: the
    # engine's clients are a SuperAdmin's.
    get_train_models() returns (rep: catalog)
    # Loads base_model on the engine with a LoRA adapter and answers
    # the client, loading; poll it until ready. The wire is the engine's own
    # create → forward_backward → optim_step → sample → save_weights loop, so a loop
    # written against the engine runs here with only the path changed. The
    # capability fields are validated as a job's are: adaptation is lora, protect is
    # refused, and kai is not a client base. A client holds a model in the memory every
    # org's inference shares, so only a SuperAdmin creates one until the engine isolates
    # orgs. An org holds at most TRAIN_CLIENTS live clients and the engine at most
    # TRAIN_ENGINE_CLIENTS across every org; past that the answer is 503 engine_full.
    post_train_clients(req: createClientIn)
    # Accumulates the gradients of a batch on the client.
    post_train_clients_by_id_forward_backward(req: forwardIn)
    # Applies the optimizer to the accumulated gradients.
    post_train_clients_by_id_optim_step(req: optimIn)
    # Decodes from the client's current weights.
    post_train_clients_by_id_sample(req: sampleIn)
    # Writes the client's adapter on the engine.
    post_train_clients_by_id_save_weights(req: saveIn)
    # ReportTrainTask is an executor's report for the task it holds: status changes,
    # the lead's coordinator address, metrics, log lines, stored artifacts and, last,
    # the lead's result. It must carry the task's lease; a report under any other is
    # 409 lease_lost and the executor stops. A report with no events is a heartbeat,
    # due at least every `report` seconds the claim answered — a task silent for the
    # lease's life is lost, and a lost lead fails its job. Each report meters the
    # task by the clock: its devices times the seconds since it was claimed, no more
    # than is held, charged to the job's payer for a job Hanzo runs. The answer says
    # how many device-seconds are held now. When the next window cannot be held and
    # what is held runs low, the job ends stopped and the answer says cancel: kill
    # the trainer and report nothing more. Once the job has ended a report records
    # nothing and answers the job's status; cancel says it was cancelled or stopped.
    post_train_jobs_by_id_events(req: reportIn) returns (rep: reportOut)
}

# ---------------------------------------------------------------------
# 27 op(s) here. What follows is what this schema does not carry.
#
# blocked (20) — the op is absent; the field has no wire form:
#   get_train_jobs  jobsOut.Data  []*train.Job  (no wire form)
#   get_train_jobs_by_id  Job.Spec  train.Spec  (reaches one)
#   get_train_jobs_by_id_metrics  metricsOut.Data  []train.series  (no wire form)
#   get_train_published  publishedOut.Data  []train.Install  (no wire form)
#   policiesCreate  PolicyVersion.Tiers  map[string]train.PolicyLead  (map)
#   policiesDiff  PolicyDiff.Benches  []train.PolicyBenchDiff  (no wire form)
#   policiesDiff  PolicyDiff.Source  [2]string  (no wire form)
#   policiesGet  PolicyBoard.Versions  []train.PolicyVersion  (no wire form)
#   policiesPromote  PolicyBoard  train.PolicyBoard  (reaches one)
#   policiesRollback  PolicyBoard  train.PolicyBoard  (reaches one)
#   policiesVersion  PolicyVersion  train.PolicyVersion  (reaches one)
#   post_train_artifacts  registerOut.Upload  train.upload  (reaches one)
#   post_train_jobs  Job  train.Job  (reaches one)
#   post_train_jobs  Spec.Dataset  train.Dataset  (reaches one)
#   post_train_jobs_by_id_artifacts  registerIn.Meta  map[string]interface {}  (map)
#   post_train_jobs_by_id_artifacts  registerOut  train.registerOut  (reaches one)
#   post_train_jobs_by_id_cancel  Job  train.Job  (reaches one)
#   post_train_jobs_by_id_publish  Job  train.Job  (reaches one)
#   post_train_jobs_by_id_publish  publishIn.Route  train.Route  (reaches one)
#   post_train_jobs_claim  Assignment.Job  train.Job  (reaches one)
#
# opaque (17) — crosses, arrives without its name:
#   Capability.Live  train.Live
#   Capability.Question  train.Question
#   Capability.Versions  train.Version (list element)
#   artifactsOut.Data  train.Download (list element)
#   capabilitiesOut.Data  train.Capability (list element)
#   catalog.Data  train.Family (list element)
#   claimIn.Supports  train.Supports
#   correctionsOut.Data  client.Correction (list element)
#   correctionsOut.Questions  train.CorrectionGroup (list element)
#   createClientIn.Adaptation  train.Adaptation
#   createClientIn.Protect  train.Protect
#   createIn.Examples  train.ExampleIn (list element)
#   createIn.Question  train.Question
#   eventsOut.Data  train.event (list element)
#   objectsOut.Data  train.Object (list element)
#   saveIn.Output  train.Output
#   trainIn.Examples  train.ExampleIn (list element)
