# LLM.md — apps/transport

Guidance for apps/transport. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Channel and transport: one word each (HIP-0523 §2.1, HIP-1066)

A CHANNEL is the conversation a message lives in; a TRANSPORT is the network it
travels over. The wire says nothing else, and nothing forwards the old names:

| capability | served at | the conversation is named |
|---|---|---|
| team (apps/team) | `/v1/team/channels`, `/v1/team/dms` | `channel`; a list is `{channels:[…]}`, one is `{channel:{…}}`, kind `public` / `private` / `dm` |
| transport (apps/transport) | `/v1/transport`, `/v1/transport/{transport}/send` | inbox rows `{transport, channelId, channelKind}`, agent `{transport, default, channels}`, send body `{channel:{id}}` |
| agent sessions, task, meet | `/v1/agent/sessions?channel=`, `/v1/task/channels/{channel}`, `/v1/meet/call?space=&channel=` | `channel` |

Storage keeps its names (`channel_inbox`, `channel_agent.room_id`,
`agent_sessions.room`, `issues.room`, the `channels` and `public_rooms` sqlpool
files): they are never on the wire. The one stored value that moved is the team
work facet: `hanzo:mixin:Room` → `hanzo:mixin:Channel`, rewritten by
`renameFacet` (apps/team/store.go) the first time a process opens a space.
`/v1/meet/getToken`'s `roomName` and `/v1/meet/record`'s `room` are untouched:
they name the LiveKit media room, a different object. The plane ops moved with
the app: `transport_ingest|recent|agent|stop|act`, typed client `client/transport`.

## Transport: which agent answers is the org's row, not a deployment variable

## GitHub and Linear are transports: an issue is a channel

`apps/transport/{github,linear}.go` — an issue (or pull request) is the channel, its
comments the thread; capabilities are `thread` only, because a tracker has no DM
and no other surface. The integrations webhooks turn a created comment that
addresses the agent into the same `Inbound` Slack emits (`github_issues.go`
`githubMentionInbound`: `@<app slug>` as a whole word, never a Bot;
`linear_webhook.go` `linearMentionInbound`: `@hanzo`), so pairing/allowlist
policy, the inbox, the per-channel agent binding and the turn are the ONE path.
The reply is `planeChatSend` `github` (issue comment via the installation token)
or `linear` (comment via the claimer's key). Identity: a commenter who has
linked resolves as anywhere; otherwise `channelIdentity` falls back to the
org's DEFAULT subject — `getUserLink(org, provider, "*")`, written by
`linearClaim` for the claimer. GitHub writes no default (its claim is platform
sudo), so an unlinked GitHub commenter gets the link prompt; a GitHub link leg
is the follow-up.

## An issue asked for in a thread lives where its repository does, and the thread hears it

One tool, the board's own two writes: `POST /v1/task/projects/:key/issues` and
`PATCH …/issues/:num` (MCP `task` group). `apps/task/home.go` `where` resolves the
key: a forge repository read as the actor, filed on GITHUB through the App when
it is a pull mirror (`forge.Repo.Home`), or `owner/name` / `github.com/owner/name`
for a repository GitHub alone holds (`forge.GitHub`, admitted by
`integrations_github_repo`, the caller's own GitHub view). The GitHub write is
`integrations_github_issue` (`apps/provider/github_issue.go`), the one door: App
token, audited as the person, then `mirrorGitHubIssue` with `Channel` so the board
row is bound to the thread. `Channel` is `client.Reply.Address()` —
`slack:<channel>/<thread>`, bound ONCE by the upsert (the create and the
`opened` webhook race; whichever names a channel wins, `""` never unbinds).
`apps/agents/replyto.go` `address` writes the turn's thread (a top-level message
is its own thread root) into both board writes, no `Turn`, over anything the model
put. Events back: `github_thread.go` `follow` — comments, close/reopen,
(un)assign, and `pull_request` opened/merged whose text closes the issue — one
escaped line in the bound thread; the App's own writes (`self`: `<slug>[bot]`) are
never said again. `assignee: "me"` = IAM's `User.GitHub` via `iam_linked` →
`GET /user/<id>` (forge: the Sudo actor). THE BOT TRIGGER IS THE `agent` LABEL
(Apps cannot be assignees): `assignee: "agent"` labels it and starts the run from
the op as the caller; a person labelling on GitHub starts it from the webhook as
their linked subject (`channelIdentity`). Both call `agents_coding_start` — the
same `startCoding` as `POST /v1/agent/coding` — with `client.Work` (title+body,
`Issue`, thread); the PR says `Closes #N`, the run files no AGT card, and a run
with no thread narrates in `CODING_ISSUE_CHANNEL` (`org:channel,…`). Forge-native
issues are filed, assigned and handed off, but the forge sends no issue events
(its system webhook triggers on workflow jobs only), so their threads hear nothing.

## Plane ops behind a chat turn: transport_agent, iam_federated

Two lookups that a bridge outside the transport turn needs are on the plane, not
env or a cross-app store read. `transport_agent` (`plane.TransportAgent`,
`AgentForIn{Transport, Channel}` → `AgentFor{Ref}`) answers which agent a channel runs —
the same `agentFor` the turn uses, org from the plane context — so Slack slash
commands in integrations resolve exactly what a message would (`agentRefFor`).
`iam_federated` (`plane.IAMFederated`, `FederatedIn{Provider, Subject}` →
`Federated{User}`) resolves an org member from the identity a provider issued;
GitHub only today, matching IAM's `User.GitHub` (numeric id, written at
federation) against the webhook's `comment.user.id`. `channelIdentity` asks it
for provider `github`, caches the answer with `putUserLink`, and only then falls
to the org's default subject or the link prompt. Both are org-bounded by the
caller's context; neither takes an org argument.
