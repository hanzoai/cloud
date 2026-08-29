# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package sandbox

struct Blob {
    Path    text       @0
    Dir     bool       @8
    Data    bytes      @16
    Entries list<text> @24
}

struct EndIn {
    ID    text @0
    Purge bool @8
}

struct ExecResult {
    ExitCode i64  @0
    Stdout   text @8
    Stderr   text @16
}

struct LeaseIn {
    ID      text @0
    Class   text @8
    Project text @16
    Runtime text @24
    TTLSec  i64  @32
}

struct Leased {
    ID      text @0
    Class   text @8
    Runtime text @16
    Status  text @24
    Workdir text @32
}

struct PathIn {
    ID   text @0
    Path text @8
}

struct Ran {
    ExitCode i64  @0
    Stdout   text @8
    Stderr   text @16
}

struct RunIn {
    ID         text       @0
    Argv       list<text> @8
    Command    text       @16
    Stdin      text       @24
    Dir        text       @32
    TimeoutSec i64        @40
    Blind      list<text> @48
    Session    text       @56
}

struct Sandbox {
    ID          text @0
    Org         text @8
    Kind        text @16
    Class       text @24
    Project     text @32
    Status      text @40
    Image       text @48
    Pod         text @56
    Runtime     text @64
    Volume      text @72
    Error       text @80
    CreatedAt   i64  @88
    LastUsedAt  i64  @96
    ConnectedAt i64  @104
    ExpiresAt   i64  @112
    Payer       text @120
    MeteredAt   i64  @128
}

struct StopIn {
    ID text @0
}

struct Stopped {
    Stopped i64 @0
}

struct WriteIn {
    ID   text  @0
    Path text  @8
    Data bytes @16
}

struct Wrote {
    Path  text @0
    Bytes i64  @8
}

struct endIn {
    ID    text @0
    Purge text @8
}

struct execRequest {
    ID         text       @0
    Argv       list<text> @8
    Command    text       @16
    Stdin      text       @24
    Dir        text       @32
    TimeoutSec i64        @40
}

struct leaseIn {
    Class   text @0
    Project text @8
    Image   text @16
    Runtime text @24
    TTLSec  i64  @32
}

struct sandboxFilter {
    Project text @0
    Status  text @8
}

struct sandboxList {
    Sandboxes list<bytes> @0
}

struct sandboxRef {
    ID text @0
}

struct ticketGrant {
    Ticket    text @0
    ExpiresIn i64  @8
    URL       text @16
}

interface sandbox {
    # Ends a sandbox and releases the compute behind it. Answers 204.
    # ENDING IS NOT STOPPING. This releases the resource: the pod goes and anything
    # only inside it goes with it. To end what a sandbox is RUNNING while keeping
    # the sandbox — the checkout, the logs, the half-written file — the verb is
    # POST /v1/sandbox/stop.
    # `?purge=1` additionally removes the record, so the sandbox stops being listed
    # at all rather than being listed as ended.
    delete_sandbox_by_id(req: endIn)
    # Ends the caller's sandbox lease: the pod goes, and the volume goes only
    # when the caller asked for that too.
    end_sandbox(req: EndIn)
    # Lists the caller org's sandboxes, newest first.
    # `?project=` and `?status=` narrow it. Only the caller's org's: the store is
    # keyed on the validated org, so another tenant's sandbox is not something this
    # operation can return.
    get_sandbox(req: sandboxFilter) returns (rep: sandboxList)
    # Returns one sandbox: its class, project, image, the runtime it was
    # given, its status and when its lease ends.
    # An id the caller's org does not hold is the same 404 an unknown id gives — the
    # store is keyed on the org, so a cross-tenant id simply is not there.
    get_sandbox_by_id(req: sandboxRef) returns (rep: Sandbox)
    # Leases the caller's sandbox, or returns the one it named if that
    # lease is still running.
    # What comes back is a real computer: a pod under a runtime boundary with a
    # toolchain already in it, its own filesystem, and a lease that ends it. Every
    # other op here acts on the one this returns.
    lease_sandbox(req: LeaseIn) returns (rep: Leased)
    # Leases a sandbox — a real computer — for the caller's org.
    # The class decides what it is for and therefore its image, working directory
    # and isolation. A dev or desktop sandbox is SINGLE-ATTACH per project, so
    # asking twice for one project resumes the one that exists rather than paying
    # for a second; an exec sandbox carries no project and is bounded per org
    # instead, refused 429 past the ceiling because the caller's correct response is
    # to wait.
    # Answers 201 with the sandbox as leased, which names the runtime it GOT — not
    # the one that was asked for.
    post_sandbox(req: leaseIn) returns (rep: Sandbox)
    # Runs one command in a sandbox the caller holds and answers with
    # its exit code, stdout and stderr.
    # Send `argv` — an argument vector cannot be word-split by accident — or
    # `command` for a shell line, which is the only input here that ever reaches a
    # shell. A non-zero exit is a SUCCESSFUL call carrying a failed command: the
    # status is 200 and the exit code is in the answer, because "the command failed"
    # and "the call failed" are different facts.
    post_sandbox_by_id_exec(req: execRequest) returns (rep: ExecResult)
    # Mints a short-lived grant to open the screen of a desktop
    # sandbox. Same properties as the terminal ticket, for the other endpoint.
    post_sandbox_by_id_screen_ticket(req: sandboxRef) returns (rep: ticketGrant)
    # Mints a short-lived grant to open a terminal on a sandbox.
    # The ticket travels in the query string of the URL it answers with, because a
    # browser cannot set an Authorization header on a WebSocket handshake. It is
    # single-purpose and short-lived for exactly that reason. A sandbox that is not
    # running is 409 rather than a ticket that cannot be used.
    post_sandbox_by_id_terminal_ticket(req: sandboxRef) returns (rep: ticketGrant)
    # Reads one path in the caller's sandbox: a file's bytes, or a
    # directory's entries when the path names one.
    read_sandbox_file(req: PathIn) returns (rep: Blob)
    # Runs one command inside the caller's sandbox and answers its exit code,
    # stdout and stderr. A non-zero exit is a successful call carrying a failed
    # program, so it comes back as data and not as an error.
    # Name a `session` and the command NARRATES INTO IT: its output is appended to
    # that session's live log as the program produces it, so anything watching the
    # session — GET /v1/agents/sessions/stream, scoped to one run with ?root= —
    # watches the work happen rather than waiting for the verdict. Without it the
    # call is what it always was: silent until it returns, which for an agentic run
    # is twenty-five minutes of blank screen.
    # The session is named; the TENANT is not. It is the org the caller already
    # proved, so a session belonging to somebody else is absent from the org this
    # call acts for and the append is refused there.
    run_in_sandbox(req: RunIn) returns (rep: Ran)
    # Interrupts whatever the caller's sandbox is running and answers how
    # many commands it ended. The sandbox stays leased — stop ends the WORK, end ends
    # the RESOURCE — so whoever stopped a run can still read what it left behind.
    stop_run(req: StopIn) returns (rep: Stopped)
    # Writes bytes to one path in the caller's sandbox, creating parents,
    # and answers the resolved path.
    write_sandbox_file(req: WriteIn) returns (rep: Wrote)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   sandboxList.Sandboxes  sandbox.Sandbox (list element)
