# Slack channel reads and replies

The Hanzo agent uses the cloud MCP surface at `/v1/mcp`. Slack reads and sends
run in the provider process, which holds the connected organization's bot token.
The caller's authenticated organization selects the token; tool arguments cannot
select another organization's credential.

## Slack app settings

For “check the latest messages in #hanzo-gtm”, a **public channel** needs bot
scopes `channels:read`, `channels:history`, and `chat:write` for a reply.
Invite Hanzo to the channel: `/invite @Hanzo`.

Additional conversation types need:

| Conversation | Discover | Read history |
| --- | --- | --- |
| Private channel | `groups:read` | `groups:history` |
| Group direct message | `mpim:read` | `mpim:history` |
| Direct message | `im:read` | `im:history` |

`channels:join` is needed for the existing public-channel join operation, but
manual invitation is enough for reading a specific channel. Message reads never
join a channel automatically. Hanzo must belong to private conversations too.

The embedded `slack.manifest.json` declares these bot scopes. After changing
scopes, **reconnect Slack through Hanzo's OAuth connect flow** so Slack grants
them and Hanzo stores the resulting token. Editing the scope list alone does not
upgrade an installed token. Check deployment overrides in `SLACK_BOT_SCOPES` if
the OAuth consent screen omits a scope.

User Token Scopes do not grant permissions to the bot token this implementation
uses. The `mcp:connect` bot scope connects **Slackbot to Hanzo's MCP server**;
it does not grant access to Slack channel messages. Preserve any existing MCP
server and user-scope configuration when updating the Slack app settings.

For the current checkout, configure:

- OAuth redirects: `https://api.hanzo.ai/v1/provider/slack/callback` and
  `https://api.hanzo.ai/v1/provider/slack/link/slack`.
- Event Subscriptions and Interactivity:
  `https://api.hanzo.ai/v1/provider/slack/events`.
- `/hanzo` command: `https://api.hanzo.ai/v1/provider/slack/commands`.

Deploy the matching server routes before switching these URLs. Configure the
events declared in the manifest when Hanzo should respond to incoming Slack
messages. On-demand history reads do not require message event subscriptions.

## When Hanzo answers

In a direct message with Hanzo, every message is answered. In a channel, a
private channel or a group DM, Hanzo answers only a message that @mentions it,
or a reply in a thread it answered in or was @mentioned in (for 30 days after the
last answer or mention). Typing its name without the mention does not summon it.

A person whose Slack account is not linked to a Hanzo account gets a private
reply that says so and carries their own connect link. The link's hanzo.id leg
returns to `https://api.hanzo.ai/v1/integrations/slack/link/callback`, the
address the `hanzo-slack` IAM client registers.

## Install and uninstall

An org admin installs Slack from the console (`POST /v1/provider/slack/connect`);
a plain member is refused, because the workspace's turns bill the org. One
workspace belongs to one org: an install on a workspace another org holds fails
with "this account is connected to another organization" and changes nothing
in the installing org.

Removing the app in Slack is the uninstall. Slack sends `app_uninstalled` and
`tokens_revoked`, in either order; either one unbinds the workspace, deletes its
token, and frees it to install into another org. Both events are in the
manifest's `bot_events`, and the app config must subscribe to them.

## Operations

These typed routes also publish MCP operations:

| Route | Operation ID | Purpose |
| --- | --- | --- |
| `GET /v1/provider/slack/channels` | `get_provider_slack_channels` | List conversations, with `types` and `cursor` |
| `GET /v1/provider/slack/messages` | `get_provider_slack_messages` | Read a channel or thread |
| `POST /v1/provider/slack/messages` | `post_provider_slack_messages` | Post as Hanzo, optionally in a thread |

The fleet groups operations under their owning subsystem. Discover its published
operation names with MCP rather than assuming the subsystem name across renames.

`GET /v1/provider/slack/file?id=<file id>` reads the content of a file attached
to a message. It is not in the table above because it publishes no MCP operation:
its response is the file's bytes rather than a JSON shape. Take the id from a
message's `files`. The bytes are fetched with the org's own bot token and require
`files:read`; Slack's private download URL is spent server-side and never
returned. A file the workspace's bot cannot see is a 404.

Read arguments:

```json
{"channel":"#hanzo-gtm","limit":15}
```

The response includes the resolved channel ID, exact message timestamps,
`reply_count`, `has_more`, and `next_cursor`. History is newest first. Fetch
thread replies separately using `thread_ts` from a parent message's `ts`; thread
results are oldest first. Continue using `cursor` when another page is needed.
Reuse the resolved channel ID to avoid repeating the channel-name lookup.

Reply arguments (when the user requests a post):

```json
{"channel":"C123EXAMPLE","thread_ts":"1710000000.000001","text":"Here is the summary…"}
```

The server defaults to 15 messages per page. Slack can return fewer, and its
limits depend on app distribution; preserve pagination and surface rate-limit
errors instead of treating a partial response as the entire conversation.

## Verification

Run `GOWORK=off CGO_ENABLED=0 go test ./apps/provider`. Tests use Slack stubs and
real local token custody, including actual MCP `tools/call` reads and replies.
They do not send messages to a real workspace.

Before deployment, regenerate provider prose, the provider OpenAPI subset, the
composed contract, and `client/catalog.json`. The fleet MCP catalog must contain
the new operations. If a provider rename is in progress, resolve its entrypoint
and manifest name before building; do not publish a catalog from inconsistent
app names.

Slack references: [history](https://docs.slack.dev/reference/methods/conversations.history/),
[conversation listing](https://docs.slack.dev/reference/methods/conversations.list/),
[threads](https://docs.slack.dev/reference/methods/conversations.replies/), and
[Slackbot MCP client](https://docs.slack.dev/ai/slackbot-mcp-client/).
