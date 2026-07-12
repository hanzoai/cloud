# Tasks UI (embedded)

`dist/` is the built **Hanzo Tasks SPA** — the `admin-tasks` app (`@hanzo/tasks`,
Vite + hanzogui) — baked into the cloud binary via `//go:embed` and served at
`/tasks/*` by `clients/tasks`. This is the real UI behind `tasks.hanzo.ai` and `console.hanzo.ai/tasks`;
cloud is the ONE process that serves it, which retires the standalone `tasks-ui`
pod (a Temporal-Web-UI fork).

The SPA is built with `base: '/tasks/'` and `VITE_API_PREFIX=/v1/tasks`, so
every asset and XHR is same-origin under paths cloud already serves
(`/tasks/*` static, `/v1/tasks/*` API).

## Regenerating dist/

Source of truth: the `admin-tasks` app. It composes `@hanzogui/admin` (from
`hanzoai/admin`) with the `hanzogui` / `@hanzogui/*` v7 primitives (from the gui
workspace). Build it in a workspace where BOTH resolve, then sync its `dist/`:

```sh
# in the workspace that provides hanzogui@7.3.x + @hanzogui/admin
cd apps/admin-tasks
bun install
bun run build            # → apps/admin-tasks/dist  (base=/tasks/, api=/v1/tasks)

# sync into cloud
rsync -a --delete apps/admin-tasks/dist/ <cloud>/clients/tasksvc/ui/dist/
```

Then `go build ./cmd/cloud` re-embeds it. Do NOT hand-edit files under `dist/` —
they are content-addressed Vite output. Keep `.sync-stamp` truthful (source repo
+ commit).
