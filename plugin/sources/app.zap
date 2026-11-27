# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package sources

struct Account {
    ID          text       @0
    Provider    text       @8
    Kind        text       @16
    Label       text       @24
    Fingerprint text       @32
    Created     text       @40
    Classes     list<text> @48
    SourceID    text       @56
    Instance    text       @64
    State       text       @72
    Plan        text       @80
    Shared      list<bool> @88
}

struct AccountDeleted {
    ID      text @0
    Deleted bool @8
}

struct AccountList {
    Accounts list<bytes> @0
}

struct AccountNew {
    Provider text @0
    Kind     text @8
    Label    text @16
    Value    text @24
}

struct AccountRef {
    ID text @0
}

struct Source {
    ID        text        @0
    Kind      text        @8
    Name      text        @16
    OS        text        @24
    Arch      text        @32
    Version   text        @40
    State     text        @48
    LastSeen  text        @56
    LinkedAt  text        @64
    Zap       text        @72
    Providers list<bytes> @80
}

struct SourceBeat {
    ID        text        @0
    At        text        @8
    Version   text        @16
    Zap       text        @24
    Providers list<bytes> @32
    Busy      i64         @40
    Sig       text        @48
}

struct SourceBeatReply {
    Interval i64  @0
    Level    text @8
    Revoked  bool @16
}

struct SourceDispatch {
    ID       text @0
    Provider text @8
    Instance text @16
}

struct SourceLease {
    ID    text @0
    Lease text @8
}

struct SourceLink {
    Kind    text @0
    Name    text @8
    OS      text @16
    Arch    text @24
    Version text @32
    Key     text @40
    Zap     text @48
}

struct SourceLinked {
    ID       text @0
    Interval i64  @8
    Level    text @16
}

struct SourceList {
    Sources list<bytes> @0
}

struct SourceRef {
    ID text @0
}

struct SourceReleased {
    Lease    text @0
    Released bool @8
}

struct SourceRevoked {
    ID      text @0
    Revoked bool @8
}

struct SourceRoute {
    Zap      text @0
    Provider text @8
    Instance text @16
    Lease    text @24
    Expires  text @32
}

struct UsageQuery {
    By   text @0
    From text @8
    To   text @16
}

struct UsageView {
    Rows      list<bytes> @0
    Snapshots list<bytes> @8
}

interface sources {
    # Removes one of the signed-in person's own provider keys. KMS is
    # asked to delete the secret first and the row goes only after it has; if KMS
    # cannot, the answer is 502 and the account stays. Another person's id is 404. A
    # login cannot be deleted here: it goes when the person logs out on its source.
    delete_sources_accounts_by_id(req: AccountRef) returns (rep: AccountDeleted)
    # Unlinks one of the signed-in person's own sources and drops its
    # provider state, so nothing routes through it. The act is audited. The source's
    # next heartbeat answers 410 and it forgets the link. Revoking twice is the same
    # answer as once.
    delete_sources_by_id(req: SourceRef) returns (rep: SourceRevoked)
    # Ends a run's hold on a login instance, so the next run can start
    # before the lease lapses. Only the person who holds the lease can release it; any
    # other lease or source is 404.
    delete_sources_by_id_dispatch_by_lease(req: SourceLease) returns (rep: SourceReleased)
    # Returns the signed-in person's own active sources, newest
    # heartbeat first, each with its online state and provider state. There is no
    # read of another person's sources: not for an org admin, not for a SuperAdmin.
    get_sources() returns (rep: SourceList)
    # Returns the signed-in person's own provider accounts with the route
    # class each may run under: the keys they sealed into KMS, and the logins their
    # linked sources report. A key shows a label and fingerprint, never its value. A
    # login is read-only here and disappears when its source stops reporting it.
    get_sources_accounts() returns (rep: AccountList)
    # Returns one of the signed-in person's own sources, including a
    # revoked one, whose state reads `revoked`. Another person's id and an unknown id
    # answer the same 404.
    get_sources_by_id(req: SourceRef) returns (rep: Source)
    # Totals the signed-in person's own reported usage by source,
    # provider, model, day or user between two days, with their latest snapshot per
    # source and provider. An org admin asking by=user gets every person's totals in
    # the org (key is the person's subject) and nothing else of theirs: no source,
    # snapshot, plan or label of another person is readable, and a SuperAdmin has no
    # extra read. For anyone else by=user is their own single row.
    get_sources_usage(req: UsageQuery) returns (rep: UsageView)
    # Links this install to the signed-in person, or refreshes the install that
    # already holds the same device key and answers with its existing id. The person
    # and org come from the validated token and never from the body; a machine token
    # is refused. A person holds at most 25 active sources and may link 10 new ones
    # an hour.
    post_sources(req: SourceLink) returns (rep: SourceLinked)
    # Seals a provider API key into KMS and records a reference to it.
    # KMS keeps the value at kms://orgs/<org>/users/<person>/providers/<provider>/<id>;
    # the row keeps that path, the label and a fingerprint, and nothing else. The value
    # is in no response, log, trace or audit field, and not even the owner can read it
    # back: the gateway resolves it at call time. Only `kind: "key"` is accepted, for a
    # provider that takes a key.
    post_sources_accounts(req: AccountNew) returns (rep: Account)
    # Looks up where a session may run on one of the caller's own linked
    # desktops and takes the lease for it; it is the lookup the /v1/dev execution field
    # calls, and frames then flow from the browser to the desktop, not through cloud. It
    # grants only when the caller owns the source (another person's is 404, with no
    # admin or SuperAdmin path), the source is not revoked, is a desktop with a ZAP
    # endpoint and a heartbeat in the last 90 seconds, the login instance is `ready`
    # and shared, the provider's classes include `desktop`, and no live lease holds the
    # instance (one run per login at a time, 15 minutes). Every grant and refusal is
    # audited. A refusal carries `code`: revoked (410), class (403), no_endpoint,
    # offline, not_ready, not_shared, busy (409).
    post_sources_by_id_dispatch(req: SourceDispatch) returns (rep: SourceRoute)
    # Records that a source is alive and what its providers can do, and
    # answers the interval and usage level. The beat is signed by the source's device
    # key, so a stolen token alone cannot impersonate it: a bad or missing signature is
    # 401. A revoked source is answered 410 and should forget the link. A source is
    # online for 90 seconds after a beat; beats closer than 10 seconds are refused 429.
    # Providers are state only and carry no token, email or key material.
    post_sources_by_id_heartbeat(req: SourceBeat) returns (rep: SourceBeatReply)
}

# ---------------------------------------------------------------------
# 11 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   AccountList.Accounts  source.Account (list element)
#   Source.Providers  source.SourceProvider (list element)
#   SourceBeat.Providers  source.SourceProvider (list element)
#   SourceList.Sources  source.Source (list element)
#   UsageView.Rows  source.UsageRow (list element)
#   UsageView.Snapshots  source.ProviderUsage (list element)
