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

struct ComputerIn {
    ID         text      @0
    Action     text      @8
    Credential text      @16
    Field      text      @24
    Submit     bool      @32
    X          list<i64> @40
    Y          list<i64> @48
    ToX        list<i64> @56
    ToY        list<i64> @64
    DY         list<i64> @72
    Button     text      @80
    Double     bool      @88
    Text       text      @96
    Keys       text      @104
    URL        text      @112
    Command    text      @120
    Seconds    f64       @128
}

struct ComputerOut {
    OK    bool @0
    Text  text @8
    Image text @16
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
    Cluster text @40
    Disk    bool @48
    Bearer  text @56
}

struct Leased {
    ID        text @0
    Class     text @8
    Runtime   text @16
    Status    text @24
    Workdir   text @32
    Cluster   text @40
    ExpiresAt i64  @48
    Carried   bool @56
    EndsAt    i64  @64
}

struct PathIn {
    ID   text @0
    Path text @8
}

struct Ports {
    Ports list<bytes> @0
}

struct Quota {
    Account text @0
    Running i64  @8
    Quota   i64  @16
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
    Events     bool       @64
}

struct Sandbox {
    ID            text @0
    Org           text @8
    Kind          text @16
    Actor         text @24
    Class         text @32
    Project       text @40
    Status        text @48
    Image         text @56
    Pod           text @64
    Runtime       text @72
    Volume        text @80
    Cluster       text @88
    Error         text @96
    CreatedAt     i64  @104
    LastUsedAt    i64  @112
    ConnectedAt   i64  @120
    ExpiresAt     i64  @128
    Payer         text @136
    MeteredAt     i64  @144
    DiskMeteredAt i64  @152
}

struct Session {
    ID        text  @0
    Label     text  @8
    Zap       text  @16
    Sandbox   text  @24
    Lease     text  @32
    Expires   text  @40
    Execution bytes @48
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

struct dev_endIn {
    ID text @0
}

struct endIn {
    ID    text @0
    Purge text @8
}

struct endSessionIn {
    ID    text @0
    Lease text @8
}

struct execRequest {
    ID         text       @0
    Argv       list<text> @8
    Command    text       @16
    Stdin      text       @24
    Dir        text       @32
    TimeoutSec i64        @40
}

struct healthOut {
    Status text @0
}

struct portsIn {
    ID text @0
}

struct previewGrant {
    URL       text @0
    Host      text @8
    Port      i64  @16
    ExpiresIn i64  @24
}

struct previewIn {
    ID   text @0
    Port i64  @8
}

struct sandboxFilter {
    Project text @0
    Status  text @8
}

struct sandboxIn {
    Class   text @0
    Project text @8
    Image   text @16
    Runtime text @24
    TTLSec  i64  @32
    Cluster text @40
}

struct sandboxList {
    Sandboxes list<bytes> @0
}

struct sandboxRef {
    ID text @0
}

struct sessionIn {
    Runtime   text  @0
    Thread    text  @8
    Execution bytes @16
}

struct ticketGrant {
    Ticket    text @0
    ExpiresIn i64  @8
    URL       text @16
}

interface sandbox {
    # Performs one GUI or shell action inside a sandbox the caller
    # holds: screenshot, click, move, drag, scroll, type, key, navigate, wait or
    # shell. Coordinates in click, move, drag and scroll are pixel coordinates of the
    # screenshot.
    computer_in_sandbox(req: ComputerIn) returns (rep: ComputerOut)
    # Gives the cell back (or releases a linked-desktop dispatch lease). The disk is
    # kept; a lease ends, a person's work does not. A cell or lease already gone is already
    # ended.
    delete_dev_sessions_by_id(req: endSessionIn)
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
    # Answers that the service is mounted.
    get_dev_health() returns (rep: healthOut)
    # Lists the sandboxes the caller holds, newest first.
    # A member holds the sandboxes they leased; an admin of the org, or a SuperAdmin,
    # holds every one in it. `?project=` and `?status=` narrow it. Only the caller's
    # org's: the store is keyed on the validated org, so another tenant's sandbox is
    # not something this operation can return.
    get_sandbox(req: sandboxFilter) returns (rep: sandboxList)
    # Returns one sandbox: its class, project, image, the runtime it was
    # given, its status and when its lease ends.
    # A sandbox is its lessee's and its org admins'. An id the caller does not hold —
    # another org's, or another member's — is the same 404 an unknown id gives.
    get_sandbox_by_id(req: sandboxRef) returns (rep: Sandbox)
    # Lists the TCP ports something listens on in a sandbox the caller
    # holds, each with the preview host that serves it — what the sandbox's Browser
    # can open. A dev server started in the sandbox appears here once it listens.
    get_sandbox_by_id_ports(req: portsIn) returns (rep: Ports)
    # Answers the caller's sandbox quota: the account it belongs to — the org, or a
    # person in the signup org — how many of that account's paid sandboxes are
    # running, and how many may run at once (-1 for no bound). Staff set the quota per
    # org or per plan; where they set none it is 100.
    get_sandbox_quota() returns (rep: Quota)
    # Leases the caller's sandbox, or returns the one it named if that
    # lease is still running, or gives it a pod again if it is parked — with its
    # disk as it was left.
    # What comes back is a real computer: a pod under a runtime boundary with a
    # toolchain already in it, its own filesystem, and a lease that ends it. Every
    # other op here acts on the one this returns.
    lease_sandbox(req: LeaseIn) returns (rep: Leased)
    # Leases the caller's cell (or resolves a linked desktop when execution.via
    # is "desktop"), or resumes it, and starts Dev in it.
    # The cell the project already holds is found by asking the sandbox who has it;
    # leasing over it would be refused, because a project's disk attaches to one cell.
    # A lease the sandbox refuses — quota, no node — is the answer, untouched. A desktop
    # dispatch refusal is likewise returned untouched with no fallback.
    post_dev_sessions(req: sessionIn) returns (rep: Session)
    # Stops the commands running in a session's cell and keeps the cell and
    # the session.
    post_dev_sessions_by_id_interrupt(req: dev_endIn) returns (rep: Stopped)
    # Brings a suspended session back.
    post_dev_sessions_by_id_resume(req: dev_endIn) returns (rep: Session)
    # Puts a session away: its cell is given back, its disk and its work stay, and
    # it takes no turn until resumed.
    post_dev_sessions_by_id_suspend(req: dev_endIn)
    # Leases a sandbox — a real computer — for the caller's org.
    # The class decides what it is for and therefore its image, working directory
    # and isolation. A dev or desktop sandbox is SINGLE-ATTACH per project, so
    # asking twice for one project resumes the one that exists rather than paying
    # for a second; an exec sandbox carries no project and is bounded per org
    # instead, refused 429 past the ceiling because the caller's correct response is
    # to wait.
    # Answers 201 with the sandbox as leased, which names the runtime it GOT — not
    # the one that was asked for.
    post_sandbox(req: sandboxIn) returns (rep: Sandbox)
    # Runs one command in a sandbox the caller holds and answers with
    # its exit code, stdout and stderr.
    # Send `argv` — an argument vector cannot be word-split by accident — or
    # `command` for a shell line, which is the only input here that ever reaches a
    # shell. A non-zero exit is a SUCCESSFUL call carrying a failed command: the
    # status is 200 and the exit code is in the answer, because "the command failed"
    # and "the call failed" are different facts.
    post_sandbox_by_id_exec(req: execRequest) returns (rep: ExecResult)
    # Stops the pod, keeps the row and the volume, and settles the tail: POST
    # /v1/sandbox/:id/pause.
    # The claim, the stop and the ship are ONE settlement for the same reason a
    # retirement is — the snapshot that carries the advanced watermark has to carry
    # the new state, or a successor hydrates a running row for a pod that is gone
    # and bills for it until the reaper notices.
    post_sandbox_by_id_pause(req: sandboxRef) returns (rep: Sandbox)
    # Opens a port of a sandbox the caller holds in a browser.
    # It answers a URL at an origin of the preview's own —
    # https://sandbox-<id>-preview-<port>.<apex>/ — carrying a single-use ticket.
    # Opening it sets a cookie on that origin and lands on its root, and from then on
    # every request there is carried to the port inside the sandbox: pages, assets,
    # APIs and WebSockets, as the app serves them on localhost. The preview stays open
    # for twelve hours or until the sandbox stops running; a preview answering 401 is
    # opened again by asking for another URL. A sandbox that is not running is 409.
    post_sandbox_by_id_preview(req: previewIn) returns (rep: previewGrant)
    # Gives a parked sandbox a pod again: POST /v1/sandbox/:id/resume.
    # The pod is NEW and so is its lease: a fresh pod name, because a pod name is
    # never reused (store.go), the volume the row already names, a lease of the
    # class's own length from now, and the RESUMING caller's credential rather than
    # the one that took the lease — a session is short-lived and the one that parked
    # it is gone.
    post_sandbox_by_id_resume(req: sandboxRef) returns (rep: Sandbox)
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
    # session — GET /v1/agent/sessions/stream, scoped to one run with ?root= —
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
# 25 op(s) here. What follows is what this schema does not carry.
#
# opaque (4) — crosses, arrives without its name:
#   Ports.Ports  client.Port (list element)
#   Session.Execution  dev.Execution
#   sandboxList.Sandboxes  sandbox.Sandbox (list element)
#   sessionIn.Execution  dev.Execution
