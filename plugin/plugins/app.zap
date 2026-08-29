# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package plugins

struct ActionOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
}

struct ListIn {
    Scope text @0
}

struct ListOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Drift  list<bytes> @24
    Total  i64         @32
}

struct NameIn {
    Name  text @0
    Scope text @8
}

struct ReloadIn {
    Name    text @0
    Version text @8
    URL     text @16
    Sum     text @24
    Scope   text @32
}

interface plugins {
    # Stops the plugin. Its routes STAY REGISTERED and answer 503 — not 404.
    # That is zip's choice and this keeps it. Removing the routes would mutate the
    # route table, and re-adding them on enable would grow it without bound across
    # repeated cycles, which is the invariant that makes reloads flat in memory. It
    # is also the better answer: 404 says "no such API" and a client may cache it
    # and stop retrying, while 503 says "this API exists and is down right now",
    # which is true and retryable. Which of the two 503s this is — deliberate stop
    # or crash — is what the status's disabled flag reports.
    adminDisablePlugin(req: NameIn) returns (rep: ActionOut)
    # Brings a stopped or disabled plugin back on the artifact it already
    # has: the zero Plugin names no new artifact, so Reload reuses the loaded spec
    # and clears the disabled flag. Named for what an operator means by it.
    adminEnablePlugin(req: NameIn) returns (rep: ActionOut)
    # Reports what each host is actually running: every loaded plugin with its
    # version, pid, uptime, reload and restart counts, and its measured CPU, RSS,
    # thread and fd cost — read from the kernel, which is only answerable at all
    # because a plugin is a process.
    # Reading this from deployment config would answer what was INTENDED. Only the
    # process knows what is TRUE, and during a rolling upgrade the two disagree on
    # purpose.
    adminPlugins(req: ListIn) returns (rep: ListOut)
    # Swaps a plugin for another build without dropping a request. The
    # replacement is started and proven to be LISTENING before any traffic moves to
    # it, so a bad build leaves the old one serving and returns an error rather
    # than a hole; the old process then drains before it is killed.
    # With a version or url+sum it pins; naming a digest this host has run before is
    # the rollback, and costs no network because the digest IS the cache key. With
    # neither it restarts what is already loaded.
    # Fleet scope applies it to one host at a time and STOPS at the first failure,
    # so a build that cannot come up reaches exactly one host.
    adminReloadPlugin(req: ReloadIn) returns (rep: ActionOut)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   ActionOut.Data  plugin.Result (list element)
#   ListOut.Data  plugin.Host (list element)
#   ListOut.Drift  plugin.Drift (list element)
