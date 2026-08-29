# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package deploy

struct GitOpsPlane {
    Installed    bool        @0
    Reason       text        @8
    Applications list<bytes> @16
}

struct appRef {
    Name text @0
}

struct argoClusterList {
    Metadata bytes       @0
    Items    list<bytes> @8
}

struct argoRevisionMetadata {
    Author        text       @0
    Date          text       @8
    Tags          list<text> @16
    Message       text       @24
    SignatureInfo text       @32
}

struct deployHealth {
    Service text @0
    Status  text @8
    K8s     bool @16
    CRD     bool @17
}

struct reconcileReport {
    Declared i64         @0
    Failed   i64         @8
    Instance text        @16
    Prune    bool        @24
    Pruned   i64         @32
    Results  list<bytes> @40
    Revision text        @48
    Source   bytes       @56
    Synced   i64         @64
}

struct revisionRef {
    Name     text @0
    Revision text @8
}

struct sessionEnded {
    LoggedIn bool @0
    LoginURL text @8
}

struct sessionUser {
    Groups    list<text> @0
    Iss       text       @8
    LoggedIn  bool       @16
    LoginURL  text       @24
    LogoutURL text       @32
    Username  text       @40
}

struct versionMessage {
    BuildDate text @0
    Compiler  text @8
    GoVersion text @16
    Platform  text @24
    Version   text @32
}

interface deploy {
    # Returns the argocd RevisionMetadata for one revision
    # of one application — what the detail view shows beside a revision.
    # An App CR is IMAGE-pinned rather than commit-pinned: the deploy names an image
    # tag, and the git source this projection reports is the display-only manifest
    # repo, not the application's own source. Nothing in this process can read a
    # commit's author or message for an arbitrary revision. So rather than 404 (which
    # the SPA turns into an error toast) or invent a git author, it answers the
    # HONEST minimum: date is when the App CR was created, message is the revision
    # asked for — with the empty revision and "HEAD" resolving to the image tag the
    # CR declares — and author is empty. An over-long revision is truncated before it
    # is echoed back.
    # Tenant-scoped exactly like the application read.
    get_deploy_applications_by_name_revisions_by_revision_metadata(req: revisionRef) returns (rep: argoRevisionMetadata)
    # Returns the argocd ClusterList of the destinations the
    # caller's applications reconcile into: one entry per distinct destination
    # server, carrying the count of applications reconciling into it. The in-cluster
    # destination is always present, so an empty fleet still answers one cluster, and
    # no cluster credential can appear — the projected type physically has no config
    # field.
    # It is TENANT-SCOPED and reads the SAME App CRs the applications list reads: a
    # platform SuperAdmin counts the whole fleet, a validated org member counts only
    # its own org's applications, anyone else is refused.
    get_deploy_clusters() returns (rep: argoClusterList)
    # Lists every Hanzo CD Application in the cluster: the git source
    # each one polls, the commit it last APPLIED, how its last sync operation ended,
    # and its recent deploy history — newest deploy first, ordered by namespace then
    # name.
    # This is the layer ABOVE the application board, and the two disagree in exactly
    # the case an operator most needs to see: main carries a new image pin, CD has
    # not applied that commit yet, so every App CR still declares the old tag and the
    # application board is legitimately "Synced" while the deploy has not landed.
    # Only the applied revision here can show that.
    # installed is false — with a reason and an empty list — when the CD CRD is not
    # served in this cluster. That is a FACT about the cluster rather than a failure
    # of the request, so the caller can say "no CD plane here" instead of rendering
    # an error it cannot act on; a genuine transport or RBAC failure still errors.
    # Read-only, and platform SuperAdmin only: the CD plane is fleet infrastructure
    # with no tenant dimension. This view observes CD and never drives it — the sync
    # policy is automated with self-heal, and the actionable verb an operator has is
    # the per-application reconcile at POST /v1/deploy/applications/{name}/sync.
    get_deploy_gitops() returns (rep: GitOpsPlane)
    # Health reports whether this deployment can observe the delivery plane.
    # 200 only when the Kubernetes API answers AND the App custom resource is served;
    # 503 with the same shape otherwise, naming which half failed. It reports BOOLEANS
    # and never the underlying error, because the route is unauthenticated — liveness
    # must be probe-able without a token — and a raw client error can disclose the
    # apiserver address or an RBAC detail. That detail is logged server-side instead.
    get_deploy_health() returns (rep: deployHealth)
    # Answers "is this browser signed in, and if not where does it
    # sign in?" — the dashboard SPA's bootstrap question, and the only route on this
    # plane that answers for an anonymous caller.
    # The anonymous answer carries loggedIn:false and a URL and NOTHING else: no
    # username, no org, no groups, no issuer, no hint about who the caller might be or
    # what exists in the cluster. Answering it costs nothing (the caller already knows
    # whether it holds a cookie) and withholding it costs the whole sign-in journey.
    # The predicate is the platform SuperAdmin fact — the SAME one every other route
    # here gates on, minted from a validated principal whose org is the reserved admin
    # org — so a validated-but-not-SuperAdmin caller is reported as NOT signed in,
    # which is the truth as this console defines it: they cannot use it.
    get_deploy_session_userinfo() returns (rep: sessionUser)
    # Returns the argocd VersionMessage the dashboard SPA reads at
    # bootstrap. There is no argocd binary behind this plane — it is a projection
    # over operator App CRs — so the fields say so rather than describing a build:
    # Version names the projection, BuildDate is the moment this response was
    # generated, and Compiler/Platform/GoVersion are the constants the SPA tolerates
    # rather than facts about this process. Platform SuperAdmin only.
    get_deploy_version() returns (rep: versionMessage)
    # Ends the console session on this host.
    # It clears this console's session cookie and answers the signed-out state with
    # the sign-in URL to start again. IAM's own session is untouched — this ends the
    # console session only, so signing back in may not prompt for credentials.
    # It is a POST because it CHANGES STATE. As a GET it was reachable by a
    # cross-site top-level navigation, which a SameSite=Lax cookie still rides, so
    # any page could sign a SuperAdmin out; a POST is not carried cross-site by that
    # cookie. It reads no request body and takes no argument: the session it ends is
    # the one the request already carries.
    post_deploy_logout() returns (rep: sessionEnded)
    # Renders the configured git source and applies it to the
    # cluster, once.
    # It runs one full GitOps sync through the embedded engine — render the
    # configured repo, ref and path, then three-way server-side apply with scoped
    # prune — and answers the revision it applied, the source it came from, the
    # declared/synced/pruned/failed counts and a per-resource result. This is the
    # WRITE half of the plane: it mutates live cluster objects and, with prune
    # enabled, deletes objects the source no longer declares.
    # SuperAdmin-only and fail-closed, with the gate INSIDE the op because a typed op
    # is also reached by POST /mcp and by the by-name call plane, where no route
    # middleware runs. The git source is read AS THE PLATFORM, not as the caller: the
    # coordinate is this deployment's own configuration and never a parameter, which
    # is why the op reads no request body at all. A deployment with the engine
    # switched off, or with no usable cluster config, answers 503; a failure to
    # start, render or sync is a 502.
    post_deploy_reconcile() returns (rep: reconcileReport)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# blocked (9) — the op is absent; the field has no wire form:
#   get_deploy_applications  argoAppList.Items  []deploy.argoApp  (no wire form)
#   get_deploy_applications_by_name  argoApp.Metadata  deploy.argoMeta  (reaches one)
#   get_deploy_applications_by_name_resource-tree  argoTree.Hosts  []interface {}  (no wire form)
#   get_deploy_applications_by_name_syncwindows  argoSyncWindows.ActiveWindows  []interface {}  (no wire form)
#   get_deploy_applications_by_name_syncwindows  argoSyncWindows.AssignedWindows  []interface {}  (no wire form)
#   get_deploy_projects  argoProjectList.Items  []deploy.argoProject  (no wire form)
#   get_deploy_settings  consoleSettings.Help  struct { BinaryUrls map[string]string "json:\"binaryUrls\""; ChatText string "json:\"chatText\""; ChatUrl string "json:\"chatUrl\"" }  (reaches one)
#   post_deploy_applications_by_name_rollback  argoApp  deploy.argoApp  (reaches one)
#   post_deploy_applications_by_name_sync  argoApp  deploy.argoApp  (reaches one)
#
# opaque (5) — crosses, arrives without its name:
#   GitOpsPlane.Applications  deploy.GitOpsApp (list element)
#   argoClusterList.Items  deploy.argoCluster (list element)
#   argoClusterList.Metadata  deploy.argoListMeta
#   reconcileReport.Results  deploy.appliedResource (list element)
#   reconcileReport.Source  deploy.reconcileSource
