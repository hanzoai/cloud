# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package exec

struct CodeResult {
    SessionID text        @0
    Stdout    text        @8
    Stderr    text        @16
    Files     list<bytes> @24
}

struct CodeRun {
    Lang               text        @0
    Code               text        @8
    Args               list<text>  @16
    Files              list<bytes> @24
    SessionID          text        @32
    UserID             text        @40
    RuntimeSessionHint text        @48
}

struct sessionRef {
    SID text @0
}

interface exec {
    # Lists the files in an execution session.
    # Everything the session's sandbox holds — the uploads a run can read and the
    # artifacts it produced — each then fetched from GET /v1/exec/download.
    # The answer is a BARE JSON ARRAY of {name, lastModified}, where `name` is the
    # same {session_id}/{fileId} identifier download takes, because that is what the
    # client matches on. The obvious typed shape, `{files: […]}`, would have been a
    # silent wire change: the request still succeeds and `response.data.find(...)`
    # finds nothing, which reads as a session holding no files.
    # The NAME of this handler is what the published summary is cut from, and it used
    # to leak: the comment opened "Files lists …", which is not this function's
    # identifier, so zipdoc's exact-match strip left it and every SDK, tool list and
    # CLI help line opened with a Go symbol no caller can see. An openapi.Describe
    # stated a better summary beside the route and was DISCARDED — Fold replaces a
    # structural operation with the typed one — so the declaration read as landed and
    # rendered nowhere. The comment is the one home for this sentence.
    # One recursive `find`, the same traversal the artifact sweep makes. It used to be
    # `ls -1A` — top level only — while the sweep collected with `find`, so a run that
    # wrote a nested artifact reported it in its reply and then omitted it here, and
    # the client's prefix match read the file as expired. Two traversals of one
    # directory is two answers about what a session holds; there is one now.
    get_exec_files_by_sid(req: sessionRef)
    # Executes a program in a throwaway sandbox and answers with what it printed
    # and what it left behind.
    # `lang` names one of the thirteen the sandbox image carries — py, js, ts, bash, r,
    # php, go, rs, c, cpp, java, d, f90 — and `code` is the whole program, not a
    # fragment: a compiled language is compiled and then run, an interpreted one is
    # interpreted, and `args` becomes the program's own argv either way. Nothing is
    # installed for you; the image is the environment.
    # A PROGRAM THAT FAILS IS A SUCCESSFUL CALL. A non-zero exit answers 200 with the
    # diagnostics on `stderr`, because "the code threw" and "the interpreter is down"
    # are different facts a caller renders differently. Only the second is an error
    # status.
    # Runs are stateful through `session_id`. Omit it and the run gets a fresh sandbox
    # whose id comes back on the answer; pass that id again and the next run sees the
    # same filesystem, so a program can write a file one call and read it the next.
    # `files` names bytes already uploaded to a session (POST /v1/exec/upload), copied in
    # before the program starts. `files` on the ANSWER is what the program created or
    # changed, by comparison against a marker taken at start — so it is the run's real
    # output, not a listing of the directory — and each is fetched from
    # GET /v1/exec/download/{session}/{name}.
    # The tenant is the caller's, never the body's, at every entry point. A typed op is
    # also an MCP tool and an op-plane op; MCP's tools/call invokes it directly, with no
    # route and therefore no middleware, so nothing there could have checked a
    # credential. tenantOf refuses a context carrying neither a validated principal nor
    # exec's own admission marker, so those entry points fail closed without a second
    # gate to keep in step.
    post_exec(req: CodeRun) returns (rep: CodeResult)
    # Answers 501 — this deployment does not serve programmatic tool calling.
    # That sentence is the SUMMARY every projection shows, so it says what a caller
    # gets rather than what the code does; the rest names what it would take to stop
    # refusing.
    # /exec/programmatic is NOT this contract's sibling — it is a different protocol on
    # an adjacent path: a multi-round-trip loop where the server suspends a Python
    # program on a tool call, returns the pending calls with a continuation_token, and
    # resumes when the client posts the results back (@hanzochat/agents
    # ProgrammaticToolCalling). Implementing it means implementing suspension and
    # resumption, which is a program, not an endpoint.
    # So it answers 501 with that fact rather than being routed into `run`, which would
    # hand the caller a CodeResult its parser cannot read — a wrong answer, where this is a
    # refusal a client can act on.
    # IT IS A TYPED OP, and the refusal for keeping it raw did not survive reading. It
    # binds no body, opens no stream, relays no other process and sits on no wildcard,
    # so none of the four wire facts that keep a route raw applies to it; what was
    # cited instead was that a permanent stub should declare nothing. That argues for
    # silence in the DOCUMENT and buys the silence everywhere else too — no MCP tool,
    # no CLI command, no SDK method — so a caller could read this address and reach it
    # by no projection but REST, and learn only by calling it that the protocol is not
    # served. Declaring `zip.WithStatus(501)` is what makes typing honest: the document
    # publishes the ONE status this route sends, over an Out with no schema, rather
    # than the 204 a void op would otherwise have invented.
    # ONE delta, pinned by TestProgrammaticRefusesEveryBody: a body that is not JSON
    # now answers 400 rather than 501, because op.invoke decodes before the handler is
    # entered. Both are refusals of a protocol this deployment does not serve, no real
    # caller sends one, and 400 is what the rest of the fleet answers to bytes it
    # cannot parse.
    # It asks tenantOf for the reason every other operation here does, and the answer
    # is the same on the wire it was: over HTTP the credential middleware has already
    # run, so an admitted caller still reads 501. What the call closes is the entry
    # point a route table cannot see — typing an operation makes it an MCP tool, which
    # zip dispatches straight into the handler with no route and therefore no
    # middleware. Uniformity is the whole property: every path into this subsystem
    # reads the admission marker, so there is no operation anybody has to remember is
    # the exception.
    post_exec_programmatic()
}

# ---------------------------------------------------------------------
# 3 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   CodeResult.Files  exec.CodeFile (list element)
#   CodeRun.Files  exec.CodeFile (list element)
