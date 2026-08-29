# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package reference

struct ClearReferenceIn {
    Set text @0
    Key text @8
}

struct ClearReferenceOut {
    Set       text @0
    Key       text @8
    Cleared   bool @16
    Overrides i64  @24
}

struct ReferenceIn {
    Set   text @0
    After text @8
    Limit i64  @16
}

struct ReferenceOut {
    Set       bytes       @0
    Overrides list<bytes> @8
    Next      text        @16
}

struct ReferenceSetsOut {
    Sets    list<bytes> @0
    Stale   list<text>  @8
    Refused list<text>  @16
}

struct RefreshReferenceIn {
    Set      text        @0
    Receipts list<bytes> @8
    Force    bool        @16
}

struct RefreshReferenceOut {
    Set     text        @0
    Took    list<bytes> @8
    Version text        @16
    Stale   bool        @24
}

struct ResolveReferenceIn {
    Sets list<text> @0
    Keys list<text> @8
}

struct SetReferenceIn {
    Set     text        @0
    Entries list<bytes> @8
}

struct SetReferenceOut {
    Set       text @0
    Written   i64  @8
    Overrides i64  @16
}

interface reference {
    # Removes one of your organisation's overrides.
    # It removes an entry your organisation wrote, never a baseline member: the
    # published set is not writable from here, so a removal can only ever restore
    # the baseline's own answer.
    riskClearReference(req: ClearReferenceIn) returns (rep: ClearReferenceOut)
    # Reference describes one set and lists your org's overrides in it.
    # The set half is public data about a published list — its version, its
    # publishers, their licences and how current each one is. The overrides half is
    # yours alone: it is read from your organisation's own store, and no other
    # organisation's entries can appear in it.
    riskReference(req: ReferenceIn) returns (rep: ReferenceOut)
    # Lists every set this plane publishes, with its version and how
    # fresh it is.
    # Read the Stale and Refused lists first: they are the two ways this plane can
    # be quietly wrong, and they are reported rather than inferred. A set in
    # Refused answers nothing — it has never loaded, it is held by another
    # component, or it names a source we hold no licence for.
    riskReferenceSets() returns (rep: ReferenceSetsOut)
    # Takes a new version of one set. SuperAdmin only.
    # It is platform work, not tenant work: it writes the shared baseline every
    # organisation reads, so it is gated to the platform's own identity. Nothing
    # here can write an organisation's overrides, and nothing an organisation sends
    # can reach this route.
    # Idempotent. A version is the content digest of what was taken, so refreshing
    # an unchanged publisher writes no rows and reports unchanged. Resumable: a run
    # that died half-way is continued from where it stopped rather than restarted.
    # A set whose source needs a licence we do not hold is refused with the reason,
    # rather than being quietly skipped.
    riskRefreshReference(req: RefreshReferenceIn) returns (rep: RefreshReferenceOut)
    # Writes your organisation's own allow and deny entries over a set.
    # Idempotent on the key: writing the same entry twice is one entry, and writing
    # it again replaces the verdict and the note. The whole batch is one
    # transaction, so a batch that would cross the per-set bound writes nothing
    # rather than half of itself — a half-applied deny list is worse than a refused
    # one, because nobody can tell which half applied.
    # Your entries are held in your organisation's own store and are never visible
    # to another organisation, and they never change what any other organisation
    # sees. The shared baseline is not writable from here at all.
    riskSetReference(req: SetReferenceIn) returns (rep: SetReferenceOut)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   riskResolveReference  ResolveReferenceOut.Answers  []reference.ReferenceAnswer  (no wire form)
#
# opaque (6) — crosses, arrives without its name:
#   ReferenceOut.Overrides  reference.ReferenceOverride (list element)
#   ReferenceOut.Set  reference.ReferenceSet
#   ReferenceSetsOut.Sets  reference.ReferenceSet (list element)
#   RefreshReferenceIn.Receipts  reference.ReferenceReceipt (list element)
#   RefreshReferenceOut.Took  reference.ReferenceTaken (list element)
#   SetReferenceIn.Entries  reference.ReferenceOverrideIn (list element)
