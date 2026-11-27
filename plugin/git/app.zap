# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package git

struct activityIn {
    Org   text @0
    Group text @8
    Page  i64  @16
    Limit i64  @24
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

struct combinedStatus {
    SHA      text        @0
    State    text        @8
    Total    i64         @16
    Statuses list<bytes> @24
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

struct forgeQuery {
    Owner text @0
}

struct gcOut {
    Repo       text @0
    SizeBytes  i64  @8
    Maintained bool @16
}

struct getMetricsIn {
    Ecosystem   text @0
    Org         text @8
    Name        text @16
    Metric      text @24
    From        text @32
    To          text @40
    Granularity text @48
    By          text @56
}

struct getMetricsOut {
    From        text        @0
    To          text        @8
    Granularity text        @16
    By          text        @24
    Series      list<bytes> @32
}

struct globRef {
    Name text @0
    Ref  text @8
    Glob text @16
}

struct homeReq {
    Name text @0
}

struct keyIn {
    Title    text @0
    Key      text @8
    Repo     text @16
    ReadOnly bool @24
}

struct keyList {
    Data list<bytes> @0
}

struct keyListQuery {
    Repo text @0
}

struct keyRef {
    ID text @0
}

struct keyView {
    ID          text @0
    Title       text @8
    Kind        text @16
    Repo        text @24
    ReadOnly    bool @32
    Fingerprint text @40
    Creator     text @48
    CreatedAt   text @56
}

struct linkAll {
    Owner text @0
}

struct linkReq {
    Name    text @0
    Source  text @8
    Project text @16
}

struct linked {
    Linked    list<bytes> @0
    Unmatched list<bytes> @8
    Held      list<text>  @16
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
    Name   text       @0
    Public list<bool> @8
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

struct putMetricsIn {
    Rows     list<bytes> @0
    Packages list<bytes> @8
}

struct putMetricsOut {
    Written  i64 @0
    Packages i64 @8
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

struct repoList {
    Data list<bytes> @0
}

struct repoRef {
    Name text @0
}

struct repoView {
    ID            text        @0
    Org           text        @8
    Project       text        @16
    Name          text        @24
    Description   text        @32
    DefaultBranch text        @40
    Public        bool        @48
    Branches      list<text>  @56
    Head          text        @64
    CloneURL      text        @72
    SSHURL        text        @80
    SizeBytes     i64         @88
    Link          bytes       @96
    CreatedAt     text        @104
    UpdatedAt     text        @112
    Upstream      text        @120
    Packages      list<bytes> @128
}

struct revRef {
    Name text @0
    Ref  text @8
}

struct runLogRef {
    ID   text @0
    Repo text @8
    Job  text @16
}

struct runLogs {
    Run  bytes       @0
    Jobs list<bytes> @8
    More i64         @16
}

struct runQuery {
    Repo  text @0
    Ref   text @8
    Limit i64  @16
}

struct runRef {
    ID   text @0
    Repo text @8
}

struct runStart {
    Repo text @0
    Ref  text @8
}

struct runnerList {
    Data  list<bytes> @0
    Pools list<bytes> @8
    Fault text        @16
}

struct statusIn {
    Name        text @0
    SHA         text @8
    State       text @16
    Context     text @24
    Description text @32
    TargetURL   text @40
}

struct statusRef {
    Name text @0
    SHA  text @8
}

struct statusView {
    ID          text @0
    Context     text @8
    State       text @16
    Description text @24
    TargetURL   text @32
    Creator     text @40
    CreatedAt   text @48
    UpdatedAt   text @56
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
    Data   list<bytes> @0
    Commit text        @8
}

struct workflowQuery {
    Repo text @0
    Ref  text @8
}

struct workflowRun {
    ID        text        @0
    Number    i64         @8
    Repo      text        @16
    Ref       text        @24
    Commit    text        @32
    Event     text        @40
    Workflow  text        @48
    Actor     text        @56
    Status    text        @64
    Title     text        @72
    Attempt   i64         @80
    URL       text        @88
    CreatedAt i64         @96
    UpdatedAt i64         @104
    StartedAt bytes       @112
    EndedAt   bytes       @120
    Jobs      list<bytes> @128
}

struct workflowRuns {
    Data   list<bytes> @0
    Commit text        @8
    State  text        @16
}

struct workspaceView {
    Owner text @0
    Made  bool @8
    Login text @16
}

interface git {
    # Removes a key; a push or fetch with it fails from the next connection.
    # Answers 204. The person who registered it or an org admin may remove it; any
    # other caller, and an id that is not this org's, is not found.
    delete_git_keys_by_id(req: keyRef)
    # Removes a repo's metadata and purges its storage. Answers 204 with
    # no body. The metadata row is the source of truth for existence, so a storage
    # purge that fails is logged and the delete still succeeds — and a second call
    # is a 404, not a second delete. A repository this app does not hold is looked
    # for where getRepo looks next, the org's made workspace on the forge, so a
    # customer's own repository (or the copy a GitHub link made there) is one a
    # delete can reach, as their list already shows it.
    delete_git_repos_by_name(req: repoRef)
    # Removes one Slack subscription from a repo; the notifier stops
    # posting that repo's events to that channel. Answers 204 with no body. An id
    # that is not this repo's subscription is not found.
    delete_git_repos_by_name_subscriptions_by_id(req: childRef)
    # Removes one outbound mirror target; later pushes stop being
    # forwarded to it. Answers 204 with no body. Nothing is done to the downstream
    # remote itself — only this repo's intent to push there is dropped.
    delete_git_repos_by_name_targets_by_id(req: childRef)
    # Answers the estate's open source figures over a window as series,
    # one per group and metric: by entity, org or ecosystem, per day or per month.
    # A running total (stars) takes each entity's last day in a month, a daily count
    # (downloads) sums its days, and a group sums its entities. A window or a match
    # too wide to answer whole is refused, never cut. SuperAdmin only.
    get_admin_git_metrics(req: getMetricsIn) returns (rep: getMetricsOut)
    # Returns the repositories on the forge (git.hanzo.ai) the caller can
    # read, in every forge namespace their org owns or in the one named, most
    # recently updated first.
    # Each is named owner/name, the coordinate `repos get`, `runs`, `workflows` and
    # the browse reads take. The read is the caller's own forge login, so a private
    # repository they are not given on the forge is not listed. The repositories
    # this app holds are `repos list`.
    get_git(req: forgeQuery) returns (rep: repoList)
    # Reads the keys the caller may see: their own, or every key of the org
    # for an org admin. `repo` narrows to one repository's deploy keys.
    get_git_keys(req: keyListQuery) returns (rep: keyList)
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
    # A name with an owner, hanzo-inc/cloud, is the repository on the forge
    # (git.hanzo.ai), its one home, read there as the caller in a forge namespace
    # their org owns; the tree and blob reads take the same coordinate.
    get_git_repos_by_name(req: repoRef) returns (rep: repoView)
    # Returns one file's bytes at one revision. Text comes back verbatim,
    # binary comes back base64, and a file past the 1 MiB view cap comes back marked
    # truncated with NO content — the client is expected to clone instead. A
    # repository in the organization's own code workspace on the forge is read from
    # there.
    get_git_repos_by_name_blob(req: pathRef) returns (rep: blobJSON)
    # Walks a ref's history newest first, or one path's history when a
    # path is given. There is no cursor: the page is the newest `limit` commits.
    get_git_repos_by_name_commits(req: logRef) returns (rep: commitsJSON)
    # Reads what a commit's contexts say, and the one word they say
    # together.
    get_git_repos_by_name_commits_by_sha_statuses(req: statusRef) returns (rep: combinedStatus)
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
    # A repository in the organization's own code workspace on the forge is listed
    # from there.
    get_git_repos_by_name_tree(req: pathRef) returns (rep: treeJSON)
    # Returns the runners that can run the caller's work, each with
    # when it was last heard from, and the health of every pool.
    # First the forge's: the runners registered on git.hanzo.ai, which run every
    # repository whose home is there — the platform starts one for each job the
    # forge queues, labelled as the job asked. Then the daemons registered into this
    # org's own declared pools. Pools groups the forge's by label set, with the
    # caller's jobs queued on each, so a label set with work waiting and no runner
    # online — a dead pool — is visible.
    # The forge's runners serve every tenant and are named for the repository whose
    # job they took, so they are listed by name to a SuperAdmin; anyone else sees
    # the pools their own queued jobs wait on — the jobs of repositories the forge
    # lets them read — with how many runners carry each and when one was last heard.
    get_git_runners() returns (rep: runnerList)
    # Returns this org's runs, newest first.
    # A repository named owner/name is on the forge, and its runs are the forge's:
    # the newest, or, when a ref is named, every run on the commit it resolves to,
    # each with its jobs and steps and the ref's verdict in State.
    get_git_runs(req: runQuery) returns (rep: workflowRuns)
    # Returns one run with its jobs and their steps.
    # A run of a forge repository is addressed by its id on the forge and the
    # repository, owner/name; one this app scheduled by its id alone.
    get_git_runs_by_id(req: runRef) returns (rep: workflowRun)
    # Returns what a run's jobs wrote: one job's log, named by its id
    # or its name, or by default the log of every job that failed, with the lines
    # in each that say what failed picked out.
    # A run of a forge repository is addressed by its id on the forge and the
    # repository, owner/name; one this app scheduled by its id alone.
    get_git_runs_by_id_logs(req: runLogRef) returns (rep: runLogs)
    # Returns per-repo and total storage bytes for the caller's org — the
    # queryable, per-tenant number commerce and o11y meter on. It spans EVERY
    # project sub-scope, unlike the repo list, so a billing consumer sees the whole
    # tenant footprint in one call. Sizes are last-measured values (create, push,
    # mirror and gc each re-measure), not a live walk of the disk.
    get_git_usage() returns (rep: usageView)
    # Reports the workflows a repository declares at a ref and which
    # declared pool would execute each job — the answer to "would a push here run,
    # and where".
    # A repository named owner/name is read from the forge, at the commit the ref
    # resolves to, in the same two directories the forge reads (.hanzo/workflows,
    # then .github/workflows).
    get_git_workflows(req: workflowQuery) returns (rep: workflowList)
    # Flips a repo's public bit, the one mutable repo setting today.
    # Public grants ANONYMOUS fetch only; push and the whole control plane stay
    # org-authed. Returns the updated repo.
    patch_git_repos_by_name(req: patchIn) returns (rep: repoView)
    # Writes one batch of the estate's open source figures and package
    # registrations, whole or not at all. A figure already held for the same date,
    # ecosystem, name and metric is replaced, so re-sending a window is safe.
    # SuperAdmin only.
    post_admin_git_metrics(req: putMetricsIn) returns (rep: putMetricsOut)
    # Registers an SSH public key: the caller's own, or — with `repo` — a deploy
    # key for one repository. Answers 201. A key already registered anywhere is a 409,
    # and a repository outside the caller's scope is not found. Deploy keys are an
    # admin's to register; a user key authenticates its owner into this org only.
    post_git_keys(req: keyIn) returns (rep: keyView)
    # Links every repository of the caller's org that holds no history
    # here and names no link to github.com/<owner>/<name>, where GitHub lets the
    # caller read it. A repository with no such namesake is listed and left alone,
    # and one holding history here is listed and never touched. Nothing is fetched:
    # a linked repository's cache fills on its first read. Org admins only.
    post_git_link(req: linkAll) returns (rep: linked)
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
    # States a commit's status. Answers 201. Stating a context that already
    # spoke replaces its word. A repository outside the caller's scope is not found.
    post_git_repos_by_name_commits_by_sha_statuses(req: statusIn) returns (rep: statusView)
    # Repacks a repo into one bitmapped pack and rewrites its commit-graph, so
    # the next clone reuses the bitmap instead of walking the whole object graph.
    # Idempotent, and safe to interrupt — git swaps both artifacts atomically. It
    # runs under one pack slot with the same memory bounds as a clone, so it can
    # block behind heavy pack traffic rather than compete with it. Storage usage is
    # re-measured afterwards, since a repack reclaims space.
    post_git_repos_by_name_gc(req: repoRef) returns (rep: gcOut)
    # Moves a repository whose history is HERE to the forge (git.hanzo.ai),
    # the one home of a repository no other host holds, and links it there: the
    # repository is made in the org's own namespace on the forge, every branch and
    # tag is pushed to it, the forge's refs are read back and must equal these, and
    # only then does the row name the forge as its link, so this copy becomes the
    # cache it can be fetched back into. A forge repository of that name that
    # already exists is refused, never overwritten. Org admins only.
    post_git_repos_by_name_home(req: homeReq) returns (rep: repoView)
    # Records that a repository's history lives on another host, making
    # the repository first when the org has none of that name. Nothing is copied:
    # a read is served from a cache filled from the host, as the host lets the
    # caller read, and a push goes to the host. A repository whose history is HERE
    # is refused (409): a link would hide what only this store holds.
    post_git_repos_by_name_link(req: linkReq) returns (rep: repoView)
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
    # It is for generated content. Changing CI through it needs an org admin (403
    # otherwise), and a workflow whose newest change came in through it is not run
    # automatically; CI written through git runs as usual.
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
    # Sets the org's code workspace up for the caller and answers it:
    # the namespace, made when the org has none, and the caller's forge account and
    # seat there. Idempotent. The estate's namespace is answered from the closed
    # table and nobody is enrolled in it. A seat is a person's: an API key or an
    # application is refused, and a SuperAdmin acting in another org reads the
    # workspace as it is and makes nothing.
    post_git_workspace() returns (rep: workspaceView)
}

# ---------------------------------------------------------------------
# 44 op(s) here. What follows is what this schema does not carry.
#
# dropped (2) — the value does not cross, and nothing fails:
#   workflowRun.EndedAt  time.Time  (empty message)
#   workflowRun.StartedAt  time.Time  (empty message)
#
# blocked (4) — the op is absent; the field has no wire form:
#   get_git_activity  activityOut.Active  map[string]int  (map)
#   get_git_activity  activityOut.Daily  map[string]int  (map)
#   get_git_activity  activityOut.Groups  []git.activityGroup  (no wire form)
#   get_git_activity  activityOut.OursDaily  map[string]int  (map)
#
# opaque (31) — crosses, arrives without its name:
#   combinedStatus.Statuses  git.statusView (list element)
#   commitsJSON.Commits  git.commitJSON (list element)
#   filesJSON.Files  git.fileJSON (list element)
#   getMetricsOut.Series  git.metricSeries (list element)
#   keyList.Data  git.keyView (list element)
#   linked.Linked  git.repoView (list element)
#   linked.Unmatched  git.unmatched (list element)
#   mirrorList.Data  git.mirrorTargetView (list element)
#   poolDeclared.Pool  git.poolView
#   poolList.Data  git.poolView (list element)
#   pullList.Data  git.pullView (list element)
#   pushReq.Files  git.pushFile (list element)
#   putMetricsIn.Packages  git.packageIn (list element)
#   putMetricsIn.Rows  git.metricRowIn (list element)
#   refsJSON.Branches  git.refJSON (list element)
#   refsJSON.Tags  git.refJSON (list element)
#   repoList.Data  git.repoView (list element)
#   repoView.Link  git.link
#   repoView.Packages  git.packageRefView (list element)
#   runLogs.Jobs  git.jobLog (list element)
#   runLogs.Run  git.workflowRun
#   runnerList.Data  git.runnerView (list element)
#   runnerList.Pools  git.poolHealth (list element)
#   subscriptionList.Data  git.subscriptionView (list element)
#   treeJSON.Entries  git.treeEntryJSON (list element)
#   usageView.Repos  git.usageRepo (list element)
#   workflowList.Data  git.workflowView (list element)
#   workflowRun.EndedAt  time.Time
#   workflowRun.Jobs  git.runJob (list element)
#   workflowRun.StartedAt  time.Time
#   workflowRuns.Data  git.workflowRun (list element)
