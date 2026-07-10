# Security Policy

## Reporting a vulnerability

Email security@hanzo.ai with details. Encrypt with our PGP key (fingerprint TBD).

We respond within 48 hours. Critical issues receive same-day acknowledgment.

## Scope

This policy covers code in this repository. For the broader Hanzo platform threat model, see [hanzoai/HIPs](https://github.com/hanzoai/HIPs).

## Sandbox boundary

`commerce` is **CDE-connected, not CDE-in-scope**: it never sees raw PAN data, only the opaque tokens returned by `hanzoai/vault`. All other identifying customer data is scoped per-org by the JWT-validated `X-Org-Id` header, and downstream provider webhooks (Stripe etc.) are signature-verified before processing.

For runtime sandbox guarantees, see HIP-0105 (in-process extension runtimes).
