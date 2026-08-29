# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package ai

struct aiMCPQuery {
    Names bool @0
}

struct aiMCPSurface {
    Tools i64         @0
    Apps  list<bytes> @8
    Names list<text>  @16
}

interface ai {
    # Tools reports what THIS PROCESS's MCP server carries: how many tools its own
    # registry projects, optionally their names, and which subsystems this process
    # composed. It is the answer to "is this MCP server up and does it have anything
    # behind it" — a question a status code cannot answer, since an empty server and
    # a full one are both 200. What the FLEET's server carries is the fleet server's
    # own answer: POST /v1/mcp, tools/list, which asks every subsystem and names the
    # ones that did not reply.
    aiMCPTools(req: aiMCPQuery) returns (rep: aiMCPSurface)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   aiMCPSurface.Apps  ai.aiMCPApp (list element)
