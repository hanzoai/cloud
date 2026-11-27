# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package worlds

struct Belief {
    ID     text @0
    Entity text @8
}

struct Create {
    Model  text @0
    Domain text @8
}

struct Event {
    ID        text       @0
    EventID   text       @8
    EntityID  text       @16
    Timestamp f64        @24
    Modality  text       @32
    Features  list<f64>  @40
    Neighbors list<text> @48
}

struct Models {
    Data list<bytes> @0
}

struct World {
    ID text @0
}

interface worlds {
    # Reads a world in the authenticated tenant and project.
    get_worlds_by_id(req: World)
    # Reads an entity's local belief without sharing model artifacts.
    get_worlds_by_id_beliefs_by_entity(req: Belief)
    # Lists alpha aliases only after the private runtime confirms readiness.
    get_worlds_models() returns (rep: Models)
    # Starts a simulation with the proprietary alpha model; other domains fail closed.
    post_worlds(req: Create)
    # Updates a local belief from an API feature vector.
    post_worlds_by_id_events(req: Event)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   post_worlds_by_id_inference  Inference.Policies  [][]int  (no wire form)
#   post_worlds_by_id_plans  Plan.Candidates  []worlds.Candidate  (no wire form)
#   post_worlds_by_id_rollouts  Rollout.Actions  [][]float64  (no wire form)
#
# opaque (1) — crosses, arrives without its name:
#   Models.Data  worlds.Model (list element)
