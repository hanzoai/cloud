# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package git

struct DeclareIn {
    Credential   bytes      @0
    Version      text       @8
    Labels       list<text> @16
    Capabilities list<text> @24
}

struct DeclareOut {
    Runner       bytes      @0
    Capabilities list<text> @8
}

struct LogIn {
    Credential bytes       @0
    Task       i64         @8
    Index      i64         @16
    Lines      list<bytes> @24
    Last       bool        @32
}

struct LogOut {
    Ack i64 @0
}

struct RegisterIn {
    Name         text       @0
    Token        text       @8
    Version      text       @16
    Labels       list<text> @24
    Ephemeral    bool       @32
    Capabilities list<text> @40
}

struct RegisterOut {
    Runner bytes @0
}

struct StateIn {
    Credential bytes       @0
    State      bytes       @8
    Outputs    list<bytes> @16
}

struct StateOut {
    State  bytes      @0
    Stored list<text> @8
}

struct TaskIn {
    Credential bytes @0
    Queue      i64   @8
}

struct TaskOut {
    Task  bytes @0
    Queue i64   @8
}

struct blobJSON {
    Path      text @0
    Size      i64  @8
    Encoding  text @16
    Content   text @24
    Binary    bool @32
    Truncated bool @33
}

struct childRef {
    Name text @0
    ID   text @8
}

struct commitsJSON {
    Commits list<bytes> @0
}

struct createReq {
    Name        text @0
    Project     text @8
    Description text @16
    Public      bool @24
}

struct filesJSON {
    Rev   text        @0
    Files list<bytes> @8
}

struct gcOut {
    Repo       text @0
    SizeBytes  i64  @8
    Maintained bool @16
}

struct globRef {
    Name text @0
    Ref  text @8
    Glob text @16
}

struct keyList {
    Data list<bytes> @0
}

struct keyRef {
    ID text @0
}

struct keyView {
    ID          text @0
    Title       text @8
    PublicKey   text @16
    Fingerprint text @24
    CreatedAt   text @32
}

struct logRef {
    Name  text @0
    Ref   text @8
    Path  text @16
    Limit i64  @24
}

struct mirrorList {
    Data list<bytes> @0
}

struct mirrorReq {
    Name    text @0
    Source  text @8
    Project text @16
}

struct mirrorTargetReq {
    Name text @0
    Host text @8
    URL  text @16
}

struct mirrorTargetView {
    ID        text @0
    Repo      text @8
    Host      text @16
    URL       text @24
    CreatedAt text @32
}

struct openReq {
    Name  text @0
    Title text @8
    Body  text @16
    Head  text @24
    Base  text @32
}

struct patchIn {
    Name   text @0
    Public bool @8
}

struct pathRef {
    Name text @0
    Ref  text @8
    Path text @16
}

struct poolDeclare {
    Name   text       @0
    Labels list<text> @8
}

struct poolDeclared {
    Pool   bytes @0
    Secret text  @8
}

struct poolList {
    Data list<bytes> @0
}

struct pullFilter {
    Name  text @0
    State text @8
}

struct pullList {
    Data list<bytes> @0
}

struct pullRef {
    Name   text @0
    Number i64  @8
}

struct pullView {
    Number    i64  @0
    Repo      text @8
    Title     text @16
    Body      text @24
    Head      text @32
    Base      text @40
    State     text @48
    Author    text @56
    MergedRev text @64
    CreatedAt text @72
    UpdatedAt text @80
}

struct pushReq {
    Name    text        @0
    Branch  text        @8
    Message text        @16
    Files   list<bytes> @24
}

struct pushResp {
    Commit   text @0
    Branch   text @8
    CloneURL text @16
    SSHURL   text @24
}

struct readmeJSON {
    Path     text @0
    Content  text @8
    Encoding text @16
}

struct refsJSON {
    Branches list<bytes> @0
    Tags     list<bytes> @8
    Default  text        @16
}

struct registerKeyReq {
    Title     text @0
    PublicKey text @8
}

struct repoList {
    Data list<bytes> @0
}

struct repoRef {
    Name text @0
}

struct repoView {
    ID            text       @0
    Org           text       @8
    Project       text       @16
    Name          text       @24
    Description   text       @32
    DefaultBranch text       @40
    Public        bool       @48
    Branches      list<text> @56
    Head          text       @64
    CloneURL      text       @72
    SSHURL        text       @80
    SizeBytes     i64        @88
    CreatedAt     text       @96
    UpdatedAt     text       @104
}

struct revRef {
    Name text @0
    Ref  text @8
}

struct runQuery {
    Repo  text @0
    Limit i64  @8
}

struct runRef {
    ID text @0
}

struct runStart {
    Repo text @0
    Ref  text @8
}

struct runnerList {
    Data list<bytes> @0
}

struct subscribeReq {
    Name    text       @0
    Channel text       @8
    Events  list<text> @16
}

struct subscriptionList {
    Data list<bytes> @0
}

struct subscriptionView {
    ID        text       @0
    Repo      text       @8
    Channel   text       @16
    Events    list<text> @24
    CreatedAt text       @32
}

struct treeJSON {
    Entries list<bytes> @0
}

struct usageView {
    Org        text        @0
    TotalBytes i64         @8
    Repos      list<bytes> @16
}

struct workflowList {
    Data list<bytes> @0
}

struct workflowQuery {
    Repo text @0
    Ref  text @8
}

struct workflowRun {
    ID        text @0
    Number    i64  @8
    Repo      text @16
    Ref       text @24
    Commit    text @32
    Event     text @40
    Workflow  text @48
    Actor     text @56
    Status    text @64
    CreatedAt i64  @72
    UpdatedAt i64  @80
}

struct workflowRuns {
    Data list<bytes> @0
}

interface git {
    # Removes a registered SSH key, scoped to the caller's org: an org can
    # only delete its own, and a key id it does not own is not found. Answers 204
    # with no body. Once removed the key no longer authenticates any SSH git access.
    delete_git_keys_by_id(req: keyRef)
    # Removes a repo's metadata and purges its storage. Answers 204 with
    # no body. The metadata row is the source of truth for existence, so a storage
    # purge that fails is logged and the delete still succeeds — and a second call
    # is a 404, not a second delete.
    delete_git_repos_by_name(req: repoRef)
    # Removes one Slack subscription from a repo; the notifier stops
    # posting that repo's events to that channel. Answers 204 with no body. An id
    # that is not this repo's subscription is not found.
    delete_git_repos_by_name_subscriptions_by_id(req: childRef)
    # Removes one outbound mirror target; later pushes stop being
    # forwarded to it. Answers 204 with no body. Nothing is done to the downstream
    # remote itself — only this repo's intent to push there is dropped.
    delete_git_repos_by_name_targets_by_id(req: childRef)
    # Returns the SSH public keys registered to the caller's org — the keys
    # that authenticate `git clone git@<host>:<org>/<repo>.git`. Keys are org-scoped
    # on read even though the fingerprint index is global, so one org never sees
    # another's.
    get_git_keys() returns (rep: keyList)
    # Returns the capacity this org has declared and how many daemons have
    # entered each pool.
    get_git_pools() returns (rep: poolList)
    # Returns the repos in the caller's scope, most recently updated
    # first. The scope is the request principal's — the gateway-minted org and its
    # optional project — never anything off the wire, so a caller only ever sees its
    # own. Rows carry no branches or HEAD; read one repo for those.
    get_git_repos() returns (rep: repoList)
    # Returns one repo with its live ref state: every branch name and the
    # resolved HEAD commit. Both are read from the object store on each call, so an
    # empty repo reports no branches and an empty head rather than failing. A repo
    # outside the caller's scope is not found.
    get_git_repos_by_name(req: repoRef) returns (rep: repoView)
    # Returns one file's bytes at one revision. Text comes back verbatim,
    # binary comes back base64, and a file past the 1 MiB view cap comes back marked
    # truncated with NO content — the client is expected to clone instead.
    get_git_repos_by_name_blob(req: pathRef) returns (rep: blobJSON)
    # Walks a ref's history newest first, or one path's history when a
    # path is given. There is no cursor: the page is the newest `limit` commits.
    get_git_repos_by_name_commits(req: logRef) returns (rep: commitsJSON)
    # Returns every file a glob selects at one revision, WITH its bytes
    # and the revision they came from. It is the read a delivery generator makes:
    # one call answers "what is the inventory at this commit, and what does it say",
    # where listing and then fetching would be a request per file.
    # Returning the resolved revision matters as much as the bytes. A generator that
    # lists at `main` and then reads at `main` can straddle a push and assemble half
    # its inventory from one commit and half from the next; resolving once makes the
    # whole read consistent by construction.
    # A file past the read cap comes back Truncated with no content rather than
    # being dropped. A caller building a desired set has to know the difference
    # between "this file is empty" and "this file was not read" — silently omitting
    # it is how a pruning reconcile deletes what the missing file declared.
    get_git_repos_by_name_files(req: globRef) returns (rep: filesJSON)
    # Returns a repo's pull requests, newest number first — what is
    # waiting to be reviewed, and what has already landed. Narrow it with
    # ?state=open or ?state=merged; omit state for every proposal.
    get_git_repos_by_name_pulls(req: pullFilter) returns (rep: pullList)
    # Returns one pull request by its per-repo number. A number belonging to
    # another tenant's repo is not found, exactly as the repo itself is not.
    get_git_repos_by_name_pulls_by_number(req: pullRef) returns (rep: pullView)
    # Returns the README at the tree root as plain text — unrendered, so
    # the caller decides how to present it. A repo with no README is not found.
    get_git_repos_by_name_readme(req: revRef) returns (rep: readmeJSON)
    # Lists a repo's branches, tags and default branch — what a branch
    # picker needs in one call. Unlike the other read ops it tolerates a repo with no
    # commits: the ref sets come back empty and the default branch is still named.
    get_git_repos_by_name_refs(req: repoRef) returns (rep: refsJSON)
    # Returns a repo's Slack subscriptions — which channels the
    # lifecycle notifier posts this repo's push and deploy events to.
    get_git_repos_by_name_subscriptions(req: repoRef) returns (rep: subscriptionList)
    # Returns a repo's outbound mirror targets — the downstream remotes
    # the mirror reactor pushes to whenever a push lands here.
    get_git_repos_by_name_targets(req: repoRef) returns (rep: mirrorList)
    # Lists the immediate children of one directory at one revision,
    # directories before files. It does not recurse — walk down a level at a time.
    get_git_repos_by_name_tree(req: pathRef) returns (rep: treeJSON)
    # Returns the daemons registered into this org's pools, newest
    # first, with when each was last heard from.
    get_git_runners() returns (rep: runnerList)
    # Returns this org's runs, newest first.
    get_git_runs(req: runQuery) returns (rep: workflowRuns)
    # Returns one run.
    get_git_runs_by_id(req: runRef) returns (rep: workflowRun)
    # Returns per-repo and total storage bytes for the caller's org — the
    # queryable, per-tenant number commerce and o11y meter on. It spans EVERY
    # project sub-scope, unlike the repo list, so a billing consumer sees the whole
    # tenant footprint in one call. Sizes are last-measured values (create, push,
    # mirror and gc each re-measure), not a live walk of the disk.
    get_git_usage() returns (rep: usageView)
    # Reports the workflows a repository declares at a ref and which
    # declared pool would execute each job — the answer to "would a push here run,
    # and where".
    get_git_workflows(req: workflowQuery) returns (rep: workflowList)
    # Flips a repo's public bit, the one mutable repo setting today.
    # Public grants ANONYMOUS fetch only; push and the whole control plane stay
    # org-authed. Returns the updated repo.
    patch_git_repos_by_name(req: patchIn) returns (rep: repoView)
    # Registers an SSH public key so it can authenticate `git clone
    # git@<host>:<org>/<repo>.git` for the caller's org. The key line is parsed and
    # canonicalized before storage, its SHA256 fingerprint becomes the auth lookup
    # handle, and the full public key round-trips (it is public). Answers 201.
    # Fingerprints are globally unique, so a key already registered — to this org or
    # any other — is a 409: one key belongs to exactly one org.
    post_git_keys(req: registerKeyReq) returns (rep: keyView)
    # Records the capacity an org has, and answers with the secret a
    # runner daemon presents to enter it.
    # Declaring is the ONLY way capacity comes to exist: a daemon cannot register
    # against a pool nobody declared, because the secret it would have to present
    # does not exist until this runs. Re-declaring an existing pool replaces its
    # labels and mints a fresh secret; runners already inside it keep working.
    post_git_pools(req: poolDeclare) returns (rep: poolDeclared)
    # Provisions an empty bare repository in the caller's scope and
    # returns it with its clone URLs. Answers 201. The name must be unique within
    # the scope — a repeat is a 409, never a silent overwrite of an existing repo.
    # The org comes from the validated principal, so a repo is always born owned by
    # the caller's own tenant.
    post_git_repos(req: createReq) returns (rep: repoView)
    # Repacks a repo into one bitmapped pack and rewrites its commit-graph, so
    # the next clone reuses the bitmap instead of walking the whole object graph.
    # Idempotent, and safe to interrupt — git swaps both artifacts atomically. It
    # runs under one pack slot with the same memory bounds as a clone, so it can
    # block behind heavy pack traffic rather than compete with it. Storage usage is
    # re-measured afterwards, since a repack reclaims space.
    post_git_repos_by_name_gc(req: repoRef) returns (rep: gcOut)
    # Imports an external git repository into the caller's repo, provisioning
    # it on first use. Fetch is FORCED and covers every ref, so a first call clones
    # the source and a repeat call re-syncs it — the endpoint is idempotent by mirror
    # semantics. Mirrored bytes are metered exactly like a push, and a push.landed
    # event is emitted for the default branch so the code index picks the repo up.
    post_git_repos_by_name_mirror(req: mirrorReq) returns (rep: repoView)
    # Proposes a branch for merging and returns it with its number. Answers
    # 201. Both branches must already exist — a proposal naming a branch nobody
    # pushed is a typo, not a plan — and base defaults to the repo's default branch.
    # Proposing the same head into the same base twice is a 409 while the first
    # proposal is still open, so a retried agent run leaves ONE thing to review
    # rather than a pile of identical ones. A repo outside the caller's scope is a
    # 404, exactly as reading it is.
    post_git_repos_by_name_pulls(req: openReq) returns (rep: pullView)
    # Merges an open pull request by FAST-FORWARDING base to head, and
    # answers the proposal in its merged state with the revision base now points at.
    # It merges only when base is already an ancestor of head — the case where head
    # contains every commit base has, so moving the branch loses nothing and invents
    # nothing. When base has moved on independently, this REFUSES with 409 and says
    # so: a real three-way merge is not implemented here, and reporting one would
    # claim a result these bytes do not produce. Rebase head onto base and merge
    # again.
    # The move is judged by the same ref policy a `git push` of it would face, and
    # fires the same build and notify reactions, so merging is not a way around
    # either. Merging an already-merged proposal is a 409.
    post_git_repos_by_name_pulls_by_number_merge(req: pullRef) returns (rep: pullView)
    # Lands a set of files as one commit without a git client — the
    # hanzo.app builder's push. The repo is CREATED on first push, the files are
    # merged onto the branch tip (unlisted files survive), and the same
    # push-to-deploy hook a real receive-pack fires is fired, so downstream this is
    # indistinguishable from a `git push`.
    post_git_repos_by_name_push(req: pushReq) returns (rep: pushResp)
    # Binds a Slack channel to a repo, so the lifecycle notifier posts
    # that repo's push and deploy events there. Answers 201. The same channel twice
    # on one repo is a 409; a repo outside the caller's scope is a 404, exactly as
    # reading it is.
    post_git_repos_by_name_subscriptions(req: subscribeReq) returns (rep: subscriptionView)
    # Registers a downstream remote the repo's advanced refs are pushed to
    # whenever a push lands here. Answers 201. The URL must be https to a host on the
    # mirror allowlist (github.com / gitlab.com): the same set the mirror credential
    # may be sent to, so a target can never capture the shared token or point the push
    # at an internal service. Any embedded userinfo is stripped — credentials ride
    # env-only at push time and never enter the stored URL. One mirror per host per
    # repo; a second is a 409.
    post_git_repos_by_name_targets(req: mirrorTargetReq) returns (rep: mirrorTargetView)
    # Runs a repository's workflows at a ref, on demand.
    # It takes the SAME path a push takes: the request is recorded in the journal
    # and delivered from there, so an explicit run and a pushed one are one
    # mechanism with one idempotency rule and not two that can disagree. Asking
    # twice for the same commit yields the same run.
    post_git_runs(req: runStart) returns (rep: workflowRuns)
    # Republishes what a registered runner can do, and answers with what
    # this side understands, so the two learn about each other from one exchange.
    post_runner_declare(req: DeclareIn) returns (rep: DeclareOut)
    # Adds console output to a task's log and answers with how far that log is
    # durable, so the runner knows where to resend from.
    post_runner_log(req: LogIn) returns (rep: LogOut)
    # Trades a pool's join secret for a runner identity and the token that
    # authenticates every later call. It is the one operation with no credential to
    # check, because a runner has none until this answers.
    # The secret names the pool it opens, and a pool exists only because somebody
    # declared it. A daemon that starts against capacity nobody declared is refused
    # here, which is where the rule that pools are declared state actually holds.
    post_runner_register(req: RegisterIn) returns (rep: RegisterOut)
    # Records a task's progress and that of its steps, and answers with the
    # result this side now holds — which is how a runner learns its task was stopped
    # from somewhere else.
    post_runner_state(req: StateIn) returns (rep: StateOut)
    # Hands the runner a job to execute, if its pool has one, and answers
    # immediately either way. A runner sends the queue version it last saw; when it
    # matches, nothing has been queued since and no lease transaction is opened.
    post_runner_task(req: TaskIn) returns (rep: TaskOut)
}

# ---------------------------------------------------------------------
# 40 op(s) here. What follows is what this schema does not carry.
#
# opaque (28) — crosses, arrives without its name:
#   DeclareIn.Credential  runner.Credential
#   DeclareOut.Runner  runner.Identity
#   LogIn.Credential  runner.Credential
#   LogIn.Lines  runner.Line (list element)
#   RegisterOut.Runner  runner.Identity
#   StateIn.Credential  runner.Credential
#   StateIn.Outputs  runner.Pair (list element)
#   StateIn.State  runner.State
#   StateOut.State  runner.State
#   TaskIn.Credential  runner.Credential
#   TaskOut.Task  runner.Task
#   commitsJSON.Commits  git.commitJSON (list element)
#   filesJSON.Files  git.fileJSON (list element)
#   keyList.Data  git.keyView (list element)
#   mirrorList.Data  git.mirrorTargetView (list element)
#   poolDeclared.Pool  git.poolView
#   poolList.Data  git.poolView (list element)
#   pullList.Data  git.pullView (list element)
#   pushReq.Files  git.pushFile (list element)
#   refsJSON.Branches  git.refJSON (list element)
#   refsJSON.Tags  git.refJSON (list element)
#   repoList.Data  git.repoView (list element)
#   runnerList.Data  git.runnerView (list element)
#   subscriptionList.Data  git.subscriptionView (list element)
#   treeJSON.Entries  git.treeEntryJSON (list element)
#   usageView.Repos  git.usageRepo (list element)
#   workflowList.Data  git.workflowView (list element)
#   workflowRuns.Data  git.workflowRun (list element)
