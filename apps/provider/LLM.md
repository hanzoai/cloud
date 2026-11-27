# connectors and channels

## Two nouns, and they are not the same noun

```
connector   the LOGIC to connect a third party — how to OAuth Slack, how to talk GitHub
channel     a CONNECTED INSTANCE you talk through — your Slack workspace, your GitHub org
```

Adapter versus opened instance. One connector opens many connections; a connection
without its connector is a row nobody can use.

This is why `/v1/integrations/connectors` and `/v1/connection` are two surfaces rather than one:

```
/v1/integrations/connectors   what CAN I connect?     the adapter registry — code, ~69 providers
/v1/connection   what HAVE I connected?  data, owned by an org or by a person
```

An earlier pass here tried to fold them into one endpoint and hit a wall: a test
wanted `fake:work` and `fake:default` and the merged catalog had flattened them
into a single card. Those are two connections through one connector, and a shape
that cannot say that is the wrong shape.

## Scope belongs to the channel, not the connector

A person connects Google for themselves (their Drive) or for the org (a shared
Drive). The connector only declares which of those it permits — a GitHub App
installation is inherently org-wide, a personal API key inherently personal,
Google is genuinely both.

One table, and the unification needs no new column: `user = ''` means the org
owns it.

```sql
CREATE TABLE connections (
  org           TEXT NOT NULL,
  user          TEXT NOT NULL DEFAULT '',  -- '' = the org's; else the person's
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',  -- several accounts of one provider
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  expires_at    INTEGER NOT NULL DEFAULT 0,
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, user, provider, label)
);
```

`owner` became `label`: both existed to tell several accounts of one provider
apart, under two names. The rows are channels — renaming `Connection` → `Channel`
is mechanical and still owed.

**The org of a channel comes from the validated principal, never the body.** A
caller that can name its own org can read another tenant's credential. Every
security finding in this repo this week had that one shape: a credential
authenticating more than its design described, because an identity reached a
general resolver instead of the one handler that needed it.

## Where the chat path stands

`bridge.go` is a second implementation of channels living on the wrong side of
the line. Every adapter does **both**: `emitIngress` to the channels inbox, and
`bridgeSpawn`/`runBridgeTurn` to answer inline. Two mechanisms, one message,
four times over.

**Fixed:** the inbox client. It crossed on a package global —
`integrations.RegisterIngress`, a consumer pointer channels installed at Mount —
and a package global is per-process. `integrations`, `channels` and `agents` are
three separate PIDs in one writer pod, so that pointer was nil on the emitting
side and every event returned at the nil check. The inbox took nothing; the
pairing and allowlist gates never ran on real traffic. It now crosses as
`plane.ChannelsIngest`, a typed op.

**Still owed:** the turn itself. `bridge.go` should not exist — channels already
carries the ingress, the policy gate, the inbox and all four egress endpoints
(`slackEndpoint`/`teamsEndpoint`/`discordEndpoint`/`telegramEndpoint`), and its own code names
the gap: *"Agent delivery is NOT built this pass."* Moving it means:

- `apps/channels/turn.go` takes the bounded per-org pool, `runBridgeTurn`,
  `bridgeReply`, the agent ref and the concurrency knobs
- the adapters keep auth + parse + emit, and drop `bridgeSpawn`
- integrations keeps what is genuinely its own: token custody, `OrgForExternalID`
  (the isolation root), the account link, and the send endpoints
- one thing to design, not skip: adapters acquire a pool slot **synchronously**
  today so a capacity shed returns a retriable non-2xx *without burning the
  platform's event id*. Once the pool lives in channels, the client has to answer
  taken/refused — `ChannelsIngestOut.Taken` exists for this — and emitting stops
  being fire-and-forget.

## Doing the connector half

No migration, no alias, no compatibility window — there are no live connections
to preserve.

1. `Provider.Scope string` → `Provider.Scopes []string`. A connect naming a scope
   the provider does not permit is a 400, not a silent coercion.
2. One `list`, one `get`, one `connect`, one `disconnect`, one `verify`. GitHub's
   product routes (`installations`, `claim`, `repos`, `pages`) are not generic
   connector verbs and stay their own surface.
3. `/v1/ai/connections` folds in as category `AI`; `anthropic` is registered
   twice today.
4. The console is the only caller of the old paths.
5. Every OAuth redirect URI moves from `/v1/integrations/<p>/callback` to
   `/v1/integrations/connectors/<p>/callback`, so each provider's app registration needs
   updating — Slack, GitHub, Cloudflare — before it ships.

## The tests that make it true

- a user-scoped channel is INVISIBLE to another user in the same org
- an org-scoped channel is visible to every member
- one provider connected at both scopes yields two rows, and neither shadows the
  other
- disconnecting a scope leaves the other alone — the org disconnecting Google
  must not sign a person out of their own
- a connect naming a scope the provider does not permit is refused

`scope_test.go` pins the first four. What is not yet pinned is the endpoint.

**And a test can pass while the thing it names is dead.** The three tests that
guarded the ingress client registered a consumer in the same process and asserted
delivery. They were green on every run for as long as production dropped every
event. A test that only ever builds the co-resident case says nothing about the
deployed one.

## Pieces: the generated connectors

Every ActivePieces piece (the tree hanzoai/auto was forked from) is a connector
here, generated, never hand-listed:

```
go run ./cmd/piece -src <activepieces checkout> -rev <repo@commit> [-logos]
  -> piece/catalog/catalog.json   every piece's declaration (auth, actions, triggers)
  -> piece/catalog/logo/          96px PNG or script-free SVG, served at /v1/provider/{id}/logo
  -> piece/catalog/program/       bundled JS of every piece that needs only the framework shim
```

- **Data is injected, not imported.** 18 app binaries link this package; only
  `plugin/provider` and `plugin/auto` link `piece/catalog` and hand it over with
  `UsePieces` from their composition root. Everyone else carries none of it.
- **Native wins an id.** slack, github, stripe… keep their own flow; the card gains
  the piece's logo, actions and triggers.
- **OAuth keys on the identity host.** A piece signing in at accounts.google.com
  reads `GOOGLE_CLIENT_ID/SECRET`; login.microsoftonline.com reads `MICROSOFT_*`.
  One registered app lights up every piece of that host. Until it is set the card
  offers the credential the piece also takes, or says `app: GOOGLE`.
- **Credentials** (secret_text, basic_auth, custom_auth) are sealed as one KMS
  secret `auth` holding the value the piece reads (`{type, secret_text|username|props}`).
  A piece that declares `validate()` and runs here is checked live first (`checks:true`).
- **Runs happen here and only here.** `POST /v1/provider/{id}/run` and the
  `integrations_run` plane op (what a flow step in the auto process calls) read the
  org's credential from KMS and run the program in goja. No credential crosses a
  process boundary.
- **Not connectable here** (`kind:none`, `note`): OIDC, client-credentials, and
  OAuth endpoints templated on a field with no default (Zoho `{location}`).

## A GitHub push, once verified, is told to three apps

`github_webhook.go` verifies the signature and takes the org from the signed
installation id, then, for every ref but a delete: the build trigger
(`cloud.OnGitPush`, platform; human pushes only), git (`tellGit` →
`git_landed`: repositories linked to that GitHub repository read the push and
index a default-branch push; every pusher, since indexing pushes nothing), and,
after the installation token is minted, the sync engine (`cloud.Sync`). The first
two run before the mint so a mint failure loses neither. Proof:
`TestWebhookOrgIsolationAndFailClosed`, `TestAPushTellsGitWhatLanded`.

## A pull request is judged on the forge, and the verdict is a GitHub status (#453)

GitHub runs none of cloud's workflows; the forge runs them for a pull request it
holds. `gate.go`: a GitHub `pull_request` event or a forge `push` of the branch has
the forge hold the same proposal (`gateOpen`); every forge job delivery
(`forge_webhook.go`) writes the status `hanzo/ci` to the commit as the App, linked to
its run (`gateJob`). Green is proven (`judge`); a fork is never held on the forge and
gets an error status; the status is not a required check. Proof: `TestJudge`,
`TestAJobOfAPullRequestWritesItsVerdictToTheHead`, `TestTheForgeHoldsThePullRequestItWillRun`.

## A GitHub connect starts at the authorization page, and offers what is installed

GitHub's install page cannot connect an account the App is already on: it offers
Configure there, and saving it returns nothing (no state, no code), so the
person was left on GitHub with the popup open. With the App's OAuth client set,
`githubAuthorize` sends the org connect to `login/oauth/authorize` for the org
callback instead, which always returns a code and the state. The callback's
`finishInstalled` (`Provider.Installed`, `githubInstalled`) finds each
installation the person controls (their own account, or an organization where
they administer every repository it grants, and it grants one) that no org holds,
and OFFERS them on the chooser page (choose.go) rather than binding them: the
person connects one, or goes on to `InstallPage` to put the App on another
account. Binding all of them at once made a reconnect after a Disconnect come
back as the old account with no way to choose. An account another org holds is
never offered, and `bind` still refuses one taken meanwhile. It is SHOWN, though:
on the chooser, unnamed ("In another Hanzo workspace"), with no Connect and a link
to its installation settings, where uninstalling it releases it from every org
(`release`, on GitHub's uninstall webhook). The page sends the person to
disconnect it in that workspace first, and says uninstalling also disconnects
it there: a shared organization's account is another team's GitHub. Sending such a person to `InstallPage`
stranded them on Configure, and a workspace they had left could hold their own
account. With nothing to offer or show, the same browser goes on to
`InstallPage` under a fresh state, and that install's return binds as before, so
Sync's "install on another account" still reaches the install page. Proof:
`github_adopt_test.go`.

The chooser is the callback's own answer (text/html), so it works in Quick
setup's popup and in a tab alike. It carries no session: its forms post
`state` plus `account` or `install` to `POST /v1/provider/:provider/choose`, and
the token is signed like the callback's state with the offered accounts under
the MAC (`statePayload.Offered`, `statePayload.Chooser`) and a nonce of its own.
The accounts shown held elsewhere are not in the token. So a page answers once,
only with an account it offered, and the callback refuses the chooser's token as a
state (and the chooser a state) without spending it, even one that offers nothing. Each account links to its
installation's settings on GitHub (`Provider.Manage`, `githubManage`), where the
person changes which repositories the App reaches. The page is `no-store`,
unframeable and loads nothing; it has no `form-action`, because browsers hold a
form's redirects to it and the answer redirects to GitHub or the page that asked.

GitHub skips the authorization page for anyone who authorized the App before,
so a connect after a Disconnect could only come back as the same account. Both
starts (the org's `githubAuthorize` and the person's `githubUserConnect`) ask for
GitHub's account picker (`prompt=select_account`, `githubPick`), so the person
chooses which GitHub account connects every time. Proof: `TestGitHubAuthorizeURL`,
`github_adopt_test.go`, and `stateOf` in `github_user_test.go`.

## The platform asks GitHub for public facts, and no token leaves (#456)
`integrations_github_figures` (github_figures.go, plane only) answers what GitHub says of up
to 100 public repositories named `owner/name`: counts and the root manifests. It reads with the
first installation the App holds (any installation's token reads public data anywhere) and
returns facts, never a credential, so the estate's collector in `apps/git` holds no GitHub
secret. Only the platform asks (`cloud.Who(ctx).Org == authz.AdminOrg`); names ride as GraphQL
variables, a private or missing repository is listed missing and nothing of it is read, and any
error that is not one repository's is a 502, never an empty answer.

## Social networks: thirteen, one framework

The social plane (`apps/social`) lists accounts and schedules posts; this package
holds each account's credential and makes the post (`planePost`, plane op
`integrations_post`). A network's id in the social vocabulary (`providerOrder`) IS its
connector's id and its poster's key in `posters`. One file per network:
`post_<network>.go` for the OAuth ones (consent, `expand*`, poster), and a
connector file for the credential ones.

How a person adds an account, and what it takes:

| Network | Connect | Owner must hold | Review before it posts to the public | Post |
|---|---|---|---|---|
| X | OAuth 2.0 + PKCE (`x.go`) | X developer app, OAuth 2.0 confidential; callback `https://api.hanzo.ai/v1/provider/x/callback`; scopes `tweet.read tweet.write users.read offline.access` | Paid API access for write volume | text; media needs `media.write` and an upload, not built |
| LinkedIn | OAuth; member and each company the member administers | LinkedIn app with Sign In (OpenID), Share on LinkedIn (`w_member_social`, self-serve) | Community Management API (`w_organization_social`, `r_organization_social`) for company pages | text; media not built |
| Facebook Pages | OAuth through Meta; each Page keeps its own token | Meta app, Facebook Login | App Review: `pages_manage_posts`, `pages_read_engagement`, `pages_show_list`; Business Verification | text, photos, video |
| Instagram | Same Meta app; each Page's linked professional account | Meta app (shared with Facebook) | App Review: `instagram_basic`, `instagram_content_publish`, `pages_show_list`, `pages_read_engagement` | photo, video (Reel), carousel to 10 |
| Threads | OAuth; the hour token is traded for a 60-day one that renews itself | Meta app with the Threads use case | App Review: `threads_basic`, `threads_content_publish` | text, photo, video, carousel |
| TikTok | OAuth (`client_key`); token renews | TikTok app: Login Kit + Content Posting API; the media host's URL prefix verified in the portal | Content Posting audit; until then every post is private (`SELF_ONLY`), which `postTikTok` chooses on its own | video, photos |
| YouTube | OAuth (Google); token renews | Google OAuth client with YouTube Data API v3 | Scope verification; API compliance audit, else uploads are locked private; 1600 quota units an upload | one video, resumable upload |
| Pinterest | OAuth; each board is an account | Pinterest app: `boards:read pins:read pins:write user_accounts:read` | Trial access to Standard access | photo, or 2 to 5 as a carousel; no video |
| Reddit | OAuth; the profile and each community the member moderates | Reddit app at reddit.com/prefs/apps (web), `identity submit mysubreddits` | Reddit's Data API terms; commercial use is by approval | self post, or one link |
| Bluesky | app password (`bluesky.go`) | nothing | none | text with links and tags, 4 photos; no video |
| Mastodon | access token made on the person's own server (`mastodon.go`) | nothing | none | text, 4 photos or a video |
| Telegram | the org's own bot token and channel (`telegram_bot.go`, id `telegram-bot`) | nothing | none | text, photos, video, albums |
| Discord | a channel's webhook (`discord_webhook.go`, id `discord-webhook`) | nothing | none | text, photo embeds, video link |
| Hacker News | not offered | | | the site has no write API |

Not built: per-network analytics (the Analytics screen shows the delivery record only),
Bluesky and Mastodon OAuth (Bluesky's needs DPoP and a hosted client document;
Mastodon's registers an app per server), X and LinkedIn media, Discord's
webhook-by-consent.

Where an app's credentials go. The org's own app is sealed by `PUT /v1/social/credentials`
(the Keys page) at KMS `/orgs/<org>/social/apps/<network>/client_id` and `client_secret`;
it wins over the deployment's and needs no release. A deployment-wide app is env,
canonical name first: `<NETWORK>_CLIENT_ID` / `<NETWORK>_CLIENT_SECRET` (`internal/appcred`
lists the older names: `X_API_KEY`, `FACEBOOK_APP_ID`, `THREADS_APP_ID`). The credential
networks hold each account's secret at `/orgs/<org>/integrations/<network>/<key>` beside
the other connectors', and read nothing from the deployment.

Things that look like bugs and are the design:

- **A post goes to every connected account of its channel.** Each is tried whatever the
  others did; any success is `published`, and `error` then names the accounts that refused.
- **A hand-listed account is refused.** An account with no platform id has no consent
  behind it; posting through "the only connection" would post twice.
- **A refresh token is kept only where `spec.refreshes` says so.** The generic engine
  used to drop it, so YouTube, Reddit and TikTok tokens died in an hour or a day.
  Facebook and Instagram keep a Page token, which does not expire.
- **A transport failure never reports its address** (`callOn`): Threads and Telegram take
  the credential in it.
- **Media addresses are the tenant's.** What this process fetches (Bluesky, Mastodon,
  YouTube) goes through `hanzoai/outbound`; the rest is fetched by the network.
- **A post still processing at TikTok when the looking stops is posted**, not failed:
  asking again would make it twice.

## Linear: one connector, and the org half built on it

The Linear provider is the user-scoped API-key connector in
`apps/integrations/saas.go` — a person enrols their own key under
`/v1/integrations/connectors/linear`. There is deliberately no second, org-plane
Linear provider: the registry refuses a duplicate id, and the two custody planes
are disjoint by construction (`integrations.Use` asserts it at boot).

What the org needs from Linear is the webhook, and `linear.go` builds it on the
person's key rather than beside it:

- `POST /v1/integrations/linear/claim {secret}` reads `organization { id }` with
  the CALLER's key — so a person can bind only an organization they belong to —
  writes the org-plane row `(org, provider=linear, external_id=<organization>)`
  that `OrgForExternalID` resolves, and seals the webhook secret under
  `kmsPath(org,"linear")/webhook_secret`. An organization another org holds is
  refused by `store.write` (errBound → 409). Claiming again rotates the secret.
- `POST /v1/integrations/linear/webhook` (raw, public at the JWT layer) reads ONE
  field before trusting anything — `organizationId`, to pick which org's secret to
  verify with — then HMAC-verifies the raw bytes with that secret, refuses a
  delivery older than a minute (`webhookTimestamp`), mirrors an Issue into the
  native todo (`ExtRef linear:<identifier>`, `Repo` = team key, state folded to
  open/closed) and fires `linear` automation triggers (`issue.<action>`,
  `comment.<action>`). A remove is never propagated.
- `POST /v1/integrations/linear/comments` and `/issues/backfill` spend the caller's
  key, so a comment an agent posts carries the person's name.

The ledger moved by exactly these routes: `ops_projection_test.go` (3 typed + 1
raw), `typed_wire_test.go` (the raw webhook), `openapi/floor.json` (+4 paths/ops),
`openapi/untyped.json` (+1). No skill is generated for Linear and none should be:
`plugin/gen-skills` clusters on paths that carry a GET, and every Linear route is a
POST.

## The Slack bridge is a member, and its secret has two names

`routeSlackEvent` now takes a message on all four surfaces Slack can deliver —
`channel`, `group`, `mpim`, `im` — where it took only `im`, so @hanzo was deaf
in every channel it had been invited to unless someone spelled its name out.
Membership is the whole subscription: Slack delivers `message.channels` only for
channels the app has joined, so there is no channel list to keep and
`member_joined_channel` records the moment rather than enabling anything.
An @mention threads under itself (a summons leaves the channel as it found it);
every other message answers where the conversation already is — in its thread if
it has one, in the channel otherwise. `POST /v1/integrations/slack/join` (org admin)
walks `conversations.list` and `conversations.join`, skipping on `is_member`, so
a run stopped by Slack's rate limit is finished by running it again.

`apps/integrations/slack.manifest.json` is embedded and is the ONE home for the
bot scopes and the event subscriptions: the consent URL is built from it, so the
old "keep this list equal to the manifest" comment has nothing left to guard.
Adding a scope still needs the workspace to re-install — Slack never widens a
token it has already issued.

THE LIVE FAULT, unrelated to any of that: `slackSigningSecret` resolves
`SLACK_SIGNING_SECRET_REF` (a KMS *reference*, fetched through
`Base.SecretFromEnv`), and universe sets `SLACK_SIGNING_SECRET` (the *value*,
KMSSecret `cloud-slack-kms-sync` → secret `cloud-slack-secret`, path
`/team-go-secret`). `SLACK_SIGNING_SECRET_REF` appears nowhere in universe, so
the secret never resolves and `POST /v1/integrations/slack/events` answers
503 "slack events not configured" on api.hanzo.ai today — the whole bridge, DMs
included, is off. The fix is in universe, not here: publish the ref name.

## WhatsApp groups and inbound media

A WhatsApp delivery is a direct message unless it names a `group_id`, on the
message or on `context`. The channel id is then the group, and a reply is
`recipient_type: "group"` with that id as `to`. A person's number stays
`individual`. The two ids are both opaque, so the flag is the only discriminator.

Inbound image, document, audio, video and sticker messages carry a media id.
The bytes are two GETs with the org's bearer: `GET /v21.0/{id}` returns a URL
that expires in about five minutes, then that URL is fetched with the same
bearer. The URL is spent here and never logged. The agent reads a `slackFile`
(`id`, `name`, `mimetype`, `size`) on the turn text — the attachment a Slack
message already carries — plus the document when it is text and a small image
as a data URL. The envelope attachment's URL is `whatsapp-media:<id>`, not the
Graph address.

`group_lifecycle_update`, `group_participants_update`, `group_settings_update`
and `group_status_update` are signed like messages, logged as the field and the
group id, and answered 200. They are not turns. `POST|GET /v1/provider/whatsapp/groups`
creates a group and reads its invite link, org admin, the same gate as joining
a Slack workspace. An unlinked person's connect URL is `https://wa.me/<number>`.
