# Tracker UI (embedded)

`dist/` is the built **Hanzo Tracker SPA** — the `tracker` app in `hanzoai/admin`
(`@hanzo/tracker`, Vite + a `@hanzogui` shell over the forge's own board CSS) —
baked into the cloud binary via `//go:embed` and served at `/tracker/*` by
`apps/tracker`. This is the real UI behind `tracker.hanzo.ai`, and it is what
retires the Huly tracker that used to answer that host.

The SPA is built with `base: '/tracker/'` and `VITE_API_PREFIX=/v1/tracker`, so
every asset and XHR is same-origin under paths this binary already serves
(`/tracker/*` static, `/v1/tracker/*` API).

## Identity, and the two things the page does carry

Sign-in is **hanzo.id** — the one identity provider — via OIDC/PKCE through
`@hanzo/iam`, driven by `AuthGate`. The bundle contains no form and no password
field; it asks IAM to authenticate somebody and holds the bearer that comes back.
It deliberately does NOT sign in against `<origin>/v1/iam`, which would be
whatever IAM the serving host embeds — a second identity authority, and on a
deployment whose embedded store is not the one holding the accounts, a login
screen that never accepts anyone.

So the page carries two things, and neither is tenancy the client invented:

- `Authorization: Bearer …` — the token IAM issued.
- `X-Org-Id` — the caller's org SELECTION. cloud deletes this header on ingress
  and re-mints it, honouring the value only when the validated token's signed
  `orgs` claim contains it, so sending it can never widen access. The switcher
  lists that same claim, which is what stops it offering a selection the server
  will silently ignore (a rejected selection is not a 403 — it is the home org's
  rows under another org's name).

## Regenerating dist/

Source of truth: the `tracker` app in `hanzoai/admin` (`apps/tracker`, package
`@hanzo/tracker`). Build it where `hanzogui@7.x` and `@hanzogui/admin` both
resolve, then sync its `dist/`:

```sh
cd apps/tracker
../../node_modules/.bin/tsc --noEmit     # typecheck gate
../../node_modules/.bin/vitest run       # the IAM-endpoint pins
../../node_modules/.bin/vite build       # → dist/  (base=/tracker/, api=/v1/tracker)
../../node_modules/.bin/playwright test  # sign-in, chrome, org switch — against dist/

rsync -a --delete --exclude='.sync-stamp' apps/tracker/dist/ <cloud>/apps/tracker/ui/dist/
```

Then `go build ./plugin/tracker` re-embeds it. Do NOT hand-edit files under
`dist/` — they are content-addressed Vite output. Update `.sync-stamp` to name
the commit you built from.

`--exclude='.sync-stamp'` is not optional. The source `dist/` has no copy of the
stamp, so a plain `--delete` removes it — and no test can catch that, because
what `embed_test.go` asserts is precisely that the stamp is NOT in the binary.
It went missing twice before the flag was written down here.

`embed_test.go` pins the two things a bad sync breaks silently: that the bundle
was built for `/tracker/` (a wrong base resolves every chunk to a path nothing
serves — a blank page, not an error), and that it calls `/v1/tracker` (the API
prefix is inlined at build time, so a bundle built against a dev proxy would
render and then talk to a surface this binary does not answer).
