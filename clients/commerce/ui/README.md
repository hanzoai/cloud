# commerce/ui

Embedded admin SPA — built externally in the Hanzo GUI workspace.

## Build

```bash
cd ../gui/apps/admin-commerce && bun run build
```

## Sync

```bash
./scripts/sync-admin-ui.sh
```

The synced `dist/` is baked into the `commerced` binary via `//go:embed`.
Served at `/_/commerce/` by `pkg/commerce/server.go`.
