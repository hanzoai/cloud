# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package engine

struct engineModel {
    Model text @0
}

struct engineReplicaUpdate {
    Model    text       @0
    ID       text       @8
    Capacity i64        @16
    Weight   i64        @24
    Roles    list<text> @32
}

struct engineStatus {
    Reachable bool @0
    Revision  text @8
}

interface engine {
    # Reads one model's load state — loaded, unloading, or not_found, as
    # the engine itself reports it.
    engineModel(req: engineModel)
    # Lists the models the engine serves, each with its load state — the
    # server's own model table (its standard list envelope, load status
    # included), relayed verbatim.
    engineModels()
    # Reads the private native router's configured model pools, worker
    # health, concurrency, role preferences and observed time to first byte.
    # This shared infrastructure inventory is restricted to platform SuperAdmins.
    engineReplicas()
    # Reports whether the engine deployment is reachable and which build
    # revision it runs — an honest lens for "is the serving runtime up", never a
    # fabricated ok.
    engineStatus() returns (rep: engineStatus)
    # Reads the engine host's inventory: OS, CPU, memory, every accelerator
    # device with its VRAM and compute capability, and the build's capabilities
    # (CUDA/Metal/flash-attention) — the real hardware under the serving runtime,
    # relayed verbatim.
    engineSystem()
    # Saves capacity, weight and preferred agent roles in the native
    # router's pool file and applies them without dropping running streams or pins.
    # Only platform SuperAdmins can retune shared workers. The Rust runtime owns
    # validation, durable storage and activation; cloud only carries the operation.
    engineUpdateReplica(req: engineReplicaUpdate)
}
