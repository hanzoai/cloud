# Fleet residency

What a dormant agent costs to keep, measured rather than modelled.

```
node fleet.mjs /tmp/fleet          # writes 1,000,000 agents, measures disk and resume
node cost.mjs                      # turns that into a monthly bill at list prices
```

## Why this exists

Competitors publish a cost-per-million-agents row and mark it modelled. Naïve's
says so in its own footnote — *"MODELLED, NEVER BILLED · 100,000 TENANTS × 10
AGENTS"* — at an assumed ~1 MB of state per agent.

An assumption is a fine thing to publish if you label it, and they do. But we do
not have to assume: a dormant agent here is a row in the same `kv(key, value,
upd)` table the cloud already runs on, so the number can be taken instead.

## What it measured

On an M-series laptop, September 2026:

| | |
|---|---|
| agents | 1,000,000 |
| on disk | 454.6 MB — **477 bytes per agent** |
| write | 3.2 s, 310,270 agents/s |
| resume, in-process | 0.031 ms |
| resume, cold process | 3.57 ms (includes `sqlite3` spawn) |

477 bytes against an assumed 1 MB is a factor of 2,198. At S3 list that is
**$0.01 a month** of storage for the fleet.

## What it does not measure

Residency, and only residency. Nothing here says what a fleet costs while it
*works* — model tokens, tool calls, and the compute of a running turn dwarf all
of the above and fall on every architecture alike.

That is the same boundary the competing page draws around its own claim: *"The
advantage is dormancy, and only dormancy."* This measures exactly that much, so
the honest unit for a price list is **$/agent-hour-awake**, with a storage line
small enough to give away.

## Method

`fleet.mjs` writes agents in batches of 50,000 through `sqlite3` on stdin —
piped rather than passed, because a 50,000-row insert is 20 MB of SQL and argv
tops out well before that. The record is what the platform actually stores for a
dormant agent: identity, org, model, standing instruction, tool grants,
schedule, and a cursor into its history. History itself is not resident; a
conversation is not loaded when nobody is talking.

Resume is timed two ways because both are true: in-process is what the server
does, cold is what a fresh worker pays.
