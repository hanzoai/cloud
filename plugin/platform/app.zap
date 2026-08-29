# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package platform

struct AppView {
    ID          text       @0
    Org         text       @8
    App         text       @16
    Env         text       @24
    Repo        text       @32
    Registry    text       @40
    Role        text       @48
    DeclaredTag text       @56
    RunningTag  text       @64
    LatestTag   text       @72
    Health      text       @80
    Phase       text       @88
    Cluster     text       @96
    Namespace   text       @104
    Endpoints   list<text> @112
    Drift       bytes      @120
}

struct CDApp {
    Name             text @0
    Namespace        text @8
    Project          text @16
    Path             text @24
    Sync             text @32
    Revision         text @40
    Health           text @48
    Message          text @56
    ReconciledAt     text @64
    Automated        bool @72
    SelfHeal         bool @73
    Phase            text @80
    OperationMessage text @88
}

struct Declaration {
    Name        text        @0
    Org         text        @8
    Application text        @16
    Project     text        @24
    Path        text        @32
    Repository  text        @40
    Tag         text        @48
    Digest      text        @56
    Hosts       list<text>  @64
    Replicas    i64         @72
    Env         list<bytes> @80
    Automated   bool        @88
}

struct addDomainReq {
    Project text @0
    App     text @8
    Host    text @16
}

struct appRef {
    Project text @0
    App     text @8
}

struct appView {
    ID                  text        @0
    Org                 text        @8
    ProjectID           text        @16
    Slug                text        @24
    Name                text        @32
    Description         text        @40
    Environment         text        @48
    Source              text        @56
    Repo                bytes       @64
    Image               bytes       @72
    BuildType           text        @80
    Dockerfile          text        @88
    Env                 list<bytes> @96
    Port                i64         @104
    Replicas            i64         @112
    StorageGB           i64         @120
    Domains             list<text>  @128
    Status              text        @136
    Namespace           text        @144
    CurrentDeploymentID text        @152
    Phase               text        @160
    Health              text        @168
    SecretSync          text        @176
    SecretSyncDetail    text        @184
    CreatedAt           i64         @192
    UpdatedAt           i64         @200
}

struct buildBoard {
    Builds list<bytes> @0
}

struct cdResp {
    Applications list<bytes> @0
}

struct createAppReq {
    Project     text        @0
    Name        text        @8
    Slug        text        @16
    Description text        @24
    Environment text        @32
    Source      text        @40
    Repo        bytes       @48
    Image       bytes       @56
    BuildType   text        @64
    Dockerfile  text        @72
    Port        i64         @80
    Replicas    i64         @88
    StorageGB   i64         @96
    Env         list<bytes> @104
    Domains     list<text>  @112
}

struct declarationRef {
    App text @0
    Org text @8
}

struct declarationsQuery {
    Org text @0
}

struct declaredResp {
    Org  text        @0
    Apps list<bytes> @8
    CD   bytes       @16
}

struct deployLogs {
    DeploymentID text @0
    Source       text @8
    Logs         text @16
}

struct deployReq {
    Project text @0
    App     text @8
    Commit  text @16
    Tag     text @24
}

struct deploymentRef {
    Project text @0
    App     text @8
    ID      text @16
}

struct deploymentView {
    ID            text @0
    Org           text @8
    ApplicationID text @16
    Version       i64  @24
    Status        text @32
    Source        text @40
    Commit        text @48
    Image         text @56
    BuildID       text @64
    Message       text @72
    CreatedAt     i64  @80
    UpdatedAt     i64  @88
}

struct domainRef {
    Project text @0
    App     text @8
    Host    text @16
}

struct domainView {
    Host      text        @0
    Kind      text        @8
    Status    text        @16
    URL       text        @24
    Verified  bool        @32
    Primary   bool        @33
    Records   list<bytes> @40
    Detail    text        @48
    CreatedAt i64         @56
}

struct driftBoard {
    Apps    list<bytes> @0
    Summary bytes       @8
}

struct environmentBoard {
    Environments list<bytes> @0
}

struct fleetQuery {
    Env    text @0
    Health text @8
    Org    text @16
    Drift  text @24
}

struct fleetRef {
    App text @0
    Env text @8
}

struct pipelineBoard {
    Pipelines list<bytes> @0
}

struct previewReq {
    Project text @0
    App     text @8
    Branch  text @16
    Image   text @24
}

struct previewView {
    URL        text  @0
    Branch     text  @8
    App        text  @16
    Deployment bytes @24
}

struct projectRef {
    Project text @0
}

struct projectView {
    Org          text @0
    Slug         text @8
    Name         text @16
    Description  text @24
    Applications i64  @32
    CreatedAt    i64  @40
}

struct promoteReq {
    Project      text @0
    App          text @8
    DeploymentID text @16
    Tag          text @24
}

struct readiness {
    Service text @0
    Status  text @8
    K8s     bool @16
    CRD     bool @17
    Error   text @24
}

struct releaseBoard {
    Releases list<bytes> @0
}

struct restartRef {
    App text @0
    Env text @8
}

struct restarted {
    OK          bool @0
    App         text @8
    Namespace   text @16
    Env         text @24
    RestartedAt text @32
}

struct rollbackReq {
    Project      text @0
    App          text @8
    DeploymentID text @16
}

struct runReq {
    Name     text        @0
    Image    text        @8
    Runtime  text        @16
    Port     i64         @24
    Shape    text        @32
    MinScale i64         @40
    MaxScale i64         @48
    GPU      i64         @56
    Env      list<bytes> @64
}

struct runView {
    ID     text @0
    Name   text @8
    URL    text @16
    Status text @24
    Shape  text @32
}

struct runnerBuildResp {
    BuildJobID text @0
    Status     text @8
    RunnerPool text @16
    Image      text @24
    Target     text @32
    Index      text @40
}

struct setEnvReq {
    Project text        @0
    App     text        @8
    Env     list<bytes> @16
}

interface platform {
    # Deletes an application and tears down what it runs.
    # It removes the application record and tears down what it owns in the org's
    # tenant namespace — its operator Service CR and its KMSSecret — then answers 204.
    # An app this org and project do not have is 404, never a silent success.
    # Teardown is best-effort by design: a cluster that refuses or is unreachable does
    # not block the delete, so the record cannot be left orphaned behind a broken
    # cluster; the failure is logged for operators and the orphan reaper reconciles
    # it. Requires a validated principal; 403 without one.
    delete_platform_projects_by_project_apps_by_app(req: appRef)
    # Detaches a hostname and releases the claim.
    # It drops the host from the app's ingress and releases any custom claim on it, so
    # the name becomes claimable again — by this org or any other. Answers 204.
    # The default host is permanent and cannot be removed: that is 400, not 404. A host
    # that is neither attached nor claimed here is 404. Requires a validated principal;
    # 403 without one.
    delete_platform_projects_by_project_apps_by_app_domains_by_host(req: domainRef)
    # Answers what this organisation has declared, joined with what
    # the delivery plane has done about it.
    # The join is best-effort BY DESIGN and says so when it is missing: the
    # declarations ARE the answer to "what have I deployed", so refusing the whole
    # board because the cluster is unreadable would lose the half that is readable.
    # What must never happen is a silent null — an unreadable plane is reported as
    # `cd.unavailable` carrying the reason, never as an app with no reconciliation.
    get_platform_apps(req: declarationsQuery) returns (rep: declaredResp)
    # Answers ONE declaration — what git says this app is, before the
    # delivery plane has had any say in it.
    get_platform_apps_by_app(req: declarationRef) returns (rep: Declaration)
    # Answers ONE app's reconciliation alone — the poll a deploy
    # console makes while it waits, without re-reading the whole inventory each time.
    get_platform_apps_by_app_cd(req: declarationRef) returns (rep: CDApp)
    # Returns real build records for your org.
    # It lists the org's BuildKit build records — the git build step behind a deploy —
    # each with the repo it built, the short commit, its status, when it started and
    # how long it took. These are real records or an honest empty list; a build appears
    # here because one ran, never because a page needed a row. Builds are created only
    # by /deploy and the push-to-deploy hook. Requires a validated principal; 403
    # without one.
    get_platform_builds() returns (rep: buildBoard)
    # Answers every Application the delivery plane holds.
    # Scoped to the namespaces the caller's own validated org owns: the ROLE admits
    # the caller and the tenant boundary is applied inside, so an admin of one org
    # never observes another's.
    get_platform_cd() returns (rep: cdResp)
    # Returns your deploy targets, and what is running on each.
    # It returns the org's environments — the distinct deploy targets its applications
    # name, `production` for anything that names none — each aggregating the apps that
    # target it, a rolled-up status and when it last changed.
    # An environment is DERIVED, not stored: there is nothing to create or delete here,
    # and an environment exists exactly as long as an app points at it. Requires a
    # validated principal; 403 without one.
    get_platform_environments() returns (rep: environmentBoard)
    # Returns the platform's own service tier, and where it has drifted.
    # It returns the board for the services the PLATFORM itself runs — iam, kms,
    # gateway and the rest — as `{apps, summary}`: per service its environment, health,
    # phase, the image tag its CR DECLARES, the tag actually running, and the drift
    # between them, plus a summary counting the board green, yellow and red.
    # This is not a customer surface. `/v1/platform/projects/:project/apps` is a
    # tenant's apps; this is the tier those tenants run ON, which is why the two are
    # named differently rather than sharing a prefix.
    # Admission is scoped at the SCAN, before any CR is read: a platform SuperAdmin
    # observes the whole fleet, an org admin observes only their own org's namespaces,
    # and an org that owns none gets an empty board — a non-super caller never even
    # lists another org's services. Narrow further with `env`, `health`, `org`, or
    # `drift=1` for only what has drifted.
    # It degrades honestly rather than failing whole: a namespace that does not exist
    # is skipped, and a running-state read the caller cannot make leaves the running
    # tag empty — an unknown, never a guess — while the declared, health and phase
    # columns still render.
    get_platform_fleet(req: fleetQuery) returns (rep: driftBoard)
    # Returns one platform service, resolved to production by default.
    # It returns a single platform service by its CR name, with the same
    # declared-versus-running and drift facts the board carries. The name must be a
    # DNS-1123 label; anything else is 400.
    # Namespaces are scanned in lifecycle order — main, then test, then dev — and the
    # first match wins, so a bare name resolves to PRODUCTION. The scan covers only the
    # namespaces the caller is authorized for, so an org admin can never read a service
    # outside their own org, and a name found in none of them is 404 rather than a leak.
    get_platform_fleet_by_app(req: fleetRef) returns (rep: AppView)
    # Reports whether this control plane can actually deploy anything.
    # A real probe, not a status page. It answers 200 only when the metadata store is
    # open AND the cluster is genuinely reachable — proved by LISTING the operator App
    # CRD, which settles reachability and CRD presence in one bounded call, and which
    # is the exact question every deploy depends on. Anything else is 503 carrying the
    # real reason and whether the CRD was found.
    # A constructed cluster client proves nothing — it is built from a kubeconfig, not
    # from a reachable apiserver — so this deliberately spends a round trip rather than
    # reporting `ok` while every deploy fails. Not admin-gated: liveness has to be
    # probe-able without a credential.
    get_platform_health() returns (rep: readiness)
    # Returns one build-and-deploy pipeline per app, with its latest run.
    # It returns one pipeline per application in the caller's org — its repo or image
    # source, its current status, and when its most recent deployment ran and how long
    # it took. A pipeline is a PROJECTION of an app plus its newest deployment, not a
    # separate record: it comes into existence with the app and is triggered only
    # through /deploy, never here. Requires a validated principal; 403 without one.
    get_platform_pipelines() returns (rep: pipelineBoard)
    # Returns your org's projects, each with how many apps live under it.
    # It lists the caller org's projects with the number of platform applications in
    # each. A project is IAM's resource — it is created and deleted at
    # /v1/iam/projects, never here — so this is the ONE projection IAM cannot serve:
    # the project plus what the platform has put under it.
    # Requires a validated principal; 403 without one, and the org comes from that
    # validated identity rather than a request header. This is the console's first
    # authenticated read, so a project store that is not yet initialised degrades to
    # an EMPTY list rather than a 500 — a new org genuinely has zero projects — and
    # the real cause is surfaced to operators instead of to the caller.
    get_platform_projects()
    # Returns one project and its app count.
    # It returns a single project of the caller's org with the number of platform
    # applications under it. A project this org does not have is 404, which is also
    # what another tenant's project looks like from here. Requires a validated
    # principal; 403 without one.
    get_platform_projects_by_project(req: projectRef) returns (rep: projectView)
    # Returns the applications in one project, with what the cluster says
    # about them.
    # It lists the caller org's applications under one project. Each row carries the
    # stored record and, for an app that is live or deploying, the LIVE phase and
    # health read from its operator Service CR; an app with sealed env also carries
    # its secret-sync state. Those cluster reads are best-effort — an unreachable
    # cluster leaves those fields empty and never blocks the listing.
    # The project must exist in IAM for this org, or the answer is 404; the `default`
    # project is implicit and always accepted, because it is part of what an org IS.
    # Requires a validated principal; 403 without one.
    get_platform_projects_by_project_apps(req: projectRef)
    # Returns one application, with its live phase, health and secret sync.
    # It returns a single application of the caller's org together with what the
    # cluster currently reports for it: the operator Service CR's phase and health,
    # and whether its sealed env has synced. An app this org and project do not have
    # is 404. Requires a validated principal; 403 without one.
    get_platform_projects_by_project_apps_by_app(req: appRef) returns (rep: appView)
    # Returns an app's deployment history.
    # It lists every deployment recorded for one of the caller org's applications,
    # newest version first, each with its version, status, source, commit and image.
    # Failed and superseded attempts are included — that is the point of a history.
    # Requires a validated principal; 403 without one.
    get_platform_projects_by_project_apps_by_app_deployments(req: appRef)
    # Returns one deployment of one app.
    # It returns a single deployment by id, scoped to the named application of the
    # caller's org — so an id belonging to another app or another tenant is 404, not a
    # read. Requires a validated principal; 403 without one.
    get_platform_projects_by_project_apps_by_app_deployments_by_id(req: deploymentRef) returns (rep: deploymentView)
    # Returns real logs for a deployment — the build's, then the app's.
    # It returns the deployment's recorded status timeline together with LIVE pod logs
    # pulled from the cluster: the build pod's output while a git build is running, and
    # the running app's output once it is deployed. The `source` field says which of
    # the two the body is — `build`, `app` or `none` — so a console can label the pane
    # honestly.
    # It never fabricates log content. When no pod exists yet, or the cluster is
    # unreachable, it degrades to the recorded timeline and says so. Every cluster read
    # is confined to the caller org's own namespaces and time-boxed. Requires a
    # validated principal; 403 without one.
    get_platform_projects_by_project_apps_by_app_deployments_by_id_logs(req: deploymentRef) returns (rep: deployLogs)
    # Returns every hostname this app answers on.
    # It lists the app's hosts: the permanent default host it was born with, any
    # org-subtree hosts attached to it, and every custom host claimed for it with its
    # verification state and, while pending, the DNS challenge records to publish. Live
    # endpoint status for each host is observed from the cluster. Requires a validated
    # principal; 403 without one.
    get_platform_projects_by_project_apps_by_app_domains(req: appRef)
    # Returns the versions that actually reached the cluster.
    # It lists the org's releases: the deployments that were genuinely applied to the
    # cluster, with the app they belong to, their version, environment, status and when
    # they were released. A deployment that failed or is still building is NOT a
    # release and is excluded — reaching the cluster is what makes one. Requires a
    # validated principal; 403 without one.
    get_platform_releases() returns (rep: releaseBoard)
    # Rolls a platform service's pods, in a named environment.
    # It triggers a rolling restart of one platform service's Deployment by stamping a
    # fresh restart annotation, and answers 202 with the app, the namespace, the
    # environment and the timestamp. It restarts pods; it does NOT change the image — a
    # version change is the release path, not this.
    # SuperAdmin ONLY, and deliberately narrower than the read gate beside it. The only
    # namespaces this board touches are the platform's own tier, so a restart here
    # recycles a SHARED service every tenant depends on. A brand-org admin is a
    # customer-org admin, not a platform operator: observing the board is bounded and
    # audited, and restarting production identity is not.
    # `?env=main|test|dev` is REQUIRED — a bare call does not default to production,
    # which is what closes the fat-finger and confused-deputy hazard — and any other
    # value is 400. A service with no Deployment to restart in that environment is 404.
    post_platform_fleet_by_app_deploy(req: restartRef) returns (rep: restarted)
    # Creates an application from a git repo or a container image.
    # It registers a new application under one of the caller org's projects and
    # answers 201 with it. Creating does NOT deploy: the app lands in `draft` and
    # nothing reaches the cluster until /deploy.
    # `source` is `git` — which requires `repo.url` — or `image`, which requires
    # `image.repository`; anything else is 400. A git app builds with zero-config
    # `pack` by default and may opt into `dockerfile`; an image app never builds. The
    # repo URL and Dockerfile path are validated here against the SAME allowlist the
    # privileged build enforces, so an unsafe source is refused before it is ever
    # persisted.
    # The `slug` is the app's identity in the cluster: given or derived from `name`,
    # it must match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`, and a slug already used in
    # this project is 409. `replicas` and `storageGb` are clamped to the deployment's
    # limits rather than refused.
    # Env keys must match `^[A-Za-z_][A-Za-z0-9_]*$`. A variable marked `secret: true`
    # is SEALED into KMS and its plaintext is never written to the database — and if
    # KMS is unavailable the create fails 503 rather than falling back to storing a
    # secret in the clear.
    # The app is seeded with its canonical default host, so it has a working HTTPS URL
    # the moment it deploys. A bare custom domain cannot be attached here — it has to
    # go through add-domain and DNS verification first. Requires a validated
    # principal; 403 without one, and every cluster object it will later create lands
    # in that org's own `tenant-<org>` namespace.
    post_platform_projects_by_project_apps(req: createAppReq) returns (rep: appView)
    # Deploys the app — building it first if it comes from git.
    # It starts a new, monotonically versioned deployment of the app and answers 202
    # with the deployment record. A 202 is an ACCEPTED deployment, not a live one.
    # An IMAGE app deploys the tag you name (falling back to the app's tag, then
    # `latest`) by writing its operator Service CR; the operator reconciles it to
    # running. A GIT app launches an in-cluster BuildKit Job at `commit` — or the app's
    # branch — and comes back in `building`; the Service CR is applied later, by the
    # reconciler, once the Job succeeds. The reconciler is restart-safe, so a build in
    # flight survives a cloud restart.
    # Deploys are bounded per org: over the concurrent-deploy cap is 429 and NOTHING is
    # recorded, so a rejected deploy leaves no phantom in the history. An unreachable
    # cluster is 503 but still records an honest `error` deployment, because a deploy
    # that was attempted and failed must not be indistinguishable from one never made.
    # Every other failure is likewise recorded in its real terminal state.
    # This is metered work: a git build is billed to the org's ledger in wall-clock
    # build minutes once the Job finishes, and the running deployment is billed for its
    # compute per tick for as long as it stays live. Requires a validated principal; 403
    # without one, and everything is written into that org's own `tenant-<org>`
    # namespace.
    post_platform_projects_by_project_apps_by_app_deploy(req: deployReq) returns (rep: deploymentView)
    # Attaches a hostname — instantly if you already own it, otherwise with a
    # DNS challenge.
    # It attaches `host` to the app, and which of two things happens depends on who
    # owns the name. A host inside the caller org's own subtree is structurally owned,
    # so it goes ACTIVE immediately and answers 201. A bring-your-own host is claimed
    # as PENDING and answers the DNS challenge records to publish; it is NOT rendered
    # into the app's ingress until /verify passes.
    # Claims are globally unique. A host already claimed by another organization is
    # 409, and so is one claimed by a different app in your own; re-adding this app's
    # OWN claim is idempotent and answers its current state at 200. The default host is
    # always attached and re-adding it is 409. A host under the platform's shared apex
    # that is not the caller's own subtree is 403 — it belongs to whoever owns that
    # subtree and can never be grabbed through the custom path.
    # `host` must be a valid DNS hostname; anything else is 400. Requires a validated
    # principal; 403 without one.
    post_platform_projects_by_project_apps_by_app_domains(req: addDomainReq) returns (rep: domainView)
    # Checks a custom domain's DNS and turns it on if it passes.
    # It runs the DNS challenge check for a pending custom host and, when it passes,
    # marks the host verified and renders it into the app's ingress so it starts
    # serving.
    # A check that RAN and did not pass is not an error: it answers 200 with the host
    # still pending and the reason in `detail`, so a console can show the operator what
    # DNS is actually returning. An already-verified host answers as-is without
    # re-checking. A host not claimed by this app is 404. Requires a validated
    # principal; 403 without one.
    post_platform_projects_by_project_apps_by_app_domains_by_host_verify(req: domainRef) returns (rep: domainView)
    # Puts a branch on its own URL.
    # It deploys an already-built `image` to a per-branch preview and answers its URL,
    # the branch, the preview's slug and the deployment. The preview is a FIRST-CLASS
    # application named `<app>-<branch>` in the same project and tenant namespace, with
    # its own default host — so it is completely isolated from production while reusing
    # the same deploy mechanic. Re-previewing a branch converges that same target in
    # place rather than stacking another one.
    # It carries NO environment variables, deliberately: a preview never inherits
    # production's secrets. It also does not build — `image` is required and must
    # already exist, and `branch` defaults to the parent app's. A branch that does not
    # resolve to a valid slug distinct from the parent's is 400. Requires a validated
    # principal; 403 without one.
    post_platform_projects_by_project_apps_by_app_preview(req: previewReq) returns (rep: previewView)
    # Promotes an already-built release to the app.
    # It redeploys an image that already exists — named either by `deploymentId`, which
    # promotes that deployment's exact built image, or by `tag`, resolved the same way
    # a deploy resolves one. One of the two is required; neither is 400.
    # Promotion never builds. A deployment that carries no built image cannot be
    # promoted and is 400, and a deployment id outside this app is 404. It runs through
    # the same deploy core as everything else, so it takes a NEW version number and is
    # subject to the same per-org concurrency cap. Requires a validated principal; 403
    # without one.
    post_platform_projects_by_project_apps_by_app_promote(req: promoteReq) returns (rep: deploymentView)
    # Goes back to the previous release.
    # It redeploys a prior image: the one named by `deploymentId`, or — with no body —
    # the newest earlier deployment that carries a real built image and did not error,
    # skipping the release currently live. An app with nothing earlier to return to is
    # 400.
    # A rollback is a deploy of an old image, not a rewind: it takes a NEW version
    # number and appends to the history rather than erasing what came after. Both
    # lookups are scoped to this app and org, so another tenant's image can never be
    # rolled in. Requires a validated principal; 403 without one.
    post_platform_projects_by_project_apps_by_app_rollback(req: rollbackReq) returns (rep: deploymentView)
    # Starts a stopped app back up.
    # It scales the app's Service back to its configured replica count and marks it
    # live, answering the updated application. It does not redeploy: the image already
    # on the Service CR is what comes back.
    # The billing watermark is reset to now as part of starting, so the org is charged
    # for THIS live span and never for the gap the app spent stopped. An app with no
    # Service CR is 404, an unreachable cluster is 503, and a cluster that refuses the
    # scale is 502. Requires a validated principal; 403 without one.
    post_platform_projects_by_project_apps_by_app_start(req: appRef) returns (rep: appView)
    # Stops an app without deleting it.
    # It scales the app's Service to zero replicas and marks it stopped, answering the
    # updated application. Nothing else is removed — the record, its env, its domains
    # and its deployment history all survive, and /start brings it back at the same
    # replica count.
    # An app that is not deployed has no Service CR to scale and is 404. An
    # unreachable cluster is 503 and a cluster that refuses the scale is 502. Because
    # the pods stop, so does the compute metering. Requires a validated principal; 403
    # without one.
    post_platform_projects_by_project_apps_by_app_stop(req: appRef) returns (rep: appView)
    # Runs a container image and gives back a URL.
    # The one-call shortcut over project → app → deploy: give it a `name` and an
    # `image` and it creates or updates an image-source application in your org's
    # DEFAULT project, deploys it through the same operator Service-CR writer
    # everything else uses, and answers its id, name, live URL, status and shape.
    # Re-running the same name UPDATES it in place, so the call is idempotent by name.
    # What it produces is a first-class application, not a special object: it is
    # listable, stoppable and redeployable through the /v1/platform routes like any
    # other app.
    # `minScale` is the replica floor. `maxScale` above it declares an autoscaling
    # ceiling; `maxScale: 0` means no autoscaler at all — a fixed run at the floor.
    # Both are clamped to the deployment's limits. `runtime` and `shape` are accepted
    # for the client contract and echoed back: the image is the runtime unit and sizing
    # is the operator's default.
    # It is BILLING-GATED before it touches the cluster: a flat per-run fee is
    # authorized against the org's own prepaid balance first, so an org that cannot pay
    # is refused without anything being created. An unreachable cluster is 503 — a run
    # never reports a URL it did not create. Secret env is sealed into KMS and fails
    # closed without it.
    # Requires a validated principal; 403 without one. The org is resolved from that
    # validated identity and is what both pays and owns the namespace — it is never
    # read from the body.
    post_platform_run(req: runReq) returns (rep: runView)
    # Replaces an app's environment variables.
    # It writes the app's whole environment set and answers the updated application.
    # This is the one post-create write path for env, and it REPLACES rather than
    # merges: a variable absent from the body is gone, and a secret dropped from the
    # set leaves the app's Secret on its next deploy.
    # Keys must match `^[A-Za-z_][A-Za-z0-9_]*$`. A value marked `secret: true` is
    # sealed into KMS and blanked in the database, so plaintext is never persisted —
    # and the write fails 503 if KMS is unavailable rather than storing one in the
    # clear.
    # The rule worth knowing: this does not restart anything. Once the app has been
    # deployed the secret sync is re-declared immediately so the operator
    # re-materialises the Secret, but RUNNING pods keep the environment they started
    # with until their next deploy or restart. Requires a validated principal; 403
    # without one.
    put_platform_projects_by_project_apps_by_app_env(req: setEnvReq) returns (rep: appView)
}

# ---------------------------------------------------------------------
# 33 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   post_platform_runner  runnerBuildReq.Args  map[string]string  (map)
#
# opaque (21) — crosses, arrives without its name:
#   AppView.Drift  platform.Verdict
#   Declaration.Env  platform.declareEnv (list element)
#   appView.Env  platform.EnvVarJSON (list element)
#   appView.Image  platform.imageView
#   appView.Repo  platform.gitSource
#   buildBoard.Builds  platform.buildRow (list element)
#   cdResp.Applications  platform.CDApp (list element)
#   createAppReq.Env  platform.EnvVarJSON (list element)
#   createAppReq.Image  platform.imageOrigin
#   createAppReq.Repo  platform.gitOrigin
#   declaredResp.Apps  platform.declared (list element)
#   declaredResp.CD  platform.unreadable
#   domainView.Records  fqdn.Record (list element)
#   driftBoard.Apps  platform.AppView (list element)
#   driftBoard.Summary  platform.fleetSummary
#   environmentBoard.Environments  platform.environmentRow (list element)
#   pipelineBoard.Pipelines  platform.pipelineRow (list element)
#   previewView.Deployment  platform.deploymentView
#   releaseBoard.Releases  platform.releaseRow (list element)
#   runReq.Env  platform.EnvVarJSON (list element)
#   setEnvReq.Env  platform.EnvVarJSON (list element)
