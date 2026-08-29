# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package integrations

struct authorizeOut {
    AuthorizeURL text @0
}

struct connectIn {
    Provider  text @0
    Token     text @8
    AccountID text @16
}

struct connectOut {
    AuthorizeURL text       @0
    Connected    bool       @8
    Provider     text       @16
    Account      text       @24
    ExternalID   text       @32
    Scopes       list<text> @40
}

struct connectorProvidersOut {
    Providers list<bytes> @0
}

struct connectorRef {
    ID text @0
}

struct connectorTokenOut {
    Token     text @0
    Provider  text @8
    Label     text @16
    ExpiresAt text @24
}

struct connectorsOut {
    Connectors list<bytes> @0
}

struct credentialIn {
    Provider  text  @0
    Label     text  @8
    Token     text  @16
    AccountID text  @24
    OAuth     bytes @32
}

struct credentialOut {
    Connected  bool  @0
    Connection bytes @8
}

struct devicePollIn {
    Provider text @0
    Flow     text @8
}

struct devicePollOut {
    Status     text  @0
    Interval   i64   @8
    Connection bytes @16
}

struct deviceStartIn {
    Provider text @0
    Label    text @8
}

struct deviceStartOut {
    Flow      text @0
    UserCode  text @8
    VerifyURL text @16
    Interval  i64  @24
    ExpiresAt text @32
}

struct disconnectOut {
    Disconnected bool @0
}

struct githubBackfillIn {
    State text @0
}

struct githubBackfillResult {
    Repos     i64  @0
    Issues    i64  @8
    Created   i64  @16
    Updated   i64  @24
    Failed    i64  @32
    Truncated bool @40
}

struct githubClaimIn {
    Accounts list<text> @0
    All      bool       @8
}

struct githubClaimOut {
    Claimed list<text> @0
    Already list<text> @8
}

struct githubForkOut {
    FullName      text @0
    HTMLURL       text @8
    CloneURL      text @16
    DefaultBranch text @24
    Existing      bool @32
}

struct githubForkReq {
    Repo text @0
    Org  text @8
}

struct githubImportIn {
    Repos list<text> @0
    All   bool       @8
}

struct githubImportOut {
    Queued i64        @0
    Repos  list<text> @8
}

struct githubInstallationsOut {
    Installations list<bytes> @0
    InstallURL    text        @8
}

struct githubPagesBuildOut {
    Repo   text @0
    Status text @8
    URL    text @16
}

struct githubPagesDisabledOut {
    Repo     text @0
    Disabled bool @8
}

struct githubPagesEnableReq {
    Repo      text @0
    Branch    text @8
    Path      text @16
    BuildType text @24
}

struct githubPagesUpdateReq {
    Repo          text @0
    CNAME         text @8
    HTTPSEnforced bool @16
    BuildType     text @24
    Branch        text @32
    Path          text @40
}

struct githubPagesUpdatedOut {
    Repo    text @0
    Updated bool @8
}

struct githubPagesView {
    Repo          text  @0
    Status        text  @8
    URL           text  @16
    CNAME         text  @24
    Custom404     bool  @32
    BuildType     text  @40
    HTTPSEnforced bool  @48
    Source        bytes @56
}

struct githubRepoRef {
    Repo text @0
}

struct githubReposOut {
    Repos  list<bytes> @0
    Unread list<text>  @8
}

struct githubSearchOut {
    Repos list<bytes> @0
    Count i64         @8
}

struct githubSearchReq {
    Q     text @0
    Limit i64  @8
}

struct gitlabProjectsOut {
    Projects list<bytes> @0
    Account  text        @8
}

struct listOut {
    Providers list<bytes> @0
}

struct providerRef {
    Provider text @0
}

struct providerView {
    ID          text  @0
    Name        text  @8
    Description text  @16
    Category    text  @24
    Available   bool  @32
    Connected   bool  @33
    Connection  bytes @40
}

struct refreshOut {
    Refreshed  bool  @0
    Connection bytes @8
}

struct verifyOut {
    Provider   text       @0
    Active     bool       @8
    Reason     text       @16
    Account    text       @24
    ExternalID text       @32
    Scopes     list<text> @40
}

interface integrations {
    # Forgets a connector: every custodied secret, then the row.
    # Idempotent — dropping a never-connected id still answers {disconnected:true}
    # (disconnect() parity). No provider Revoke: none of the user-plane providers
    # exposes a revoke endpoint.
    delete_integrations_connectors_by_id(req: connectorRef) returns (rep: disconnectOut)
    # Deletes the repo's Pages site. 404 when there is none, so a
    # caller can tell "turned it off" from "there was nothing on".
    delete_integrations_github_repos_by_repo_pages(req: githubRepoRef) returns (rep: githubPagesDisabledOut)
    # Returns every registered integration provider together with THIS org's
    # connection status for it — the catalog the console's Integrations page renders.
    # Org-authed: a caller with no validated principal is 403, because the status is
    # per-org and there is no org-less answer. User-plane providers (the /v1/integrations/connectors
    # surface) are omitted; the two planes are disjoint.
    get_integrations() returns (rep: listOut)
    # Returns ONE provider with this org's connection status — the same view list
    # carries, for a single id. An unknown id is 404, and so is a user-plane provider:
    # the org surface never resolves one.
    get_integrations_by_provider(req: providerRef) returns (rep: providerView)
    # Lists the caller's OWN connectors across every provider — the set
    # `hanzo connector ls` prints. Rows are keyed (org,user), so this can never
    # surface another user's connector, and no secret is in the view.
    get_integrations_connectors() returns (rep: connectorsOut)
    # Hands the custodied access token to its owner — the ONE place
    # custody exits. The (org,user)-keyed row IS the same-user gate: another user's
    # id is simply "no row" → 404. fresh() auto-rotates within the refreshSkew
    # window; static providers degenerate to a plain kmsGet of Secrets[0]. Refresh
    # tokens are NEVER returned — custody keeps the sink. The token is never logged.
    get_integrations_connectors_by_id_token(req: connectorRef) returns (rep: connectorTokenOut)
    # Lists the user-scoped provider cards — the catalog of what a
    # user can connect, and how. Methods derive from capabilities (Device/Adopt/Verify
    # — Mount asserts at least one), never from a parallel kind enum.
    get_integrations_connectors_providers() returns (rep: connectorProvidersOut)
    # Lists the GitHub accounts the caller may see the App
    # installed on, each confirmed against the App's own list, plus where to add
    # another.
    # The confirmation is the point. A connection row holds an installation id, and
    # an id whose installation was since removed on GitHub is a row that mints
    # nothing — every list and import against it fails with a token error, which
    # reads as "our git integration is broken" rather than "that install is gone".
    # Checking the App's view turns that into a fact the caller can act on.
    # ORG-SCOPED for a tenant, deliberately. The App is installed across every
    # customer, so the raw list is the customer list; a tenant sees only accounts its
    # own org has bound. It discovers a NEW account by installing it (InstallURL),
    # which is GitHub's own consent screen — not by reading ours.
    # A SUPER ADMIN sees the App's whole install list, because that list is the
    # platform's own inventory rather than any one tenant's data, and platform sudo
    # is the single cross-tenant scope this house has. Without it an App installed
    # out-of-band — granted straight from GitHub, so no connect flow ever ran and no
    # connection row exists — is invisible to everyone: the console card reads "not
    # connected" and an operator asked "which GitHub orgs do you see" can only
    # answer for accounts already bound, which is precisely the accounts that were
    # never the question.
    get_integrations_github_installations() returns (rep: githubInstallationsOut)
    # Lists the org's granted GitHub repositories, each annotated with its
    # native import + sync status from the git object plane. Org-authed: the org comes
    # from the validated principal, and the granted set is bounded to THAT org's
    # installation token — an org can never enumerate another org's repos. The console
    # polls it to watch an import flip a repo to imported.
    get_integrations_github_repos() returns (rep: githubReposOut)
    # Returns the repo's Pages status, live URL, custom domain and build
    # source. The repo is resolved against the org installation's GRANTED set, so a
    # caller can never address a repo the App was not granted; 404 when the repo has no
    # Pages site.
    get_integrations_github_repos_by_repo_pages(req: githubRepoRef) returns (rep: githubPagesView)
    # Lists the projects the org's GitLab connection can reach —
    # membership projects, most recently active first.
    get_integrations_gitlab_projects() returns (rep: gitlabProjectsOut)
    # Acquires the org's credential for one provider. It has TWO paths and the
    # REQUEST picks which: a "token" key in the body seals that credential directly
    # (verify-before-store), and its absence begins the 3-legged OAuth flow — minting a
    # single-use nonce plus an HMAC-signed state that binds this org to this provider,
    # and answering with the provider's authorize URL for the caller to redirect to.
    # Fail-closed order, unchanged: no principal → 403; unknown provider → 404; an
    # AdminOnly connector without the caller's own-org admin bit → 403; not configured
    # → 503; KMS not ready → 503 (the flow WILL need to seal a token, so refuse now
    # rather than dead-end at the callback).
    post_integrations_by_provider_connect(req: connectIn) returns (rep: connectOut)
    # Revokes (best-effort) and forgets an org's connection: it deletes
    # every custodied KMS secret and the connection row. Idempotent — disconnecting a
    # provider that was never connected still returns {disconnected:true}. Symmetric
    # with connect: an AdminOnly connector needs the caller's own-org admin bit.
    post_integrations_by_provider_disconnect(req: providerRef) returns (rep: disconnectOut)
    # Re-checks a CONNECTED apikey connector's stored credential against the
    # provider, live (`hanzo connector verify`). Org-scoped (any member may check
    # status); the credential is read from KMS, verified, and NEVER returned or logged.
    # A verification failure is reported as {active:false}, not an error — the console/
    # CLI renders it. Only apikey providers support verify (OAuth tokens are checked at
    # use, not re-verified here).
    post_integrations_by_provider_verify(req: providerRef) returns (rep: verifyOut)
    # Forces a token rotation for a connected connector, ahead of the
    # automatic rotation a token read would do inside the expiry window. Only
    # providers that declare a Refresh support it.
    post_integrations_connectors_by_id_refresh(req: connectorRef) returns (rep: refreshOut)
    # Is the direct intake path: a customer-held token/setup-token
    # (Verify) or an externally obtained OAuth bundle from the CLI's local PKCE
    # (Adopt). ALWAYS verify-before-store: a bad credential is refused and NOTHING
    # is persisted (connectByCredential's fail-closed order).
    post_integrations_connectors_by_provider_credential(req: credentialIn) returns (rep: credentialOut)
    # Begins a device sign-in and returns the code to show the user plus
    # how to poll for completion. KMS readiness is checked NOW rather than dead-ending
    # the user at poll-done (connect() parity), and the per-provider connector cap is
    # checked before the provider is called. The provider's device code is persisted
    # only in the encrypted grants table and is NEVER returned.
    post_integrations_connectors_by_provider_device(req: deviceStartIn) returns (rep: deviceStartOut)
    # Advances a device sign-in. Terminal outcomes are DATA, not errors
    # (verifyConn {active:false} discipline) — the status set is closed:
    # pending|connected|denied|expired. pollSlow collapses to "pending" on the
    # wire; the raised cadence rides interval.
    post_integrations_connectors_by_provider_device_by_flow_poll(req: devicePollIn) returns (rep: devicePollOut)
    # Binds installations the App ALREADY holds to the org the caller is
    # acting in — the reconciliation for a grant that happened outside our connect
    # flow.
    # An installation IS the grant: GitHub recorded the consent when the App was
    # installed, and our connection row is bookkeeping that never got written because
    # nobody came through our callback. This writes that row from the App's own view,
    # so 23 accounts granted straight from GitHub stop reading as nothing.
    # The org is taken from the VALIDATED PRINCIPAL and never from the body, because
    # it is the one part GitHub cannot tell us. An installation carries an account
    # login, a type and a repository selection — nothing that names a Hanzo org. So
    # the binding cannot be DERIVED, only asserted, and the only unforgeable assertion
    # available is the org the caller is already acting in. Inferring one from the
    # account name would be a guess the store cannot catch: its key is
    # (org,provider,owner), so a wrong org is a valid row, and a valid row is a
    # mirror pointed at the wrong tenant.
    # SUPER ADMIN only, for that same reason. A tenant's proof that an account is
    # theirs is GitHub's own consent screen — the connect flow — and without it any
    # org could claim any account the App holds. Platform sudo is already the scope
    # that reads the whole install list, so it is the scope that may bind from it;
    # giving a tenant this verb would hand it every other tenant's repositories.
    # Idempotent: the row is keyed (org,provider,owner) and connected_at survives an
    # upsert, so claiming twice rebinds the same account to the same org and reports
    # it under `already`. Re-claiming also REFRESHES the installation id, so an
    # account reinstalled on GitHub — new id, same login — self-heals instead of
    # minting tokens against a dead installation.
    # Claiming an account another org holds ADDS this org's row and leaves theirs
    # standing, so no org loses an integration it is using.
    post_integrations_github_claim(req: githubClaimIn) returns (rep: githubClaimOut)
    # Forks a granted repository.
    # GitHub's fork is ASYNCHRONOUS: it answers 202 with the target repo and
    # populates it in the background, and it answers the same 202 when the fork
    # already exists. So this reports what GitHub said rather than waiting — a call
    # that blocked until the clone finished would time out on a large repository and
    # tell the caller nothing it does not already know.
    post_integrations_github_fork(req: githubForkReq) returns (rep: githubForkOut)
    # Seeds the native todo with the EXISTING issues across the
    # org's granted repos (default state=open); the webhook keeps them live thereafter.
    # Org-scoped by the validated principal — a caller only ever backfills its OWN org.
    # Synchronous + bounded (a total time budget and an issue cap) so it returns the
    # counts directly; idempotent by ExtRef, so a re-run continues where a truncated
    # pass left off and never duplicates.
    post_integrations_github_issues_backfill(req: githubBackfillIn) returns (rep: githubBackfillResult)
    # Creates the repo's Pages site and answers 201 Created with it.
    # With buildType "workflow" the site builds via GitHub Actions; otherwise it builds
    # from a branch source, defaulting to the repo's own default branch when none is
    # given. Only "/" and "/docs" are legal source paths (GitHub's rule).
    post_integrations_github_repos_by_repo_pages(req: githubPagesEnableReq) returns (rep: githubPagesView)
    # Requests a Pages rebuild and returns the queued build's status.
    # The build is queued AT GITHUB, not completed here, so the answer is 202 Accepted
    # and its status is the one GitHub reported at queue time. 404 when the repository
    # has no Pages site, or when the org's installation was not granted it.
    post_integrations_github_repos_by_repo_pages_builds(req: githubRepoRef) returns (rep: githubPagesBuildOut)
    # Imports the selected (or all) granted repos into git.hanzo.ai. The
    # selection is intersected with the installation's GRANTED set, so a client can
    # never import a repo the App was not granted (org isolation + a grant check). The
    # import runs in a bounded background worker (don't block the request), so the
    # answer is 202 Accepted; poll GET /v1/integrations/github/repos for the per-repo
    # status to flip to imported.
    post_integrations_github_repos_import(req: githubImportIn) returns (rep: githubImportOut)
    # Finds repositories on GitHub.
    # This reads the PUBLIC index and returns nothing an installation unlocks: it is
    # how you find a repository to fork, not a way to see inside one. The org's own
    # token is used only so the query is rate-limited against the installation
    # rather than anonymously — the results are the same ones anyone would get.
    post_integrations_github_search(req: githubSearchReq) returns (rep: githubSearchOut)
    # Mints a short, single-use deep-link code bound to the caller's
    # org and returns the t.me link the console navigates to. Org-authed: a caller with
    # no validated principal is 403 (same gate as the framework connect). The code is
    # stored as an oauth_nonce (org,telegram); the webhook's /start handler claims it to
    # bind chat→org. It is short (128-bit hex) so it fits Telegram's 64-char `start`
    # payload limit.
    post_integrations_telegram_connect() returns (rep: authorizeOut)
    # Sets or clears the custom domain (cname) and updates HTTPS
    # enforcement, build type, or source. ONLY the provided fields are sent to GitHub,
    # so an update never resets a setting the caller did not mention.
    put_integrations_github_repos_by_repo_pages(req: githubPagesUpdateReq) returns (rep: githubPagesUpdatedOut)
}

# ---------------------------------------------------------------------
# 27 op(s) here. What follows is what this schema does not carry.
#
# opaque (13) — crosses, arrives without its name:
#   connectorProvidersOut.Providers  integrations.connectorProviderView (list element)
#   connectorsOut.Connectors  integrations.connView (list element)
#   credentialIn.OAuth  integrations.oauthBundleIn
#   credentialOut.Connection  integrations.connView
#   devicePollOut.Connection  integrations.connView
#   githubInstallationsOut.Installations  integrations.githubInstallationView (list element)
#   githubPagesView.Source  integrations.githubPagesSource
#   githubReposOut.Repos  integrations.githubRepoView (list element)
#   githubSearchOut.Repos  integrations.githubSearchHit (list element)
#   gitlabProjectsOut.Projects  integrations.gitlabProjectView (list element)
#   listOut.Providers  integrations.providerView (list element)
#   providerView.Connection  integrations.connectionView
#   refreshOut.Connection  integrations.connView
