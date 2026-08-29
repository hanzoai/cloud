# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package team

struct blobRef {
    Workspace text @0
    Filename  text @8
    File      text @16
}

struct botRoster {
    Bots list<bytes> @0
}

struct botSync {
    Synced    bool @0
    Projected i64  @8
}

struct cookieAck {
    Result bool @0
}

struct planInfo {
    Plan       text @0
    Active     bool @8
    Seats      i64  @16
    Guests     i64  @24
    GuestLimit i64  @32
    UpgradeURL text @40
}

struct statsIn {
    Token text @0
}

struct teamRoom {
    ID        text       @0
    Workspace text       @8
    Name      text       @16
    Topic     text       @24
    Direct    bool       @32
    Private   bool       @33
    Archived  bool       @34
    Members   list<text> @40
    Life      text       @48
    Bindings  list<text> @56
}

struct teamRoomBind {
    ID        text       @0
    Workspace text       @8
    Life      text       @16
    Bindings  list<text> @24
}

struct teamRooms {
    Rooms list<bytes> @0
}

interface team {
    # Signs this browser out of team by expiring the HttpOnly
    # account-token cookie the OAuth callback set. It is the counterpart of the
    # cookie PUT, it takes nothing — the cookie it clears is named by this service,
    # never by the caller — and it is unconditional: a caller with no cookie, an
    # expired one or a forged one all get the same acknowledgement, because clearing
    # something that is not there is the same outcome as clearing something that is.
    # It clears ONLY the team session cookie. The IAM access-token cookie the same
    # callback set is a different credential with a different lifetime and is left
    # alone, so this is a team sign-out, not a platform one.
    delete_team_account_cookie() returns (rep: cookieAck)
    # Removes one blob from a workspace's file store. The caller must
    # hold a verified session AND be a member of the workspace; anything else — an
    # unknown workspace, another tenant's workspace, a workspace the caller is not
    # in — answers the same 404, so a probe learns nothing about what exists.
    # It is IDEMPOTENT: deleting a present or an absent blob both answer 204, so a
    # delete never confirms a blob's existence and a foreign blob id (a physical key
    # the caller can never name into another tenant's box) is a harmless no-op. A
    # storage backend that is unavailable fails closed with 502 rather than lying
    # about success.
    delete_team_files_by_workspace_by_filename(req: blobRef)
    # Returns the identity providers this deployment starts a login
    # with. It is always exactly one — hanzo.id. Which identities that provider accepts
    # (Google, GitHub, passkey, password) is IAM's question, answered on IAM's own
    # page next to the identity check and the training-data consent that must
    # precede a first session; listing them here would be a second place holding
    # that answer, and the two drift the moment IAM gains or drops one.
    get_team_account_providers()
    # Returns the plan and seat counts for the caller's OWN org, resolved
    # from the VERIFIED team session token — never a client header. Seats and guests
    # are the org's distinct active human members (a bot member is not a seat); the
    # plan comes from the licensing entitlement and is empty when that read is
    # unavailable, so the page shows an honest dash rather than a fabricated tier. A
    # caller with no verified session gets 401, and a real seat-read failure is a
    # 502 rather than a false "0 members".
    get_team_billing_plan() returns (rep: planInfo)
    # Returns the caller org's bot members — the org's agents projected as
    # the workspace Employees they become, each with the member account uuid and
    # Person reference the roster addresses it by. An agents subsystem that is not
    # mounted answers an empty list, never an error.
    get_team_bots() returns (rep: botRoster)
    # Returns every room of the caller's org, across the workspaces
    # it owns, with the work facet each carries.
    # It reads the SAME Chunter documents the transactor serves, so a room opened
    # in the Team client appears here with no sync, and a facet written here is read
    # by anything holding the document. Direct messages are included: a room between
    # two people is a room with no name, not a different kind of thing.
    get_team_rooms() returns (rep: teamRooms)
    # SyncBots re-projects the caller org's agents as workspace members into EVERY
    # workspace of the org, and removes the ones whose agent is gone. It is
    # idempotent, and admin only: mutating a workspace's roster requires the
    # gateway-minted admin flag, which a client can never forge. It answers how many
    # roster entries the reconcile touched.
    post_team_bots_sync() returns (rep: botSync)
    # States what a room is for: its lifecycle intent, and what it is
    # about. It answers the room as it now stands.
    # The write is a platform MIXIN on the room document, applied through the
    # SAME applyTx path the Team client's own writes take and broadcast to every
    # connected client — so a room bound here updates live in an open workspace
    # rather than on the next reload.
    put_team_rooms_by_id(req: teamRoomBind) returns (rep: teamRoom)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   get_team_transactor_statistics  statsOut.Statistics  team.statsSessions  (reaches one)
#   post_team_collaborator_rpc_by_documentid  collabRequest.Payload  team.collabPayload  (reaches one)
#   post_team_collaborator_rpc_by_documentid  collabResult.Content  map[string]string  (map)
#
# opaque (2) — crosses, arrives without its name:
#   botRoster.Bots  team.botMember (list element)
#   teamRooms.Rooms  team.teamRoom (list element)
