# Deploying hanzoai/cloud

Reference deployment manifests for the unified Hanzo Cloud binary
(HIP-0106). Each manifest demonstrates a different deployment topology.

| Manifest | Topology | Use case |
|----------|----------|----------|
| `compose.yml` | single-node Docker | VPS, dev, demo |

Coming soon: `kustomize/` and `helm/` for k8s deploys (see luxfi/operator
for the canonical CRD-driven shape).

## Quick start (Docker Compose)

```bash
cp deploy/compose.env.example deploy/compose.env
# edit HANZO_BRAND / HANZO_DOMAIN / HANZO_IAM_ISSUER as needed
docker compose -f deploy/compose.yml --env-file deploy/compose.env up -d
curl http://localhost:8080/health
```

## Environment

Required:
- `HANZO_IAM_ISSUER` — OIDC issuer URL. Without it the IAM subsystem
  refuses to mount and the binary exits.

Optional (defaults shown):
- `HANZO_BRAND=hanzo`
- `HANZO_DOMAIN=api.hanzo.local`
- `HANZO_ENABLE=iam,base,kms,gateway,o11y`
- `HANZO_DATA_DIR=/var/lib/cloud`

The full subsystem list is in `cmd/cloud/main.go`. Per HIP-0106
payments and vault NEVER co-resident — leave them off this binary
unless you understand the PCI scope implications.
