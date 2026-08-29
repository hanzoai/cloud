# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package mq

struct Config {
    Name       text       @0
    Subjects   list<text> @8
    Retention  text       @16
    MaxMsgs    i64        @24
    MaxBytes   i64        @32
    MaxAge     text       @40
    MaxMsgSize i32        @48
    Storage    text       @56
    Replicas   i64        @64
}

struct Consumer {
    Name        text  @0
    Stream      text  @8
    Config      bytes @16
    Delivered   bytes @24
    AckFloor    bytes @32
    Pending     u64   @40
    Redelivered i64   @48
    Waiting     i64   @56
    AckPending  i64   @64
    Created     bytes @72
}

struct Health {
    Status  text @0
    Version text @8
    Uptime  text @16
}

struct Purge {
    Name   text @0
    Filter text @8
    Keep   u64  @16
}

struct Stream {
    Name    text  @0
    Config  bytes @8
    State   bytes @16
    Created bytes @24
}

struct Streams {
    Streams list<bytes> @0
    Total   i64         @8
}

struct infoOut {
    Server     text @0
    Name       text @8
    Version    text @16
    JetStream  bool @24
    MaxPayload i64  @32
    Streams    i64  @40
}

struct listIn {
    Limit  i64 @0
    Offset i64 @8
}

struct makeIn {
    Stream  text  @0
    Durable bytes @8
}

struct nameIn {
    Name text @0
}

struct nextIn {
    Stream  text @0
    Name    text @8
    Batch   i64  @16
    Expires text @24
    NoWait  bool @32
}

struct pickIn {
    Stream text @0
    Limit  i64  @8
    Offset i64  @16
}

struct pickOut {
    Consumers list<bytes> @0
    Total     i64         @8
}

struct purgeOut {
    Purged u64 @0
}

struct readIn {
    Name          text @0
    Seq           u64  @8
    LastBySubject text @16
    NextBySubject text @24
    Limit         i64  @32
}

struct seqIn {
    Name text @0
    Seq  u64  @8
}

struct twoIn {
    Stream text @0
    Name   text @8
}

interface mq {
    # Removes a stream with all its messages and consumers. Irreversible.
    delete_mq_stream_by_name(req: nameIn)
    # Erases one message by sequence; the sequence gap remains.
    delete_mq_stream_by_name_message_by_seq(req: seqIn)
    # Removes a consumer and its delivery state; unacknowledged messages
    # stay in the stream.
    delete_mq_stream_by_stream_consumer_by_name(req: twoIn)
    # Reports whether the message plane behind this surface answers.
    get_mq_health() returns (rep: Health)
    # Returns the broker's identity and the org's stream count.
    get_mq_info() returns (rep: infoOut)
    # Returns the org's streams, name-ordered, with their live state.
    get_mq_stream(req: listIn) returns (rep: Streams)
    # Returns one stream's configuration and live state.
    get_mq_stream_by_name(req: nameIn) returns (rep: Stream)
    # Returns a stream's consumers, name-ordered, with delivery state.
    get_mq_stream_by_stream_consumer(req: pickIn) returns (rep: pickOut)
    # Returns one consumer's configuration and delivery state.
    get_mq_stream_by_stream_consumer_by_name(req: twoIn) returns (rep: Consumer)
    # Creates a durable stream in the org's namespace and returns it.
    post_mq_stream(req: Config) returns (rep: Stream)
    # Removes messages from a stream, leaving its consumers in place.
    post_mq_stream_by_name_purge(req: Purge) returns (rep: purgeOut)
    # Creates a durable pull consumer on a stream and returns it.
    post_mq_stream_by_stream_consumer(req: makeIn) returns (rep: Consumer)
    # Reconfigures an existing stream; the path names the stream, and the
    # immutable fields (storage, retention) must restate what they are.
    put_mq_stream_by_name(req: Config) returns (rep: Stream)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# dropped (2) — the value does not cross, and nothing fails:
#   Consumer.Created  time.Time  (empty message)
#   Stream.Created  time.Time  (empty message)
#
# blocked (2) — the op is absent; the field has no wire form:
#   get_mq_stream_by_name_message  readOut.Messages  []mq.Delivery  (no wire form)
#   post_mq_stream_by_stream_consumer_by_name_next  readOut  mq.readOut  (reaches one)
#
# opaque (10) — crosses, arrives without its name:
#   Consumer.AckFloor  mq.Sequences
#   Consumer.Config  mq.Durable
#   Consumer.Created  time.Time
#   Consumer.Delivered  mq.Sequences
#   Stream.Config  mq.Config
#   Stream.Created  time.Time
#   Stream.State  mq.State
#   Streams.Streams  mq.Stream (list element)
#   makeIn.Durable  mq.Durable
#   pickOut.Consumers  mq.Consumer (list element)
