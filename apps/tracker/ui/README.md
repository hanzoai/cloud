# Tracker UI (embedded)

`dist/` is the built **Hanzo Tracker SPA** — the `admin-tracker` app
(`@hanzo/tracker`, Vite + a `@hanzogui` shell over the forge's own board CSS) —
baked into the cloud binary via `//go:embed` and served at `/tracker/*` by
`apps/tracker`. This is the real UI behind `tracker.hanzo.ai`, and it is what
retires the Huly tracker that used to answer that host.

The SPA is built with `base: '/tracker/'` and `VITE_API_PREFIX=/v1/tracker`, so
every asset and XHR is same-origin under paths this binary already serves
(`/tracker/*` static, `/v1/tracker/*` API).

Same-origin is load-bearing rather than convenient: the SPA sends **no tenancy**
of its own. cloud's identity layer mints the validated org from the IAM session
before any tracker handler runs, so the page carries the session cookie and
nothing else. A UI on a second host would have to hold a token and name an org —
exactly the client-supplied tenancy this surface refuses.

## Regenerating dist/

Source of truth: the `admin-tracker` app in `hanzoai/admin`. Build it where
`hanzogui@7.x` and `@hanzogui/admin` both resolve, then sync its `dist/`:

```sh
cd apps/admin-tracker
../../node_modules/.bin/tsc --noEmit     # typecheck gate
../../node_modules/.bin/vite build       # → dist/  (base=/tracker/, api=/v1/tracker)

rsync -a --delete apps/admin-tracker/dist/ <cloud>/apps/tracker/ui/dist/
```

Then `go build ./plugin/tracker` re-embeds it. Do NOT hand-edit files under
`dist/` — they are content-addressed Vite output. Keep `.sync-stamp` truthful
(source repo + commit).

`embed_test.go` pins the two things a bad sync breaks silently: that the bundle
was built for `/tracker/` (a wrong base resolves every chunk to a path nothing
serves — a blank page, not an error), and that it calls `/v1/tracker` (the API
prefix is inlined at build time, so a bundle built against a dev proxy would
render and then talk to a surface this binary does not answer).
