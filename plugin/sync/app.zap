# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package sync

struct patchSyncIn {
    ID        text       @0
    Direction list<text> @8
    Trigger   list<text> @16
    Actor     list<text> @24
    Source    bytes      @32
    Target    bytes      @40
    Kind      list<text> @48
}

struct syncList {
    Data list<bytes> @0
}

struct syncQueued {
    Queued bool @0
    ID     text @8
}

struct syncRef {
    ID text @0
}

struct syncReq {
    Kind      text  @0
    Source    bytes @8
    Target    bytes @16
    Direction text  @24
    Trigger   text  @32
    Actor     text  @40
    Run       bool  @48
}

struct syncView {
    ID        text  @0
    Kind      text  @8
    Source    bytes @16
    Target    bytes @24
    Direction text  @32
    Trigger   text  @40
    Actor     text  @48
    CreatedAt text  @56
    UpdatedAt text  @64
    Scope     text  @72
    Native    bytes @80
}

interface sync {
    # Removes one sync and tears down the outbound mirror it derived, answering
    # 204. The teardown is the point: without it an unsynced repository would keep
    # force-pushing to the upstream it is no longer linked to. Org-scoped, so another
    # tenant's id is the same 404 an unknown id gives.
    delete_sync_by_id(req: syncRef)
    # Returns every sync link the caller's org has, each with its two endpoints, its
    # direction and trigger policy, the time it last reconciled, and for a repo link its
    # native copy on the forge — the address to clone it from, its default branch and
    # whether it is in step. Scoped to the caller's own org — another tenant's links are
    # structurally unreachable.
    get_sync() returns (rep: syncList)
    # Returns one sync by id. It is org-scoped: an id belonging to another tenant is
    # the same 404 an unknown id gives, so a probe learns nothing about what exists.
    get_sync_by_id(req: syncRef) returns (rep: syncView)
    # Updates one sync's mutable policy — direction, trigger and actor — in place.
    # The endpoints and the kind are immutable: re-pointing a sync is a delete and a
    # create, so a link can never silently start syncing somewhere else. A field the
    # request omits is left as it was. Changing the direction immediately reconciles the
    # derived outbound mirror, so turning push off stops the upstream being written to
    # rather than merely recording the intent.
    patch_sync_by_id(req: patchSyncIn) returns (rep: syncView)
    # Declares a sync between two endpoints and returns it. It is an UPSERT:
    # re-declaring the same source and target updates that link rather than piling up
    # duplicates, so a console that re-submits is safe. The org comes from the validated
    # principal, never from the request, so a sync can only ever bind endpoints inside
    # the caller's own org. A git source must be an https clone URL on the provider's own
    # host with no embedded credentials; a target left empty is derived as a native
    # repository named after the source. A source naming only a GitHub account
    # (https://github.com/<account>) links the whole account: every repository the org's
    # GitHub installation grants on it gets a repo link of its own, with this link's
    # direction and trigger, and so does every repository created there later. With run=true the first reconcile is queued in
    # the background, so a large initial import never blocks this response.
    post_sync(req: syncReq) returns (rep: syncView)
    # Reconciles one sync now — the manual re-sync, and the initial import for a link
    # created without run=true. The work is handed to a bounded background worker and the
    # call answers 202 immediately, so a large mirror-in never holds the request open;
    # queued=true means accepted, not finished.
    post_sync_by_id_run(req: syncRef) returns (rep: syncQueued)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (8) — crosses, arrives without its name:
#   patchSyncIn.Source  sync.endpointReq
#   patchSyncIn.Target  sync.endpointReq
#   syncList.Data  sync.syncView (list element)
#   syncReq.Source  sync.endpointReq
#   syncReq.Target  sync.endpointReq
#   syncView.Native  sync.nativeView
#   syncView.Source  sync.endpointView
#   syncView.Target  sync.endpointView
