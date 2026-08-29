# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package todo

struct issueClaim {
    Key text @0
    Num i64  @8
}

struct issueEdit {
    Key         text @0
    Num         i64  @8
    Title       text @16
    Description text @24
    Status      text @32
    Priority    text @40
    Assignee    text @48
}

struct issueHit {
    Project  text @0
    Number   i64  @8
    Kind     text @16
    Source   text @24
    Repo     text @32
    Room     text @40
    Title    text @48
    Status   text @56
    Priority text @64
    Assignee text @72
    URL      text @80
}

struct issueHits {
    Issues list<bytes> @0
    Count  i64         @8
}

struct issueQuery {
    Key       text @0
    Status    text @8
    Kind      text @16
    Repo      text @24
    Label     text @32
    Source    text @40
    Scheduled bool @48
}

struct issueRef {
    Key text @0
    Num i64  @8
}

struct issueSearch {
    Q        text @0
    Project  text @8
    Status   text @16
    Kind     text @24
    Repo     text @32
    Room     text @40
    Source   text @48
    Assignee text @56
    Limit    i64  @64
}

struct issueView {
    ID          text       @0
    Identifier  text       @8
    ProjectKey  text       @16
    Number      i64        @24
    Kind        text       @32
    Source      text       @40
    Repo        text       @48
    ExtRef      text       @56
    Title       text       @64
    Description text       @72
    Status      text       @80
    Priority    text       @88
    Assignee    text       @96
    Labels      list<text> @104
    StartAt     i64        @112
    DueAt       i64        @120
    CreatedAt   i64        @128
    UpdatedAt   i64        @136
}

struct newIssue {
    Key         text @0
    Title       text @8
    Description text @16
    Status      text @24
    Priority    text @32
}

struct projectRef {
    Key text @0
}

struct roomRef {
    Room    text @0
    Project text @8
}

struct todoProject {
    ID          text @0
    Org         text @8
    Key         text @16
    Name        text @24
    Description text @32
    CreatedAt   i64  @40
    UpdatedAt   i64  @48
}

interface todo {
    # Returns a board's issues — work items with their column, priority,
    # assignee, labels and schedule.
    # WHICH board is a filter, not an address. Bound to a repository (the key from
    # the path) it is that project's board; left unbound it is the org's whole
    # board; narrowed by label it is a board smaller than any repository — which is
    # the only way an app that lives as a directory inside a shared repository can
    # have one. Every combination is the same rows through the same projection, so
    # no two boards can disagree about what a column means.
    # The column is a LABEL on the forge, so the board and the forge web UI are the
    # same object seen twice: relabelling in either moves the card in both. A closed
    # issue reads as done whatever its labels say.
    get_todo_board(req: issueQuery)
    # Answers across every project in the org.
    # The org comes from the validated principal and never from the request: a
    # caller able to name the org could read another tenant's backlog, and a search
    # is exactly the shape that would quietly return it.
    get_todo_issues(req: issueSearch) returns (rep: issueHits)
    # Returns the boards of your org — the places your work actually
    # is. The key addresses the board's issues.
    # A BOARD IS A PLACE WORK IS, not an object somebody provisioned. So the list is
    # assembled from the work itself: the repositories your org has filed issues on,
    # plus the boards the index holds. A repository with nothing on it is not in the
    # list and is still perfectly addressable — GET /projects/<name> reads it and a
    # create files into it — so nothing is lost by leaving it out.
    # Measured, which is why: reading the forge's whole repository inventory put 745
    # boards here, of which all but a handful were vendored forks and mirrors
    # (.github, .profile, DOMPurify, BoatAttack) that will never carry this org's
    # work. A list that long is not a list — the estate's real roadmap was in it
    # somewhere and no one could see it.
    # The forge half is the FORGE's answer for your own account, so two people in
    # one org can legitimately see different boards.
    get_todo_projects()
    # Returns one board of your org by its key — the repository name.
    # 404 when your org has no repository under that key, or when your own forge
    # account cannot see it.
    get_todo_projects_by_key(req: projectRef) returns (rep: todoProject)
    # Returns a board's issues — work items with their column, priority,
    # assignee, labels and schedule.
    # WHICH board is a filter, not an address. Bound to a repository (the key from
    # the path) it is that project's board; left unbound it is the org's whole
    # board; narrowed by label it is a board smaller than any repository — which is
    # the only way an app that lives as a directory inside a shared repository can
    # have one. Every combination is the same rows through the same projection, so
    # no two boards can disagree about what a column means.
    # The column is a LABEL on the forge, so the board and the forge web UI are the
    # same object seen twice: relabelling in either moves the card in both. A closed
    # issue reads as done whatever its labels say.
    get_todo_projects_by_key_issues(req: issueQuery)
    # Returns ONE work item in full — its description included.
    # The list reads answer a board, and a board is a summary: the description is
    # where the actual content of a work item lives — what an issue asks for, what
    # an epic's acceptance criteria are — and no read on this surface returned it.
    # The address is the one PATCH already accepts, so an item you can move is now
    # an item you can read.
    # It reads the forge directly rather than filtering the org fan-out, then falls
    # back to the index for a board the forge has never heard of — the same order,
    # and the same reason, as GetProject: a row is one kind of thing however it came
    # to exist, so a caller does not have to know which store it is in to fetch it.
    get_todo_projects_by_key_issues_by_num(req: issueRef) returns (rep: issueView)
    # Edits a work item — rename it, rewrite it, move it to another
    # column, or re-prioritise it. Absent fields are left alone.
    # MOVING A CARD IS A RELABEL. The column lives in the forge's label set, so the
    # move replaces that set rather than writing a status column here that a
    # forge-side change could contradict. Moving to `done` also CLOSES the issue on
    # the forge, because a done card and an open issue are a contradiction.
    patch_todo_projects_by_key_issues_by_num(req: issueEdit) returns (rep: issueView)
    # Opens a work item on the board — an issue on that repository on
    # the deployment's forge, filed as YOU.
    # The column and priority are written as LABELS, which is what makes the card
    # and the forge issue the same object: someone relabelling in the forge web UI
    # has moved your card.
    post_todo_projects_by_key_issues(req: newIssue) returns (rep: issueView)
    # Takes an issue: it becomes yours and it moves to in_progress.
    # The holder is the CALLER, never an argument. "Assign this to someone else" is
    # a different act with different authority, and it already exists as a PATCH;
    # conflating them would let anyone hand work to anyone by naming them.
    # Claiming something already held by someone else is refused rather than
    # silently taken — two agents on one issue is the failure this prevents, and a
    # claim that quietly wins a race is worse than one that says no.
    post_todo_projects_by_key_issues_by_num_claim(req: issueClaim) returns (rep: issueHit)
}

# ---------------------------------------------------------------------
# 9 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_todo_rooms_by_room  roomWork.Status  map[string]int  (map)
#
# opaque (1) — crosses, arrives without its name:
#   issueHits.Issues  todo.issueHit (list element)
