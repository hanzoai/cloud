# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package provider

struct authorizeOut {
    AuthorizeURL text @0
}

struct connectOut {
    AuthorizeURL text       @0
    Connected    bool       @8
    Provider     text       @16
    Account      list<text> @24
    ExternalID   list<text> @32
    Scopes       list<text> @40
}

struct connectionRef {
    ID text @0
}

struct connectionsOut {
    Connections list<bytes> @0
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
    Status     text      @0
    Interval   list<i64> @8
    Connection bytes     @16
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

struct disconnectIn {
    Provider text @0
    Account  text @8
}

struct disconnectOut {
    Disconnected bool       @0
    Accounts     list<text> @8
    Uninstalled  list<text> @16
}

struct githubBackfillIn {
    State text @0
    Repo  text @8
}

struct githubBackfillResult {
    Repos     i64  @0
    Issues    i64  @8
    Created   i64  @16
    Updated   i64  @24
    Failed    i64  @32
    Truncated bool @40
}

struct githubBranchesIn {
    Owner text @0
    Repo  text @8
    Q     text @16
    Limit i64  @24
    After text @32
}

struct githubBranchesOut {
    Branches list<bytes> @0
    Next     text        @8
    Total    i64         @16
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
    Queued    i64        @0
    Repos     list<text> @8
    Codebases list<text> @16
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
    Repo          text       @0
    CNAME         list<text> @8
    HTTPSEnforced list<bool> @16
    BuildType     text       @24
    Branch        text       @32
    Path          text       @40
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

struct githubReposIn {
    Q     text @0
    Owner text @8
    Limit i64  @16
    After text @24
}

struct githubReposOut {
    Repos     list<bytes> @0
    Next      text        @8
    Total     i64         @16
    Unread    list<text>  @24
    Connected bool        @32
}

struct githubSearchOut {
    Repos list<bytes> @0
    Count i64         @8
}

struct githubSearchReq {
    Q     text @0
    Limit i64  @8
}

struct githubUserCompleteIn {
    Grant text @0
}

struct githubUserConnectIn {
    Return text @0
}

struct githubUserConnectOut {
    AuthorizeURL text @0
}

struct githubUserDisconnectOut {
    Disconnected bool @0
    Revoked      bool @1
}

struct githubUserOut {
    Configured  bool @0
    Connected   bool @1
    Login       text @8
    ExpiresAt   text @16
    ConnectedAt text @24
}

struct gitlabProjectsOut {
    Projects list<bytes> @0
    Account  text        @8
}

struct googleUserCompleteIn {
    Grant text @0
}

struct googleUserConnectIn {
    Return text @0
}

struct googleUserConnectOut {
    AuthorizeURL text @0
}

struct googleUserDisconnectOut {
    Disconnected bool @0
    Revoked      bool @1
}

struct googleUserOut {
    Configured  bool @0
    Connected   bool @1
    Email       text @8
    Send        bool @16
    ExpiresAt   text @24
    ConnectedAt text @32
}

struct linearBackfillIn {
    State text @0
}

struct linearBackfillResult {
    Issues    i64  @0
    Created   i64  @8
    Updated   i64  @16
    Failed    i64  @24
    Truncated bool @32
}

struct linearClaimIn {
    Secret text @0
}

struct linearClaimOut {
    Organization text @0
    Name         text @8
    Path         text @16
}

struct linearCommentIn {
    Issue text @0
    Body  text @8
}

struct linearCommentOut {
    ID  text @0
    URL text @8
}

struct listOut {
    Providers list<bytes> @0
}

struct providerRef {
    Provider text @0
}

struct providerView {
    ID          text        @0
    Name        text        @8
    Description text        @16
    Category    text        @24
    Scopes      list<text>  @32
    Available   bool        @40
    Connected   bool        @41
    Connection  bytes       @48
    Accounts    list<bytes> @56
    Modes       list<text>  @64
    Kind        text        @72
    Fields      list<bytes> @80
    Logo        text        @88
    Actions     list<bytes> @96
    Triggers    list<bytes> @104
    Runnable    bool        @112
    Checks      bool        @113
    App         text        @120
    Note        text        @128
}

struct refreshOut {
    Refreshed  bool  @0
    Connection bytes @8
}

struct slackChannelsIn {
    Types  text @0
    Cursor text @8
}

struct slackChannelsOut {
    Channels   list<bytes> @0
    NextCursor text        @8
}

struct slackDeleteMessageIn {
    Channel text @0
    TS      text @8
}

struct slackDeleteMessageOut {
    Channel text @0
    TS      text @8
}

struct slackJoinOut {
    Listed  i64         @0
    Joined  list<text>  @8
    Already i64         @16
    Failed  list<bytes> @24
}

struct slackMessagesIn {
    Channel  text @0
    Limit    i64  @8
    Cursor   text @16
    ThreadTS text @24
    Oldest   text @32
    Latest   text @40
}

struct slackMessagesOut {
    Channel    text        @0
    Messages   list<bytes> @8
    HasMore    bool        @16
    NextCursor text        @24
}

struct slackReactIn {
    Channel text @0
    TS      text @8
    Name    text @16
}

struct slackReactOut {
    Channel text @0
    TS      text @8
    Name    text @16
}

struct slackSearchIn {
    Query text @0
    Count i64  @8
    Page  i64  @16
}

struct slackSearchOut {
    Matches list<bytes> @0
    Total   i64         @8
    Page    i64         @16
    Pages   i64         @24
}

struct slackSendMessageIn {
    Channel  text @0
    Text     text @8
    ThreadTS text @16
}

struct slackSendMessageOut {
    Channel text @0
    TS      text @8
}

struct slackUpdateMessageIn {
    Channel text @0
    TS      text @8
    Text    text @16
}

struct slackUpdateMessageOut {
    Channel text @0
    TS      text @8
}

struct tokenOut {
    Token     text @0
    Provider  text @8
    Label     text @16
    ExpiresAt text @24
}

struct userCatalogOut {
    Providers list<bytes> @0
}

struct verifyOut {
    Provider   text       @0
    Active     bool       @8
    Reason     text       @16
    Account    list<text> @24
    ExternalID list<text> @32
    Scopes     list<text> @40
}

struct whatsappGroupIn {
    Subject     text @0
    Description text @8
}

struct whatsappGroupOut {
    ID         text @0
    InviteLink text @8
}

struct whatsappInviteIn {
    Group text @0
}

interface provider {
    # Forgets a connector: every custodied secret, then the row.
    # Idempotent — dropping a never-connected id still answers {disconnected:true}
    # (disconnect() parity). No provider Revoke: none of the user-plane providers
    # exposes a revoke endpoint.
    delete_connection_by_id(req: connectionRef) returns (rep: disconnectOut)
    # Deletes the repo's Pages site. 404 when there is none, so a
    # caller can tell "turned it off" from "there was nothing on".
    delete_provider_github_repos_by_repo_pages(req: githubRepoRef) returns (rep: githubPagesDisabledOut)
    # Takes back one of this app's messages:
    # DELETE /v1/provider/slack/messages.
    delete_provider_slack_messages(req: slackDeleteMessageIn) returns (rep: slackDeleteMessageOut)
    # Lists the caller's OWN connectors across every provider — the set
    # `hanzo connector ls` prints. Rows are keyed (org,user), so this can never
    # surface another user's connector, and no secret is in the view.
    get_connection() returns (rep: connectionsOut)
    # Hands the custodied access token to its owner — the ONE place
    # custody exits. The (org,user)-keyed row IS the same-user gate: another user's
    # id is simply "no row" → 404. fresh() auto-rotates within the refreshSkew
    # window; static providers degenerate to a plain kmsGet of Secrets[0]. Refresh
    # tokens are NEVER returned — custody keeps the sink. The token is never logged.
    get_connection_by_id_token(req: connectionRef) returns (rep: tokenOut)
    # Lists the USER-plane provider cards. It is not the org
    # catalog at /v1/provider and the two are not a duplicate of one another: a
    # provider declares its custody plane, and the planes are disjoint, so a
    # user-scoped provider 404s on the org surface and vice versa. Hence the
    # catalog of what a
    # user can connect, and how. Modes derive from capabilities (Device/Adopt/Verify
    # — Mount asserts at least one), never from a parallel kind enum.
    get_connection_providers() returns (rep: userCatalogOut)
    # Returns every registered integration provider together with THIS org's
    # connection status for it — the catalog the console's Integrations page renders.
    # Org-authed: a caller with no validated principal is 403, because the status is
    # per-org and there is no org-less answer. User-plane providers (the /v1/connection
    # surface) are omitted; the two planes are disjoint.
    get_provider() returns (rep: listOut)
    # Returns ONE provider with this org's connection status — the same view list
    # carries, for a single id. An unknown id is 404, and so is a user-plane provider:
    # the org surface never resolves one.
    get_provider_by_provider(req: providerRef) returns (rep: providerView)
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
    get_provider_github_installations() returns (rep: githubInstallationsOut)
    # Lists the GitHub repositories the caller may work on, most
    # recently pushed first, searched and paged on the server.
    # A person who connected their own GitHub sees what their token reaches in this
    # App's installations. An org admin who has not sees every repository in the
    # installations their org holds. Anyone else gets an empty page with
    # connected:false, which is the console's cue to ask them to connect.
    get_provider_github_repos(req: githubReposIn) returns (rep: githubReposOut)
    # Lists a repository's branches, the default first, with prefix
    # search. The repository must be one the caller may see — the same set
    # GET /v1/provider/github/repos lists — or it is 404.
    get_provider_github_repos_by_owner_by_repo_branches(req: githubBranchesIn) returns (rep: githubBranchesOut)
    # Returns the repo's Pages status, live URL, custom domain and build
    # source. The repo is resolved against the org installation's GRANTED set, so a
    # caller can never address a repo the App was not granted; 404 when the repo has no
    # Pages site.
    get_provider_github_repos_by_repo_pages(req: githubRepoRef) returns (rep: githubPagesView)
    # Reports whether the caller has connected their own GitHub
    # account in the org they act in, and as whom.
    get_provider_github_user() returns (rep: githubUserOut)
    # Lists the projects the org's GitLab connection can reach —
    # membership projects, most recently active first.
    get_provider_gitlab_projects() returns (rep: gitlabProjectsOut)
    # Reports whether the caller has connected their own Google
    # account in the org they act in, and as whom.
    get_provider_google_user() returns (rep: googleUserOut)
    # Lists Slack conversations for the caller's connected workspace.
    # Read next_cursor until empty to discover channels, including private channels
    # or group DMs when types requests them and the bot has their read scopes.
    get_provider_slack_channels(req: slackChannelsIn) returns (rep: slackChannelsOut)
    # Reads recent messages from a named Slack channel such as
    # #hanzo-gtm. The bot must be a member and have the relevant history scope.
    # Set thread_ts to fetch a parent's replies, and cursor to continue a page.
    # Channel history is newest first; thread replies are oldest first.
    get_provider_slack_messages(req: slackMessagesIn) returns (rep: slackMessagesOut)
    # Reads the invite link of a group this org's number owns.
    # Org admin, for the same reason creating one is: the link admits anyone who
    # holds it.
    get_provider_whatsapp_groups(req: whatsappInviteIn) returns (rep: whatsappGroupOut)
    # Forces a token rotation for a connected connector, ahead of the
    # automatic rotation a token read would do inside the expiry window. Only
    # providers that declare a Refresh support it.
    post_connection_by_id_refresh(req: connectionRef) returns (rep: refreshOut)
    # Is the direct intake path: a customer-held token/setup-token
    # (Verify) or an externally obtained OAuth bundle from the CLI's local PKCE
    # (Adopt). ALWAYS verify-before-store: a bad credential is refused and NOTHING
    # is persisted (connectByCredential's fail-closed order).
    post_connection_by_provider_credential(req: credentialIn) returns (rep: credentialOut)
    # Begins a device sign-in and returns the code to show the user plus
    # how to poll for completion. KMS readiness is checked NOW rather than dead-ending
    # the user at poll-done (connect() parity), and the per-provider connector cap is
    # checked before the provider is called. The provider's device code is persisted
    # only in the encrypted grants table and is NEVER returned.
    post_connection_by_provider_device(req: deviceStartIn) returns (rep: deviceStartOut)
    # Advances a device sign-in. Terminal outcomes are DATA, not errors
    # (verifyConn {active:false} discipline) — the status set is closed:
    # pending|connected|denied|expired. pollSlow collapses to "pending" on the
    # wire; the raised cadence rides interval.
    post_connection_by_provider_device_by_flow_poll(req: devicePollIn) returns (rep: devicePollOut)
    # Revokes (best-effort) and forgets an org's connection: it deletes
    # every custodied KMS secret and the connection row. Idempotent — disconnecting a
    # provider that was never connected still returns {disconnected:true}. Symmetric
    # with connect: an AdminOnly connector needs the caller's own-org admin bit.
    # An `account` disconnects that one account and leaves the org's others; without
    # it every account goes. A provider with an App installation (GitHub) also removes
    # the installation at the provider for each account no other org still holds, so
    # connecting that account again is a fresh install rather than GitHub's Configure
    # page, which returns nothing the connect can bind.
    post_provider_by_provider_disconnect(req: disconnectIn) returns (rep: disconnectOut)
    # Re-checks a CONNECTED apikey connector's stored credential against the
    # provider, live (`hanzo connector verify`). Org-scoped (any member may check
    # status); the credential is read from KMS, verified, and NEVER returned or logged.
    # A verification failure is reported as {active:false}, not an error — the console/
    # CLI renders it. Only apikey providers support verify (OAuth tokens are checked at
    # use, not re-verified here).
    post_provider_by_provider_verify(req: providerRef) returns (rep: verifyOut)
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
    post_provider_github_claim(req: githubClaimIn) returns (rep: githubClaimOut)
    # Forks a granted repository.
    # GitHub's fork is ASYNCHRONOUS: it answers 202 with the target repo and
    # populates it in the background, and it answers the same 202 when the fork
    # already exists. So this reports what GitHub said rather than waiting — a call
    # that blocked until the clone finished would time out on a large repository and
    # tell the caller nothing it does not already know.
    post_provider_github_fork(req: githubForkReq) returns (rep: githubForkOut)
    # Seeds the native todo with the EXISTING issues across the
    # org's granted repos (default state=open); the webhook keeps them live thereafter.
    # Org-scoped by the validated principal — a caller only ever backfills its OWN org.
    # Synchronous + bounded (a total time budget and an issue cap) so it returns the
    # counts directly; idempotent by ExtRef, so a re-run continues where a truncated
    # pass left off and never duplicates.
    post_provider_github_issues_backfill(req: githubBackfillIn) returns (rep: githubBackfillResult)
    # Creates the repo's Pages site and answers 201 Created with it.
    # With buildType "workflow" the site builds via GitHub Actions; otherwise it builds
    # from a branch source, defaulting to the repo's own default branch when none is
    # given. Only "/" and "/docs" are legal source paths (GitHub's rule).
    post_provider_github_repos_by_repo_pages(req: githubPagesEnableReq) returns (rep: githubPagesView)
    # Requests a Pages rebuild and returns the queued build's status.
    # The build is queued AT GITHUB, not completed here, so the answer is 202 Accepted
    # and its status is the one GitHub reported at queue time. 404 when the repository
    # has no Pages site, or when the org's installation was not granted it.
    post_provider_github_repos_by_repo_pages_builds(req: githubRepoRef) returns (rep: githubPagesBuildOut)
    # Imports the selected (or all) granted repos into git.hanzo.ai. The
    # selection is intersected with the installation's GRANTED set, so a client can
    # never import a repo the App was not granted (org isolation + a grant check). The
    # import runs in a bounded background worker (don't block the request), so the
    # answer is 202 Accepted; poll GET /v1/git/repos for each repository to appear
    # on git.hanzo.ai.
    post_provider_github_repos_import(req: githubImportIn) returns (rep: githubImportOut)
    # Finds repositories on GitHub.
    # This reads the PUBLIC index and returns nothing an installation unlocks: it is
    # how you find a repository to fork, not a way to see inside one. The org's own
    # token is used only so the query is rate-limited against the installation
    # rather than anonymously — the results are the same ones anyone would get.
    post_provider_github_search(req: githubSearchReq) returns (rep: githubSearchOut)
    # Finishes connecting the caller's GitHub account: it takes
    # the authorization the callback parked, trades it for the person's token, and
    # seals it. Only the person who started the flow can complete it — another
    # person's grant id is simply not found — and a grant completes once.
    post_provider_github_user_complete(req: githubUserCompleteIn) returns (rep: githubUserOut)
    # Begins connecting the caller's own GitHub account: it answers
    # the App's authorization page, carrying a state signed for this org and this
    # person. Every member may connect their own; it grants nothing the person does
    # not already hold on GitHub. The page shows GitHub's account picker (githubPick),
    # so a connect after a Disconnect can choose another account: GitHub otherwise
    # skips the page for anyone who authorized the App before.
    post_provider_github_user_connect(req: githubUserConnectIn) returns (rep: githubUserConnectOut)
    # Forgets the caller's GitHub connection in this org: the
    # token is revoked at GitHub, and the sealed secrets and the row go. It says
    # whether GitHub confirmed the revocation rather than reporting a success it did
    # not see. Disconnecting what is not connected succeeds.
    post_provider_github_user_disconnect() returns (rep: githubUserDisconnectOut)
    # Finishes connecting the caller's Google account: it takes
    # the authorization the callback parked, trades it for the person's tokens, and
    # seals them. Only the person who started the flow can complete it — another
    # person's grant id is simply not found — and a grant completes once. A grant
    # that leaves out sending mail, or offline access, is refused and nothing is
    # kept: a connection that cannot send is not one this surface offers.
    post_provider_google_user_complete(req: googleUserCompleteIn) returns (rep: googleUserOut)
    # Begins connecting the caller's own Google account: it answers
    # Google's consent page, asking offline access to send mail as them, carrying a
    # state signed for this org and this person. Every member may connect their own;
    # it grants nothing the person does not already hold at Google.
    post_provider_google_user_connect(req: googleUserConnectIn) returns (rep: googleUserConnectOut)
    # Forgets the caller's Google connection in this org: the
    # grant is revoked at Google, and the sealed secrets and the row go. It says
    # whether Google confirmed the revocation rather than reporting a success it did
    # not see. Disconnecting what is not connected succeeds.
    post_provider_google_user_disconnect() returns (rep: googleUserDisconnectOut)
    # Binds the caller's Linear organization to the org and seals the
    # webhook secret. The organization is READ from the caller's own key, never taken
    # from the body: a person can only bind an organization they are a member of. An
    # organization another org already holds is refused.
    post_provider_linear_claim(req: linearClaimIn) returns (rep: linearClaimOut)
    # Posts a comment on a Linear issue with the caller's own key, so it
    # carries their name. This is the op an agent is offered when it should answer in
    # Linear rather than in chat.
    post_provider_linear_comments(req: linearCommentIn) returns (rep: linearCommentOut)
    # Seeds the native todo with the EXISTING Linear issues the
    # caller's key can see (default state=open); the webhook keeps them live
    # thereafter. Synchronous and bounded, idempotent by ExtRef.
    post_provider_linear_issues_backfill(req: linearBackfillIn) returns (rep: linearBackfillResult)
    # Joins every public channel in the caller org's workspace.
    # Org admin, because it changes what the whole workspace sees: after it the agent
    # is a member of every public room and answers in all of them.
    post_provider_slack_join() returns (rep: slackJoinOut)
    # Posts as Hanzo to the caller's connected Slack workspace.
    # Use channel from a message read and thread_ts to reply to a specific message.
    # Requires chat:write; this operation does not join channels automatically.
    post_provider_slack_messages(req: slackSendMessageIn) returns (rep: slackSendMessageOut)
    # Adds an emoji reaction: POST /v1/provider/slack/reactions.
    post_provider_slack_reactions(req: slackReactIn) returns (rep: slackReactOut)
    # Answers a workspace question: POST /v1/provider/slack/search.
    # Slack's search.messages wants a USER token for a person's own view of a
    # workspace; a bot token searches what the app can see. Whichever the install
    # granted, the token custodied for this org is the one sent.
    post_provider_slack_search(req: slackSearchIn) returns (rep: slackSearchOut)
    # Mints a short, single-use deep-link code bound to the caller's
    # org and returns the t.me link the console navigates to. Org-authed: a caller with
    # no validated principal is 403 (same gate as the framework connect). The code is
    # stored as an oauth_nonce (org,telegram); the webhook's /start handler claims it to
    # bind chat→org. It is short (128-bit hex) so it fits Telegram's 64-char `start`
    # payload limit.
    post_provider_telegram_connect() returns (rep: authorizeOut)
    # Creates a group on the org's WhatsApp number and returns
    # its invite link.
    # Org admin, because the group speaks as the org's number and the invite link
    # admits anyone who holds it.
    post_provider_whatsapp_groups(req: whatsappGroupIn) returns (rep: whatsappGroupOut)
    # Sets or clears the custom domain (cname) and updates HTTPS
    # enforcement, build type, or source. ONLY the provided fields are sent to GitHub,
    # so an update never resets a setting the caller did not mention.
    put_provider_github_repos_by_repo_pages(req: githubPagesUpdateReq) returns (rep: githubPagesUpdatedOut)
    # Rewrites one of this app's messages:
    # PUT /v1/provider/slack/messages. Slack refuses a message the token did not
    # post, and that refusal is reported as it arrives.
    put_provider_slack_messages(req: slackUpdateMessageIn) returns (rep: slackUpdateMessageOut)
}

# ---------------------------------------------------------------------
# 48 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   post_provider_by_provider_connect  connectIn.Fields  map[string]string  (map)
#   post_provider_by_provider_run  runIn.Input  map[string]interface {}  (map)
#   post_provider_by_provider_run  runOut.Output  interface {}  (any)
#
# opaque (22) — crosses, arrives without its name:
#   connectionsOut.Connections  provider.connectionView (list element)
#   credentialIn.OAuth  provider.oauthBundleIn
#   credentialOut.Connection  provider.connectionView
#   devicePollOut.Connection  provider.connectionView
#   githubBranchesOut.Branches  provider.githubBranch (list element)
#   githubInstallationsOut.Installations  provider.githubInstallationView (list element)
#   githubPagesView.Source  provider.githubPagesSource
#   githubReposOut.Repos  provider.githubRepoItem (list element)
#   githubSearchOut.Repos  provider.githubSearchHit (list element)
#   gitlabProjectsOut.Projects  provider.gitlabProjectView (list element)
#   listOut.Providers  provider.providerView (list element)
#   providerView.Accounts  provider.connectionView (list element)
#   providerView.Actions  provider.opView (list element)
#   providerView.Connection  provider.connectionView
#   providerView.Fields  provider.Field (list element)
#   providerView.Triggers  provider.opView (list element)
#   refreshOut.Connection  provider.connectionView
#   slackChannelsOut.Channels  provider.slackConversation (list element)
#   slackJoinOut.Failed  provider.joinFailure (list element)
#   slackMessagesOut.Messages  provider.slackMessage (list element)
#   slackSearchOut.Matches  provider.slackSearchHit (list element)
#   userCatalogOut.Providers  provider.providerView (list element)
