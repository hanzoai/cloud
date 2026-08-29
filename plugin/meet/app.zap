# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package meet

struct callIn {
    Workspace text @0
    Room      text @8
}

struct meetHealth {
    Ready   bool @0
    Service text @8
    Status  text @16
}

struct recordIn {
    Room text @0
}

struct recording {
    Room    text @0
    ID      text @8
    Status  text @16
    Bucket  text @24
    Object  text @32
    Started i64  @40
    Error   text @48
}

struct venue {
    Name  text @0
    WS    text @8
    Ready bool @16
}

interface meet {
    # Health reports whether the office can mint join tokens.
    # It reports whether this deployment holds the LiveKit key pair it needs:
    # ready:true with 200 when tokens can be minted, the SAME body with ready:false,
    # status "degraded" and 503 when they cannot — so a probe and a dashboard both
    # read the degraded state instead of someone grepping a boot log.
    # It takes no credential and is reachable on every public host, so it withholds
    # both the reason and the signing key's name on purpose: ready is the whole
    # dashboard fact, and the reason — which names the key file and the Secret — is
    # written to the boot log where an operator already is.
    get_meet_health() returns (rep: meetHealth)
    # Answers where a room's call happens, for a caller who may join it.
    # It is the "resolved at render" half of HIP-0523 §12: a surface showing a channel
    # asks for the room's call at the moment it draws one, rather than reading a media
    # room name someone stored on the room. Nothing here is persisted and nothing is
    # created — a media room begins existing when the first participant connects and
    # stops when the last leaves, so there is no call to create and none to clean up.
    # AUTHORIZATION IS THE JOIN DECISION, unchanged and shared. It delegates to
    # state.admits, the same function POST /v1/meet/getToken and all three recording
    # operations admit on, so a caller who is told where a call is, is a caller who
    # could have joined it. Answering the address to someone who cannot join would make
    # this a workspace-membership oracle for anyone who can guess a room id.
    # It deliberately does NOT report whether a call is in progress. That is a fact the
    # media server holds and this binary would have to ask for it over the network,
    # which is a different decision with a different failure mode — and reporting
    # "nobody is in this call" when the question could not be asked would be exactly the
    # unknown-rendered-as-zero this surface refuses elsewhere.
    meetCall(req: callIn) returns (rep: venue)
    # Answers what is being recorded in a room, and where the file went.
    # It reports the recording that is RUNNING, and once none is, the most recent one
    # the media server still holds — with its final status and its object. That second
    # case is the one that matters for finding a file: the answer to a start is the
    # only other place the location appears, and a client that lost it, or a colleague
    # who was not the one to press record, has nowhere else to look.
    # It is behind the same check as starting one: where a recording of a private
    # conversation is kept is a fact about that conversation, so it is told to the
    # people the room admits and to nobody else.
    meetRecordRead(req: recordIn) returns (rep: recording)
    # Begins recording a room, or hands back the recording already running.
    # A recording is a durable artifact of a conversation, so only someone this room
    # would admit may make one: the caller is authorized by the SAME decision
    # /v1/meet/getToken makes about the same room, and refused with the same 401.
    # A SECOND START RETURNS THE FIRST rather than refusing it. There is at most one
    # recording per room and this operation's job is to establish that there is one —
    # which is already true when a colleague, or the caller's own double-click,
    # started it a moment ago. The answer is the same shape either way, naming the
    # recording that is actually running, so a client never has to tell the two cases
    # apart to find the id.
    # A deployment with no media server address or no object store answers 503 naming
    # which, because a recording that silently does not happen is worse than one that
    # is refused. The reason reaches only a caller this room already admits.
    meetRecordStart(req: recordIn) returns (rep: recording)
    # Ends a room's recording — EVERY one of them.
    # Whoever the room admits may stop it, including someone who did not start it:
    # a person being recorded has to be able to end it, and a rule that only the
    # starter may stop would deny exactly that. Stopping is free — a caller made to
    # pay to stop being recorded would be paying for the wrong thing.
    # 200 MEANS THE ROOM IS NOT BEING RECORDED, and that is why this ends all of them
    # rather than the first. "At most one per room" is an invariant this surface wants
    # and cannot impose: reading the list and starting are two calls, and two replicas
    # racing through that window both start. When the list comes back holding two, two
    # is the truth — and ending one while answering 200 tells the person withdrawing
    # consent that it stopped while a second worker keeps writing. A stop that cannot
    # finish the job says so instead.
    # Stopping a room that is not being recorded is not an error. The answer names the
    # room with no recording on it, which is the state the caller asked for.
    meetRecordStop(req: recordIn) returns (rep: recording)
}
