# connectors and channels

## Two nouns, and they are not the same noun

```
connector   the LOGIC to connect a third party — how to OAuth Slack, how to talk GitHub
channel     a CONNECTED INSTANCE you talk through — your Slack workspace, your GitHub org
```

Adapter versus opened instance. One connector opens many channels; a channel
without its connector is a row nobody can use.

This is why `/v1/integrations/connectors` and `/v1/channels` are two surfaces rather than one:

```
/v1/integrations/connectors   what CAN I connect?     the adapter registry — code, ~69 providers
/v1/channels     what HAVE I connected?  data, owned by an org or by a person
```

An earlier pass here tried to fold them into one endpoint and hit a wall: a test
wanted `fake:work` and `fake:default` and the merged catalog had flattened them
into a single card. Those are two channels through one connector, and a shape
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
