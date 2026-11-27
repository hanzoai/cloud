# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package triage

struct faultList {
    Faults list<bytes> @0
}

struct workList {
    Work list<bytes> @0
}

interface triage {
    # Is the ledger: every fingerprint counted, and the state of its issue.
    get_admin_triage_faults() returns (rep: faultList)
    # Is every issue handed to a run, and where the run stands.
    get_admin_triage_work() returns (rep: workList)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   get_admin_triage_config  Config.Threshold  map[string]triage.Limit  (map)
#   put_admin_triage_config  Config  triage.Config  (reaches one)
#   put_admin_triage_config  Config  triage.Config  (reaches one)
#
# opaque (2) — crosses, arrives without its name:
#   faultList.Faults  triage.Fault (list element)
#   workList.Work  triage.Work (list element)
