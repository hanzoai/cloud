# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package team

struct blobRef {
    Space    text @0
    Filename text @8
    File     text @16
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

struct publicChannels {
    Channels list<bytes> @0
}

struct publicQuery {
    Q     text @0
    Org   text @8
    Limit i64  @16
}

struct statsIn {
    Token text @0
}

struct teamChannelBind {
    ID       text       @0
    Space    text       @8
    Life     text       @16
    Bindings list<text> @24
}

struct teamChannelEdit {
    ID       text       @0
    Space    text       @8
    Name     list<text> @16
    Topic    list<text> @24
    Archived list<bool> @32
}

struct teamChannelJoin {
    ID      text       @0
    Space   text       @8
    Members list<text> @16
}

struct teamChannelLeave {
    ID      text @0
    Account text @8
    Space   text @16
}

struct teamChannelMembers {
    Members list<bytes> @0
}

struct teamChannelNew {
    Name     text       @0
    Space    text       @8
    Topic    text       @16
    Private  bool       @24
    Members  list<text> @32
    Life     text       @40
    Bindings list<text> @48
}

struct teamChannelOne {
    Channel bytes @0
}

struct teamChannels {
    Channels list<bytes> @0
}

struct teamCommentWrite {
    ID    text        @0
    Space text        @8
    Text  text        @16
    Files list<bytes> @24
}

struct teamDirect {
    Channel bytes @0
    Created bool  @8
}

struct teamDirectOpen {
    Space   text       @0
    Members list<text> @8
}

struct teamDoc {
    ID           text @0
    Space        text @8
    Teamspace    text @16
    Title        text @24
    Parent       text @32
    Rank         text @40
    Author       text @48
    CreatedOn    i64  @56
    ModifiedOn   i64  @64
    Comments     i64  @72
    Collaborator text @80
}

struct teamDocAt {
    ID    text @0
    Space text @8
}

struct teamDocEdit {
    ID        text       @0
    Space     text       @8
    Title     list<text> @16
    Parent    list<text> @24
    Teamspace list<text> @32
}

struct teamDocNew {
    Space     text @0
    Teamspace text @8
    Title     text @16
    Parent    text @24
}

struct teamDocs {
    Docs       list<bytes> @0
    Teamspaces list<bytes> @8
}

struct teamDocsRead {
    Space     text @0
    Teamspace text @8
    Parent    text @16
}

struct teamIn {
    Org   text @0
    Space text @8
}

struct teamInbox {
    Items  list<bytes> @0
    Unread i64         @8
}

struct teamInboxAll {
    Space text @0
}

struct teamInboxAt {
    ID    text @0
    Space text @8
}

struct teamInboxCleared {
    Read i64 @0
}

struct teamInboxItem {
    ID        text  @0
    Space     text  @8
    Reason    text  @16
    Read      bool  @24
    Archived  bool  @25
    CreatedOn i64   @32
    Channel   text  @40
    Doc       text  @48
    Thread    text  @56
    Message   bytes @64
}

struct teamInboxRead {
    Space    text @0
    Archived bool @8
}

struct teamMembers {
    Members  list<bytes> @0
    Degraded list<text>  @8
}

struct teamMembersRead {
    Space text @0
}

struct teamMessage {
    ID        text        @0
    Channel   text        @8
    Thread    text        @16
    Doc       text        @24
    Author    text        @32
    Text      text        @40
    Mentions  list<text>  @48
    CreatedOn i64         @56
    EditedOn  i64         @64
    Replies   i64         @72
    LastReply i64         @80
    Reactions list<bytes> @88
    Files     list<bytes> @96
}

struct teamMessageAt {
    ID    text @0
    Space text @8
}

struct teamMessageEdit {
    ID    text @0
    Space text @8
    Text  text @16
}

struct teamMessageRead {
    ID    text @0
    Space text @8
}

struct teamMessageWrite {
    ID    text        @0
    Space text        @8
    Text  text        @16
    Files list<bytes> @24
}

struct teamMessages {
    Messages list<bytes> @0
}

struct teamOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct teamReactionAt {
    ID    text @0
    Emoji text @8
    Space text @16
}

struct teamReactionWrite {
    ID    text @0
    Emoji text @8
    Space text @16
}

struct teamReplyWrite {
    ID    text        @0
    Space text        @8
    Text  text        @16
    Files list<bytes> @24
}

struct teamsIn {
    Limit  text @0
    Cursor text @8
    Q      text @16
    Sort   text @24
    Org    text @32
    User   text @40
}

struct teamsOut {
    Status   text        @0
    Msg      text        @8
    Data     list<bytes> @16
    Total    list<i64>   @24
    Next     text        @32
    Degraded list<text>  @40
}

interface team {
    # Returns one space with everyone who holds a role in it: their account
    # id, the name IAM holds for them, and the role — owners first, then admins,
    # members and guests. A space the org does not own is 404. SuperAdmin only.
    adminTeam(req: teamIn) returns (rep: teamOut)
    # Returns every org's team spaces, one page at a time: the org, the
    # space's id and name, who created it, how many people are in it and when it was
    # created.
    # Pages by keyset: pass `next` back as `cursor`, so a deep page costs what the
    # first one does. `q` matches name, slug, org and id; `org` narrows to one tenant;
    # `org` with `user` (an IAM user id) lists the spaces that person is in. Member
    # counts come from IAM per row, and a row IAM did not answer for carries no count
    # while `degraded` names the gap. SuperAdmin only.
    adminTeams(req: teamsIn) returns (rep: teamsOut)
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
    # Takes one person out of a channel — the caller leaving, when the
    # account is their own. Removing somebody else takes owning the channel or
    # administering the space. Leaving a channel you are not in, or removing somebody
    # who is not there, changes nothing. A direct message cannot be left: its people
    # are what it is. Answers 204.
    delete_team_channels_by_id_members_by_account(req: teamChannelLeave)
    # Removes a document with everything nested under it and every
    # comment on any of them — the Team client's own delete, which takes the
    # subtree with it. Its author, an owner of its teamspace or an admin of the
    # space may delete it. Answers 204.
    # The document model has no archived state for a page (only a teamspace can be
    # archived), so there is nothing softer to offer here than removal.
    delete_team_docs_by_id(req: teamDocAt)
    # Removes one blob from a space's file store. The caller must
    # hold a verified session AND be a member of the space; anything else — an
    # unknown space, another tenant's space, a space the caller is not
    # in — answers the same 404, so a probe learns nothing about what exists.
    # It is IDEMPOTENT: deleting a present or an absent blob both answer 204, so a
    # delete never confirms a blob's existence and a foreign blob id (a physical key
    # the caller can never name into another tenant's box) is a harmless no-op. A
    # storage backend that is unavailable fails closed with 502 rather than lying
    # about success.
    delete_team_files_by_space_by_filename(req: blobRef)
    # Removes a message, with its replies, reactions, files and the
    # inbox notifications that point at it. Its author may delete it, and so may an
    # owner or admin of the space.
    # Each removal is a platform remove through the Team client's write path, so the
    # message disappears from every open client live. Answers 204.
    delete_team_messages_by_id(req: teamMessageAt)
    # Takes back the caller's reaction to a message and answers the message
    # with its reactions as they now stand. Taking back a reaction the caller never
    # made changes nothing and is not an error.
    delete_team_messages_by_id_reactions_by_emoji(req: teamReactionAt) returns (rep: teamMessage)
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
    # Returns the channels the caller may see, with the kind and work
    # facet each carries.
    # A signed-in TEAM MEMBER reads every channel of every space they are in that is
    # open to them: public channels, and the private channels and direct messages
    # that name them. An APPLICATION of the org (its own machine credential) reads
    # the public channels of the org's spaces and nothing that belongs to
    # particular people. Anybody else without a team session is 401.
    # It reads the SAME Chunter documents the transactor serves, so a channel opened
    # in the Team client appears here with no sync, and a facet written here is read
    # by anything holding the document.
    get_team_channels() returns (rep: teamChannels)
    # Returns the people and agents in one channel, as the roster
    # describes them. The caller must be able to see the channel.
    get_team_channels_by_id_members(req: teamMessageRead) returns (rep: teamChannelMembers)
    # Returns the tail of one channel's conversation, oldest first, each
    # message with its reactions, files and thread count.
    # It reads the SAME Chunter documents the transactor serves, so a message typed
    # in the Team client is here with no sync. A team member reads any channel they
    # may see; an application of the org reads public channels only; anybody else
    # without a team session is 401. A channel the caller may not read answers 404
    # rather than 403, so a probe learns nothing about what exists.
    get_team_channels_by_id_messages(req: teamMessageRead) returns (rep: teamMessages)
    # Returns the documents of a space the caller may see, with the
    # teamspaces they are grouped in.
    # These are the Team client's own document rows, so a page created there is
    # here with no sync. Each carries the `collaborator` id its body opens with on
    # the /v1/team/collaborator socket. A private teamspace's documents are listed
    # only for its members.
    get_team_docs(req: teamDocsRead) returns (rep: teamDocs)
    # Returns one document the caller may see.
    get_team_docs_by_id(req: teamDocAt) returns (rep: teamDoc)
    # Returns the comments on a document, oldest first — the same
    # message shape a channel's conversation answers.
    get_team_docs_by_id_comments(req: teamDocAt) returns (rep: teamMessages)
    # Returns the caller's notifications, newest first, each with the
    # channel or document it is about and the message that caused it.
    # These are the rows the Team client's Inbox reads, so a notification cleared
    # there is cleared here. Only the caller's own notifications are ever listed.
    get_team_inbox(req: teamInboxRead) returns (rep: teamInbox)
    # Returns the people and agents in a space, with the name, avatar,
    # role and presence a client draws them with.
    # People come from IAM, the membership authority; agents from the org's agent
    # registry; avatars from the Person documents the Team client edits. An IAM that
    # does not answer is a 502 rather than an empty roster, and an agent registry
    # that does not answer is named in `degraded` beside the people who did load.
    # The caller must be a member of the space.
    get_team_members(req: teamMembersRead) returns (rep: teamMembers)
    # Returns a message's thread, oldest first, each reply with its
    # reactions and files.
    get_team_messages_by_id_replies(req: teamMessageAt) returns (rep: teamMessages)
    # Lists the channels orgs have published, across every org.
    # It is NOT part of GET /channels, and the separation is the point: that address
    # answers the CALLER'S channels, so folding these in would put strangers' channels
    # in somebody's own sidebar.
    # It reads the directory and never a tenant's store. Every field it can answer
    # with is one an org published by making a channel public, so there is nothing here
    # to scope by org — a directory only its own org can read is not a directory.
    # An authenticated principal is still required, because an anonymous crawler is
    # not who this is for.
    get_team_public(req: publicQuery) returns (rep: publicChannels)
    # Renames a channel, sets its topic, or archives or reopens it, and
    # answers the channel as it now stands.
    # Its owners and the space's admins may edit a channel; a channel nobody owns — one
    # opened by an integration — may be edited by anyone in it, and a direct
    # message by either person in it. Archiving withdraws a public channel from the
    # cross-org directory in the same write; reopening lists it again.
    patch_team_channels_by_id(req: teamChannelEdit) returns (rep: teamChannelOne)
    # Renames a document or moves it — under another document, to the top
    # of its teamspace, or to another teamspace with everything nested under it.
    # Anyone who can write the teamspace may rename or move a page, as in the Team
    # client. A move goes after its new siblings, and a document cannot be moved
    # under itself or under one of its own descendants.
    patch_team_docs_by_id(req: teamDocEdit) returns (rep: teamDoc)
    # Rewrites what a message says. Only its author may edit it.
    # The edit is an ordinary update of the message document through the Team
    # client's own write path, stamped with editedOn, so an open client shows the
    # new text and the "edited" mark live. Somebody the new text mentions for the
    # first time is notified.
    patch_team_messages_by_id(req: teamMessageEdit) returns (rep: teamMessage)
    # Opens a named channel and answers it as the store now holds it.
    # It writes through the SAME applyTx path the Team client uses, so a channel opened
    # here is broadcast to every live client of the space and appears in an open
    # sidebar without a reload — the same property listChannels rests on, read from the
    # write side.
    # A SIGNED-IN TEAM MEMBER opens it as themselves: they are its first member and
    # its owner, every other member named must be somebody the space knows (a person
    # in it, or one of the org's agents), and a guest may not open channels at all. An
    # application of the org opens it as the org, with exactly the members it names.
    # Anybody else without a team session is 401.
    # TWO TRANSACTIONS, NOT ONE, when the request states a facet. The document and
    # its mixin are separate writes in this model (bindChannel writes only the second),
    # and composing them here rather than inventing a combined tx keeps one write
    # path for each. A create that lands and a facet that does not is visible as a
    # channel with default intent, which is the honest partial state.
    post_team_channels(req: teamChannelNew) returns (rep: teamChannelOne)
    # Adds people to a channel and answers the channel as it now stands.
    # Anyone in the space except a guest may join a public channel by naming
    # themselves. Adding somebody else takes being in the channel already (or
    # administering the space, for a channel they can see). A direct message's people
    # are what it is, so none can be added — open another one. Everyone added must
    # be a member of the space or one of the org's agents; an agent added to a channel
    # answers when it is @-mentioned there.
    post_team_channels_by_id_members(req: teamChannelJoin) returns (rep: teamChannelOne)
    # Says one thing in a channel, as the caller.
    # The write goes through the SAME applyTx path the Team client's own messages
    # take and is broadcast to every connected client of the space, so a message
    # sent here appears live in an open channel rather than on the next reload. It
    # answers the message as the store now HOLDS it.
    # `<@account-uuid>` in the text is stored as the platform's mention, so the
    # person is notified in their inbox. Mentioning one of the org's agents — or
    # writing in a direct message with one — wakes it: it runs as itself, on behalf
    # of the caller, and posts its answer into the same channel. The caller must be
    # able to see the channel, and an archived channel refuses new messages (409).
    post_team_channels_by_id_messages(req: teamMessageWrite) returns (rep: teamMessage)
    # Opens the direct message between the caller and the people named,
    # or answers the one that already exists — there is one conversation per set of
    # people, however many times it is asked for.
    # Every person named must be a member of the space or one of the org's agents;
    # a direct message with an agent is a conversation it answers every message in,
    # and a guest may not open one.
    # Answers 201 when this call opened it and 200 when it was already there.
    post_team_dms(req: teamDirectOpen) returns (rep: teamDirect)
    # Creates a document, as the caller, after its siblings.
    # It is created through the Team client's own write path, so it appears in an
    # open Documents sidebar live, and its author is subscribed to it: a comment on
    # it lands in their inbox. Its body starts empty — open the returned
    # `collaborator` id on the /v1/team/collaborator socket to write it.
    # A space whose caller can write no teamspace at all gets one on its first
    # document: a public "General" teamspace every current member of the space is
    # in, which is what the Team client would otherwise make somebody create by hand
    # before the first page. Guests and agents are not made members of it.
    post_team_docs(req: teamDocNew) returns (rep: teamDoc)
    # Comments on a document, as the caller.
    # It is a message attached to the document, written through the Team client's
    # own path: the people subscribed to the document and anyone mentioned are
    # notified, and an agent mentioned answers with a comment of its own. It can be
    # replied to, reacted to, edited and deleted with the message ops.
    post_team_docs_by_id_comments(req: teamCommentWrite) returns (rep: teamMessage)
    # Archives one of the caller's notifications — read, and
    # out of the live inbox — and answers it. It is listed again with
    # `archived=true`.
    post_team_inbox_by_id_archive(req: teamInboxAt) returns (rep: teamInboxItem)
    # Marks one of the caller's notifications read and answers it.
    # Another person's notification is a 404, as one that does not exist is.
    post_team_inbox_by_id_read(req: teamInboxAt) returns (rep: teamInboxItem)
    # Marks every live notification of the caller's read, and
    # says how many it changed.
    post_team_inbox_read(req: teamInboxAll) returns (rep: teamInboxCleared)
    # Answers a message in its thread, as the caller.
    # A reply is a thread message attached to the one it answers, written through
    # the Team client's own path: the parent's reply count and last-reply time move
    # with it, the people already in the thread and anyone mentioned are notified,
    # and an agent mentioned in a reply answers in the same thread. A reply cannot
    # itself be replied to — threads are one level deep, as in the Team client.
    post_team_messages_by_id_replies(req: teamReplyWrite) returns (rep: teamMessage)
    # States what a channel is for: its lifecycle intent, and what it is
    # about. It answers the channel as it now stands.
    # The write is a platform MIXIN on the channel document, applied through the
    # SAME applyTx path the Team client's own writes take and broadcast to every
    # connected client — so a channel bound here updates live in an open space
    # rather than on the next reload. A team member binds a channel they can see; an
    # application of the org binds its public channels only; anybody else without
    # a team session is 401.
    put_team_channels_by_id(req: teamChannelBind) returns (rep: teamChannelOne)
    # Adds the caller's reaction to a message and answers the message with
    # its reactions as they now stand. Reacting twice with one emoji is one
    # reaction: the second call changes nothing.
    # The emoji is the last path segment, percent-encoded — PUT
    # /v1/team/messages/7a1c/reactions/%F0%9F%91%8D for 👍.
    put_team_messages_by_id_reactions_by_emoji(req: teamReactionWrite) returns (rep: teamMessage)
}

# ---------------------------------------------------------------------
# 35 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   get_team_transactor_statistics  statsOut.Statistics  team.statsSessions  (reaches one)
#   post_team_collaborator_rpc_by_documentid  collabRequest.Payload  team.collabPayload  (reaches one)
#   post_team_collaborator_rpc_by_documentid  collabResult.Content  map[string]string  (map)
#
# opaque (18) — crosses, arrives without its name:
#   publicChannels.Channels  team.listed (list element)
#   teamChannelMembers.Members  team.teamMember (list element)
#   teamChannelOne.Channel  team.teamChannel
#   teamChannels.Channels  team.teamChannel (list element)
#   teamCommentWrite.Files  team.teamFileIn (list element)
#   teamDirect.Channel  team.teamChannel
#   teamDocs.Docs  team.teamDoc (list element)
#   teamDocs.Teamspaces  team.teamTeamspace (list element)
#   teamInbox.Items  team.teamInboxItem (list element)
#   teamInboxItem.Message  team.teamMessage
#   teamMembers.Members  team.teamMember (list element)
#   teamMessage.Files  team.teamFile (list element)
#   teamMessage.Reactions  team.teamReaction (list element)
#   teamMessageWrite.Files  team.teamFileIn (list element)
#   teamMessages.Messages  team.teamMessage (list element)
#   teamOut.Data  team.teamDetail
#   teamReplyWrite.Files  team.teamFileIn (list element)
#   teamsOut.Data  team.teamRow (list element)
