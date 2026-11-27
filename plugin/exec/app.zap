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
    # It lists with one recursive `find`, the same traversal the artifact sweep
    # makes, so a nested artifact a run reports in its reply is listed here too: one
    # traversal of the directory, one answer about what a session holds.
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
    # credential. tenantOf refuses a context carrying no validated principal, so those
    # entry points fail closed without a second gate to keep in step.
    post_exec(req: CodeRun) returns (rep: CodeResult)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   CodeResult.Files  exec.CodeFile (list element)
#   CodeRun.Files  exec.CodeFile (list element)
