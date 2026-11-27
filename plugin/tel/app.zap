# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package tel

struct Call {
    ID     text @0
    From   text @8
    To     text @16
    Status text @24
    Org    text @32
    Agent  text @40
}

struct Number {
    ID       text       @0
    E164     text       @8
    Country  text       @16
    Type     text       @24
    Org      text       @32
    Capable  list<text> @40
    Monthly  i64        @48
    Currency text       @56
}

struct SMS {
    ID     text @0
    From   text @8
    To     text @16
    Text   text @24
    Status text @32
    Org    text @40
}

struct buyInput {
    E164 text @0
}

struct callInput {
    From    text @0
    To      text @8
    Agent   text @16
    Record  bool @24
    Webhook text @32
}

struct callList {
    Data list<bytes> @0
}

struct idInput {
    ID text @0
}

struct messageInput {
    From  text       @0
    To    text       @8
    Text  text       @16
    Media list<text> @24
}

struct messageList {
    Data list<bytes> @0
}

struct numberList {
    Data list<bytes> @0
}

struct searchInput {
    Country text @0
    Area    text @8
    Type    text @16
    Limit   i64  @24
}

struct summary {
    Numbers  i64 @0
    Calls    i64 @8
    Messages i64 @16
}

interface tel {
    # Ends a call this org placed. The holding is read for THIS org before the
    # carrier is asked, for the reason releaseNumber gives one surface up: an id
    # belonging to another tenant would otherwise be hung up by whoever guessed it.
    delete_tel_calls_by_id(req: idInput)
    # Checks the holding is THIS org's before it reaches the carrier.
    # Without that read, an id belonging to another tenant would be released by
    # whoever guessed it.
    delete_tel_numbers_by_id(req: idInput)
    # Lists the calls this org has placed or received, newest first. Like the
    # message list beside it, these are our own records rather than the carrier's.
    get_tel_calls() returns (rep: callList)
    # Lists the messages this org has sent or received, newest first. Records from
    # our own store, not the carrier's — so it is what this platform did on the
    # org's behalf, which is the set an audit or a bill has to agree with.
    get_tel_messages() returns (rep: messageList)
    # Lists the phone numbers this org HOLDS — the ones it has bought and not
    # released. Distinct from the availability search one path down
    # (`/numbers/available`), which asks the carrier what could be bought: this
    # answers only from our own store, so it is what an org owns rather than what
    # it could own.
    get_tel_numbers() returns (rep: numberList)
    # Asks the carrier what is available to buy. Nothing is recorded —
    # a search is not a holding, and treating it as one is how inventory leaks.
    get_tel_numbers_available(req: searchInput) returns (rep: numberList)
    # Counts what this org holds on the telephony plane: its numbers, its calls and
    # its messages. The one read a dashboard makes before it asks for any list, so
    # it answers three totals and no rows.
    get_tel_summary() returns (rep: summary)
    # Dials. An `agent` names a Hanzo assistant to answer it; the call is
    # refused up front when no assistant plane is configured, because a call that
    # connects to silence has already cost the person who answered it.
    post_tel_calls(req: callInput) returns (rep: Call)
    # Sends a message from one of this org's own numbers.
    # `from` must be a number the org HOLDS, checked against the store rather than
    # taken on trust — a caller that could send from any number could impersonate
    # one, and the carrier would deliver it. `to` is required, and the body needs
    # text or media, because a message with neither is delivered as nothing and
    # billed as something.
    post_tel_messages(req: messageInput) returns (rep: SMS)
    # Provisions with the carrier FIRST and records second. The other order
    # records a holding that may not exist, and a number the platform believes it owns
    # but cannot use is worse than one it failed to buy.
    post_tel_numbers(req: buyInput) returns (rep: Number)
}

# ---------------------------------------------------------------------
# 10 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   callList.Data  tel.Call (list element)
#   messageList.Data  tel.SMS (list element)
#   numberList.Data  tel.Number (list element)
