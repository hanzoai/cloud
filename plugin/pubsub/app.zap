# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package pubsub

struct busAck {
    OK        bool @0
    Stream    text @8
    Seq       u64  @16
    Duplicate bool @24
}

# ---------------------------------------------------------------------
# 0 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   post_pubsub_publish  busPublish.Headers  map[string]string  (map)
#   post_pubsub_request  busMessage.Headers  map[string][]string  (map)
#   post_pubsub_request  busRequest.Headers  map[string]string  (map)
