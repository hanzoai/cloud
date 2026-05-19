# Hanzo Cloud

The unified Go binary that imports every Hanzo-native subsystem and dispatches
requests per deployment configuration. One artifact, many subsystems.

Per [HIP-0106](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0106-unified-hanzo-cloud-binary.md).

## Subsystems mounted

- `iam` — identity & access
- `base` — per-tenant SQLite + extension runtimes (per HIP-0105)
- `kms` — secrets
- `commerce` — checkout, billing, pricing, invoicing (light router; NOT in PCI-DSS scope)
- `ai` — LLM control plane / RAG / model hub / MCP management (was hanzoai/cloud pre-rename)
- `gateway` — HTTP routing + policy
- `o11y` — metrics / traces / logs
- `vfs` — virtual filesystem / object-store abstraction
- `mq` — message queue
- `dns`, `amqp`, `mcp`, `auto`, `tasks`, ... (full list per HIP-0106)

## Deployment modes

Same binary; different startup configuration:

```bash
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=hanzo  --domain=hanzo.ai
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=osage  --domain=osage.cloud
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=lux    --domain=lux.cloud
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=zoo    --domain=zoo.cloud
```

## White-label fork pattern

Customers fork `hanzoai/cloud` to launch their own ecosystem in one binary. Brand
detection, enabled subsystems, ZAP endpoints (payments / vault backends) are all
deployment configuration.

## Web framework

[hanzoai/zip](https://github.com/hanzoai/zip) — Sinatra-style Go web framework
built on Fiber v3. The ONE Go web framework. No `.Fast` escape hatch.

## Status

Scaffold. The Mount(app, deps) integration for each subsystem lands per
HIP-0106's migration phases.
