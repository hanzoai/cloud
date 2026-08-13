# Meet UI (embedded)

`dist/` is the built **Hanzo Meet SPA** — the `admin-meet` app (`@hanzo/meet`,
Vite + @hanzogui + LiveKit's own React components) — baked into the cloud binary
via `//go:embed` and served at `/meet/*` by `apps/meet`.

It is the native call client. The media server (our LiveKit at
`wss://live.hanzo.bot`) and the join-token mint (`POST /v1/meet/getToken`) were
already ours; this replaces the office plugin in the published Team front, which
was the last part of a Hanzo call that was not.

The SPA is built with `base: '/meet/'` and `VITE_API_PREFIX=/v1/meet`, so every
asset and every XHR is same-origin under paths cloud already serves
(`/meet/*` static, `/v1/meet/*` API). The client mints nothing and holds no
credential: the gateway validates the IAM JWT, `apps/meet` decides on what it
stamped.

## The flow

Two screens, and both entry paths — the lobby and a pasted link — go through the
second one, so there is one way to enter a call rather than two that disagree.

**Lobby** (`/meet`) — which workspace, which room. No camera: the preview belongs
to joining, not to choosing an address.

1. `GET /v1/meet/session` — who you would be seated as, where the media plane is,
   and which workspaces you may open a room in. The list is already narrowed to
   the roles the mint would grant, so the offer and the grant cannot drift.
2. It composes the room's address, `<workspace>_<slug>`, which is also the URL:
   `/meet/<workspace>/<slug>`. That prefix is the only thing binding a room to a
   tenant, and `apps/meet` reads it back on the mint.

**Room** (`/meet/<workspace>/<slug>`) — the join screen, then the call.

3. `GET /v1/meet/session` again (a pasted link has never seen the lobby), then
   LiveKit's `PreJoin`: camera preview, device pickers, your name.
4. `POST /v1/meet/getToken {roomName, participantName}` → the raw join token,
   minted on submit and spent immediately by the connect that follows.
5. The browser connects to LiveKit directly with it. No media passes through us.

The identity on the tile is the SERVER's (`sub` = the team account), never the
body's — LiveKit evicts a duplicate identity, so a caller-chosen one would let
anyone eject a colleague. `participantName` is a display label only.

## Regenerating dist/

Source of truth: the `meet` app in `hanzoai/admin`. Build it in a workspace where
`hanzogui@8.x` + `@hanzogui/admin` resolve, then sync its `dist/`:

```sh
cd apps/meet
bun install
VITE_BASE=/meet/ bun run build    # tsc --noEmit && vite build → dist/

rsync -a --delete apps/meet/dist/ <cloud>/apps/meet/ui/dist/
```

**VITE_BASE is not optional.** Upstream builds for the root that its own
standalone image owns, so the default is now `/`. This binary serves the bundle
under `StripPrefix("/meet", …)`, and a root-based build emits `/assets/` URLs
that nothing here answers — index.html is 200 and the page renders blank, with
no runtime signal at all. `embed_test.go` asserts the prefix for that reason,
and asserts the sign-in path with it: that one is a runtime string rather than
an asset URL, so `base` cannot rewrite it and it is derived from `BASE_URL` in
the app instead.

Then `go build ./plugin/meet` re-embeds it. Do NOT hand-edit files under `dist/` —
they are content-addressed Vite output. Keep `.sync-stamp` truthful (source repo
+ commit).
