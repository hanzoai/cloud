# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package engine

struct engineModel {
    Model text @0
}

struct engineStatus {
    Reachable bool @0
    Revision  text @8
}

interface engine {
    # Model reads one model's load state — loaded, unloading, or not_found, as
    # the engine itself reports it.
    engineModel(req: engineModel)
    # Models lists the models the engine serves, each with its load state — the
    # server's own model table (its standard list envelope, load status
    # included), relayed verbatim.
    engineModels()
    # Status reports whether the engine deployment is reachable and which build
    # revision it runs — an honest lens for "is the serving runtime up", never a
    # fabricated ok.
    engineStatus() returns (rep: engineStatus)
    # System reads the engine host's inventory: OS, CPU, memory, every accelerator
    # device with its VRAM and compute capability, and the build's capabilities
    # (CUDA/Metal/flash-attention) — the real hardware under the serving runtime,
    # relayed verbatim.
    engineSystem()
}
