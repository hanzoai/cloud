# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package bot

struct BotRoster {
    Bots list<bytes> @0
}

struct BotRuns {
    Bots list<bytes> @0
}

struct BotStopped {
    RunID  text @0
    Status text @8
}

struct BotSync {
    Synced    bool @0
    Projected i64  @8
}

struct stopBotIn {
    RunID text @0
}

interface bot {
    # Returns the caller org's bots as space members — each with the member
    # account uuid and the Person reference the roster addresses it by.
    # A deployment that runs no team subsystem has no spaces and therefore no
    # roster, which is an empty list rather than an error: ErrNoPeer is the ONE
    # error that means "this deployment does not run that app", and every other
    # failure is an outage and says so.
    get_bot_members() returns (rep: BotRoster)
    # List returns the caller org's live bot runs, read from the bot runtime and projected
    # into the console contract with each run's live session URL derived here.
    # The org is ALWAYS the validated principal's org, NEVER a request field, and it is
    # what scopes the runtime's answer — so one tenant can never enumerate another's
    # runs. A runtime that cannot answer is an error, not an empty list: [] would tell
    # the caller "your org has no runs", which is a different claim from "we could not
    # ask", and the difference is the whole reason this endpoint exists.
    get_bot_runs() returns (rep: BotRuns)
    # Re-projects the caller org's bots as members into every space of the org
    # and removes the ones whose agent is gone. Idempotent, and admin only — the
    # admin bit rides the caller to team, which is what decides it.
    post_bot_members_sync() returns (rep: BotSync)
    # Answers 501 to every call: launching a bot run is not implemented.
    # The bot runtime exposes no launch operation, so nothing here can start a sandbox.
    # This address is published rather than dropped because it is the collection every
    # run is created in: GET lists them, POST would launch one.
    # The refusal is total and takes no input. No run id is minted, no session URL is
    # handed back, and no per-run fee is charged. That is the point: the earlier version
    # minted an id the runtime had never heard of, pointed it at a VNC node that did not
    # exist, and took real money for it. 501 is the truth, and the truth is cheaper than
    # a plausible lie.
    # Listing and stopping runs are live and org-scoped. Only the launch is missing, and
    # it returns in the same change that can prove a bot boots — a runtime-side launch
    # operation first (TS, cross-repo), with the entitlement gate and the meter beside
    # it.
    post_bot_runs()
    # Stop terminates one of the caller org's own bot runs and reports its terminal state.
    # The own-key guard is the org: it is the caller's validated org, never theirs to
    # choose, and the runtime resolves the run id UNDER it. A run belonging to another
    # tenant is not among this org's runs, so it answers absent — the same 404 a
    # nonexistent id gets, which is what keeps this from being an oracle.
    # Absence is honoured ONLY when the runtime answers it. A runtime that does not
    # serve stop reports nothing about the run, and reporting "stopped" on that basis
    # would be a stop that cannot fail — so it is a 502.
    post_bot_runs_by_runid_stop(req: stopBotIn) returns (rep: BotStopped)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   BotRoster.Bots  client.BotMember (list element)
#   BotRuns.Bots  bot.BotRun (list element)
