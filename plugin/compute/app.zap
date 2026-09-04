# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package compute

struct agentBinding {
    Owner       text @0
    Name        text @8
    MachineId   text @16
    Org         text @24
    AgentName   text @32
    Provider    text @40
    PublicIp    text @48
    BotVersion  text @56
    Status      text @64
    Message     text @72
    CreatedTime text @80
    UpdatedTime text @88
}

struct bindAgentReq {
    ID         text @0
    AgentName  text @8
    BotVersion text @16
}

struct bindingList {
    AgentBindings list<bytes> @0
}

struct clusterAttach {
    Name       text @0
    Kubeconfig text @8
    Provider   text @16
    Default    bool @24
}

struct clusterDetached {
    Detached text @0
}

struct clusterDetailView {
    Nodes list<bytes> @0
}

struct clusterList {
    Clusters list<bytes> @0
    Degraded list<bytes> @8
}

struct clusterRef {
    ID text @0
}

struct clusterView {
    DoksClusterID text        @0
    DoClusterID   text        @8
    Name          text        @16
    Region        text        @24
    Status        text        @32
    NodePools     list<bytes> @40
    NodeSize      text        @48
    NodeCount     i64         @56
    CreatedAt     text        @64
    Kind          text        @72
    NvidiaGPU     i64         @80
    AmdGPU        i64         @88
}

struct createClusterReq {
    Name     text  @0
    Region   text  @8
    Version  text  @16
    NodePool bytes @24
}

struct fleetBoard {
    Units list<bytes> @0
}

struct gpuList {
    GPUs list<bytes> @0
}

struct jobCancel {
    ID     text @0
    Run    text @8
    Reason text @16
}

struct jobCanceled {
    Canceled text @0
    Run      text @8
}

struct jobFilter {
    GPU    text @0
    Status text @8
}

struct jobList {
    Jobs list<bytes> @0
}

struct k8sClusterRef {
    ID text @0
}

struct machineList {
    Machines list<bytes> @0
}

struct machineQuery {
    Kind text @0
}

struct machineRef {
    ID text @0
}

struct machineView {
    Agent       text  @0
    Binding     bytes @8
    ID          text  @16
    Name        text  @24
    Region      text  @32
    Type        text  @40
    Status      text  @48
    Provider    text  @56
    PublicIp    text  @64
    PrivateIp   text  @72
    CreatedTime text  @80
    Vcpu        i64   @88
    Mem         text  @96
    GPU         text  @104
    Image       text  @112
    Os          text  @120
}

struct nodeList {
    Nodes list<bytes> @0
}

struct nodePoolView {
    PoolID    text @0
    Name      text @8
    Size      text @16
    Count     i64  @24
    MinNodes  i64  @32
    MaxNodes  i64  @40
    AutoScale bool @48
}

struct poolCreate {
    ClusterID text @0
    Provider  text @8
    Name      text @16
    Size      text @24
    Count     i64  @32
    MinNodes  i64  @40
    MaxNodes  i64  @48
    AutoScale bool @56
}

struct poolRef {
    ClusterID text @0
    PoolID    text @8
    Provider  text @16
}

struct poolScale {
    ClusterID text @0
    PoolID    text @8
    Provider  text @16
    Count     i64  @24
}

struct sampleAccepted {
    Recorded bool @0
}

struct sampleIngest {
    Unit     text @0
    Host     text @8
    GPUUtil  f64  @16
    GPUs     i64  @24
    GPUModel text @32
    MemUsed  i64  @40
    MemFree  i64  @48
}

struct sampleList {
    Samples list<bytes> @0
}

struct sampleQuery {
    Unit   text @0
    Source text @8
    Range  text @16
}

struct workerList {
    Workers list<bytes> @0
}

interface compute {
    # Attaches a BYO cluster to the caller's org — the kubeconfig is
    # validated, KMS-sealed and added to the fleet — and answers 201 with the cluster
    # as it now appears on GET /v1/compute/clusters. Billed the nominal management fee: the
    # customer brings the compute, Hanzo meters the management plane.
    attachCluster(req: clusterAttach) returns (rep: clusterView)
    # Binds a cloud Agent to one of the caller org's machines: the
    # machine is recorded as running that Agent's @hanzo/bot runtime. The owning org is
    # the validated tenant, never a client field.
    bindMachineAgent(req: bindAgentReq) returns (rep: agentBinding)
    # Cancels a queued or running render in the caller's org. The engine
    # cancel is org-scoped, so a tenant can only ever cancel its OWN job: a job in
    # another tenant's shard is 404, exactly like one that never existed. An
    # already-finished job is 409.
    cancelFleetJob(req: jobCancel) returns (rep: jobCanceled)
    # Provisions a DOKS cluster for the caller's org and answers 201.
    # ADMIN-GATED — a SuperAdmin, or an OrgAdmin of the caller's own org — because
    # provisioning spends real infrastructure on the house account. The request is
    # validated at this boundary, then Visor owns provisioning and the hanzo-org
    # ownership tag.
    createKubernetesCluster(req: createClusterReq) returns (rep: clusterView)
    # Adds a node pool to one of the caller org's clusters and answers 201
    # with the created pool. Only the CreateNodePoolSpec fields are forwarded;
    # owner/provider/clusterId ride in the query exactly as Visor expects them.
    createNodePool(req: poolCreate) returns (rep: nodePoolView)
    # Destroys a DOKS cluster by id and answers 204. ADMIN-GATED, like
    # create. Visor scopes the delete to the org (refuses a foreign id), so this can
    # only ever remove the caller org's own cluster.
    deleteKubernetesCluster(req: k8sClusterRef)
    # Terminates one of the caller org's machines. Visor takes the
    # machine identity as owner+name, and the owner is the validated principal, so a
    # caller can only ever terminate its own tenant's machine. Answers 204.
    deleteMachine(req: machineRef)
    # Removes a node pool from one of the caller org's clusters. The owner
    # scopes the delete to the caller's tenant; provider+clusterId drive the
    # provider-side removal. Answers 204.
    deleteNodePool(req: poolRef)
    # Removes a BYO cluster from the caller org's fleet. It only ever
    # touches BYO clusters — a managed cluster's nodes are removed through the node-pool
    # routes — and answers 404 when the name is not in this org's fleet.
    detachCluster(req: clusterRef) returns (rep: clusterDetached)
    # Returns one cluster's detail: node pools + worker nodes. Visor scopes
    # the lookup to the org (a foreign or missing id resolves to not-found), so a tenant
    # can never read another tenant's cluster by guessing an id.
    getKubernetesCluster(req: k8sClusterRef) returns (rep: clusterDetailView)
    # Returns one of the caller org's machines by its org-scoped name.
    # Visor keys the lookup by owner/name, so an id belonging to another tenant
    # resolves to not-found rather than another org's machine.
    getMachine(req: machineRef) returns (rep: machineView)
    # Returns the agent binding of one of the caller org's
    # machines, or 404 when the machine runs no bot runtime.
    getMachineAgent(req: machineRef) returns (rep: agentBinding)
    # Regions lists the regions a machine can be launched in.
    # The catalog is GLOBAL — identical for every tenant — so no owner is forwarded
    # upstream. It is still org-gated, because a catalog is a map of what this
    # deployment can spend money in and an anonymous caller has no business reading it.
    get_compute_regions()
    # Sizes lists the machine sizes available to launch, with their specifications.
    # Global and org-gated, exactly as the region catalog is, and for the same reasons.
    get_compute_sizes()
    # Returns the caller org's clusters from both sources: the managed
    # clusters projected from Visor's node pools, and the BYO clusters attached to the
    # caller's project. A Visor outage costs the managed half only — the BYO half
    # still lists, because a page that 502s on an optional provider is worse than a
    # page that shows what it can.
    listClusters() returns (rep: clusterList)
    # Returns every compute unit the caller's org has, from every source, each
    # carrying its latest utilization: agent run-targets, the BYO machines that dialed
    # in, attached BYO clusters and Visor-provisioned machines.
    # A unit with a live snapshot of its own keeps it; the rest are overlaid from the
    # utilization series, and only when the sample agrees about the SOURCE — two planes
    # could mint the same unit id, and a board must never show one machine's load on
    # another's row. BYO GPU units also carry their gpu-jobs queue depth. Every source
    # is folded in independently: a broken one costs its own rows and nothing else.
    listFleet() returns (rep: fleetBoard)
    # Returns the caller org's gpu-jobs render queue, each row tagged with
    # the GPU it targets (empty = the shared any-GPU lane) and the node claiming it,
    # optionally narrowed to one GPU's queue and/or one status.
    # A job whose worker died — STARTED with an elapsed lease and not yet reclaimed —
    # reads "stalled", not "running". Fail-soft: an unavailable tasks engine yields an
    # empty queue rather than an error.
    listFleetJobs(req: jobFilter) returns (rep: jobList)
    # Returns the caller org's utilization series, oldest first.
    # A rejected narrower is a 400 carrying its own reason (the vocabulary is ours and
    # safe to echo); a warehouse failure is logged and answered 503 "unavailable",
    # because a chart that silently reads "no load" when the truth is "we cannot tell"
    # is worse than one that says so. An ABSENT warehouse is different again: it returns
    # an empty series, which renders honestly as "no samples yet".
    listFleetSamples(req: sampleQuery) returns (rep: sampleList)
    # Returns the caller org's BYO machines — the ones that dialed in
    # via `hanzo link` — with everything each host reported about itself. The Machines
    # and GPUs pages fold the same data into their normalized shapes; this is the
    # canonical raw list a fleet view (or the CLI's `status`) reads.
    listFleetWorkers() returns (rep: workerList)
    # Returns one row per physical accelerator the caller's org has, derived
    # from its real GPU machines (the size slug says how many cards a node holds) and
    # from the accelerators BYO workers report through nvidia-smi.
    # Live telemetry is absent on Visor rows because Visor's machine object carries
    # none — an honest omission the console renders as "—", never a fabricated 0.
    listGpus() returns (rep: gpuList)
    # Lists the org's DOKS clusters (Visor, house account) folded with
    # the org's BYO clusters — ONE fleet cluster view under the unified k8s noun. A Visor
    # outage is logged and skipped so a down optional provider never hides the BYO list.
    listKubernetesClusters() returns (rep: clusterList)
    # Returns every DOKS worker node in the org's clusters as a machine —
    # the SAME set the fleet folds in (managedMachines), exposed directly under the k8s
    # noun. House account (hanzo-org cluster tag) + BYOC, deduped by Visor.
    listKubernetesNodes() returns (rep: nodeList)
    # Returns every agent↔machine binding in the caller's org — which
    # machines are running which cloud Agent, with vm's own reconciled status.
    listMachineAgents() returns (rep: bindingList)
    # Returns every machine the caller's org has — Visor's registry, the
    # live DigitalOcean droplets and the DOKS worker nodes (deduped into one union),
    # plus the BYO machines that dialed in via `hanzo link` (provider "byo").
    # A source Visor cannot answer for is logged and skipped, never an error: one
    # wedged upstream must not hide the machines the other sources can see.
    listMachines(req: machineQuery) returns (rep: machineList)
    # Records a BYO worker's live GPU utilization into the SAME series the
    # fleet board overlays. The org is the validated principal and source/kind are fixed
    # server-side, so a worker names only its own metrics — never another tenant or
    # another source. Answers 202: the warehouse write is DETACHED (its own bounded
    # context, never in the response path), so a slow or absent warehouse cannot stall a
    # heartbeat.
    recordFleetSample(req: sampleIngest) returns (rep: sampleAccepted)
    # Resizes a node pool to an absolute node count and returns the pool as
    # Visor reports it after the change.
    scaleNodePool(req: poolScale) returns (rep: nodePoolView)
    # Detaches the agent runtime from one of the caller org's
    # machines. The machine stays — this halts the bot, it does not terminate the
    # compute. Answers 204.
    unbindMachineAgent(req: machineRef)
}

# ---------------------------------------------------------------------
# 27 op(s) here. What follows is what this schema does not carry.
#
# dropped (1) — the value does not cross, and nothing fails:
#   clusterDetailView.clusterView  compute.clusterView  (promoted, not carried)
#
# blocked (1) — the op is absent; the field has no wire form:
#   listGpuAlerts  gpuAlertList.Alerts  []interface {}  (no wire form)
#
# opaque (14) — crosses, arrives without its name:
#   bindingList.AgentBindings  compute.agentBinding (list element)
#   clusterDetailView.Nodes  compute.machineView (list element)
#   clusterList.Clusters  compute.clusterView (list element)
#   clusterList.Degraded  compute.sourceFailure (list element)
#   clusterView.NodePools  compute.nodePoolView (list element)
#   createClusterReq.NodePool  struct { Name string "json:\"name,omitempty\""; Size string "json:\"size\""; Count int "json:\"count\"" }
#   fleetBoard.Units  compute.fleetUnit (list element)
#   gpuList.GPUs  compute.gpuView (list element)
#   jobList.Jobs  compute.gpuJob (list element)
#   machineList.Machines  compute.machineView (list element)
#   machineView.Binding  compute.agentBinding
#   nodeList.Nodes  compute.machineView (list element)
#   sampleList.Samples  compute.sampleView (list element)
#   workerList.Workers  compute.byoWorker (list element)
