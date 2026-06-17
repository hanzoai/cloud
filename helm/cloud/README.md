# helm/cloud

Minimal Helm chart for the unified `hanzoai/cloud` binary (HIP-0106).

## Install

```bash
helm install cloud ./helm/cloud \
  --set hanzo.iamIssuer=https://iam.hanzo.id \
  --set hanzo.brand=hanzo \
  --set hanzo.domain=api.hanzo.ai \
  --set image.tag=v0.1.0
```

## Required values

- `hanzo.iamIssuer` — OIDC issuer URL. Without this the IAM subsystem
  refuses to mount and the pod CrashLoopBackOff's.

## What this chart is NOT

This is a single-Deployment / single-Service / single-ConfigMap chart.
For multi-tenant / multi-CRD operator-driven topology, install
`luxfi/operator` and use the `Service` CRD instead — that's the
canonical k8s shape per HIP-0106 + HIP-0014.

For PCI workloads (payments, vault) — those subsystems are NEVER
co-resident with the rest of cloud. Run them as separate Deployments
backed by their own charts; configure their ZAP RPC endpoints into
this chart's values.
