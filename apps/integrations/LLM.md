# connectors — one way to connect a third party

## What this should be

**One surface.** `/v1/connectors`. A caller asks for connectors; the server
returns the ones that principal may see, each carrying whether it is connected
and who owns the credential.

**Scope belongs to the CONNECTION, not the provider.** A person connects Google
for themselves (their Drive) or for the org (a shared Drive). The provider only
declares which of those it permits — a GitHub App installation is inherently
org-wide, a personal API key inherently personal, and Google is genuinely both.

```
Provider.Scopes  []string   // what this connector permits: ["org"], ["user"], or both
POST /v1/connectors/google/connect {"scope":"user"}   // my Drive
POST /v1/connectors/google/connect {"scope":"org"}    // the org's Drive
```

**One table**, and the unification needs no new column — `user = ''` means the
org owns it. That convention already exists here: `owner = ''` in the old
`connections` table meant "one owner". The same idea, stated once.

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

`owner` becomes `label`: both existed to tell several accounts of one provider
apart, under two names.

## What it is today, and why

Two doors over ONE registry of 69 providers, split by a `Provider.Scope` field:

- `/v1/integrations` — `list` skips `Scope == userScope`; org rows keyed
  `(org, provider, owner)`
- `/v1/connectors` — the per-user plane; rows keyed `(org, user, provider, label)`

The code says "the two planes are disjoint", and that is the sentence to delete.
Disjointness is why "connect Google for my org or just me" cannot be expressed:
the provider decides the plane, so Google is org-only forever.

A third surface, `/v1/ai/connections` (openai · anthropic · google), is the same
idea a third time — and `anthropic` is registered in BOTH it and the connector
registry today. It folds in as category `AI`.

## Doing it

No migration, no alias, no compatibility window — there are no live connections
to preserve, and `widenConnectionKey` (an in-place rebuild from an older key) is
the accretion this replaces. Delete it with the rest.

1. One table above; drop `connectors` and `connections`, and `widenConnectionKey`.
2. `Provider.Scope string` → `Provider.Scopes []string`. A connect naming a scope
   the provider does not permit is a 400, not a silent coercion.
3. Fold the handlers: one `list`, one `get`, one `connect`, one `disconnect`, one
   `verify`. GitHub's product routes (`installations`, `claim`, `repos`, `pages`)
   are not generic connector verbs and stay their own surface under
   `/v1/connectors/github/*`.
4. Delete `/v1/integrations` and its manifest prefix. Update the console in the
   same change — it is the only caller.
5. `/v1/ai/connections` folds in; remove the duplicate `anthropic` registration.

## The tests that make it true

- a user-scoped connection is INVISIBLE to another user in the same org
- an org-scoped connection is visible to every member
- a connect naming a scope the provider does not permit is refused
- one provider connected at both scopes yields two rows, and neither shadows the
  other
- the org of a connection comes from the validated principal, never the body —
  a caller that can name its own org can read another tenant's credential

That last one is not hypothetical. Every security finding in this repo this week
had the same shape: a credential that authenticated more than its design
described, because an identity was carried to a general resolver instead of to
the one handler that needed it.

## Where this stands

**Done and on main** (`87248144`): the store. One table keyed
`(org, user, provider, label)`, `Connector` folded into `Connection`, `owner`
became `label`, `widenConnectionKey` deleted. `List` answers what a principal may
use; `Delete` names one account, `Disconnect` takes every account at a scope.
Four tests pin Google-at-both-scopes, cross-user isolation, and that the org
disconnecting does not sign a person out of their own.

**Started, not landed** — the door fold. What was written and works:

- every `/v1/integrations` path renamed to `/v1/connectors`, and the retired
  prefix removed from `manifest.Apps` (one prefix, one owner)
- `o.list` stopped skipping user-scope providers, so one catalog covers both
- the second catalog (`o.connectors`, `o.connectorProviders`) deleted as dead
- `connView` gained `Scope` ("org" | "user"), derived from the key, not stored
- `providerView` gained `Connections []connView`, FILTERED to what the caller may
  see — `c.User == "" || c.User == user`. That filter is the tenancy boundary and
  is the line to review hardest.

**What is left, and it is small but real:**

1. `connectors_test.go` still asks the catalog for flat connection rows
   (`list.Connectors`). They now live under `providers[].connections`, so the
   test reads the wrong shape — update the test, not the response.
2. Three `github_bind_test.go` cases 404 after the rename. Unresolved: the
   callback path moved and something still points at the old one. Find the
   registration rather than guessing — a 404 here means a route, not a policy.
3. `/v1/ai/connections` still to fold in as category `AI`; `anthropic` is
   registered twice today.
4. The console is the only caller of the old paths.

**Operational, before this ships:** every OAuth redirect URI changes from
`/v1/integrations/<p>/callback` to `/v1/connectors/<p>/callback`, so each
provider's app registration needs updating — Slack, GitHub, Cloudflare.
