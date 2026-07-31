# apps/registry — Hanzo Registry on /v1/registry (management plane over the running registries)

Hanzo Registry's data planes are running products, not code in cloud: the OCI
registry at **oci.hanzo.ai** (github.com/hanzoai/registry — CNCF distribution,
S3-backed, Hanzo IAM token auth; alias registry.hanzo.ai) and the npm registry
at **pkg.hanzo.ai** (hanzoai/pkg — verdaccio on S3, uplinked to npmjs; the
hanzoai/git forge's /v1/packages ecosystems share the host). This app is the
typed MANAGEMENT surface over both: what exists, who may pull it — cloud adds
IAM auth, the org boundary, and the unified projection (OpenAPI, MCP tool, CLI
command, SDK method — all from the six typed ops).

**Control-plane only.** The OCI wire (manifests, blobs, push, pull) stays on
oci.hanzo.ai and is never proxied through cloud — a data path here would
double-move every image byte and break the content-addressed protocol. This
plane answers questions AROUND the wire and mints entry to it.

## The served slice (6 typed ops, 0 untyped)

| op | upstream call |
|---|---|
| GET /v1/registry/status | GET /v2/ challenge probe + GET /-/ping composed (honest reachability lens; reports the advertised token realm) |
| GET /v1/registry/projects | catalog walk + search composed into the org's one namespace row with live counts |
| GET /v1/registry/images | GET /v2/_catalog (Link-paged walk), filtered server-side to `<org>/…` |
| GET /v1/registry/tags?image= | GET /v2/{org}/{image}/tags/list with scope `repository:<org>/<image>:pull` |
| GET /v1/registry/packages | GET /-/v1/search, filtered server-side to `<org>` and `@<org>/…` |
| POST /v1/registry/token | GET {realm}?service&scope through the registry's own 401 challenge — a pull-only, org-pinned, short-lived token |

The wire below the ops is the distribution token protocol exactly as the
docker CLI speaks it: probe /v2/, parse the WWW-Authenticate Bearer challenge
(realm + service — the live registry advertises the IAM realm
`…/v1/iam/registry/token`), mint with Basic auth, ride Bearer. The challenge
is parsed per process, never hardcoded, so a realm move follows the deployment
with zero code.

## Tenancy

The registries are shared platform deployments, so the org boundary is
enforced HERE on their own namespace conventions: an org's images are the
catalog entries under `<org>/…` (the fleet's `<host>/<org>/<app>` push
convention) and its npm packages are `<org>` and `@<org>/…`. The org comes
from the validated principal (principal.Org), never an In field; per-image ops
pass the `owned` gate, which composes the ONE addressable repository name
`<org>/<image>` — a foreign repository cannot be expressed. Filtering happens
before any response shape exists. typed_wire_test.go measures all of it.

Config: REGISTRY_UPSTREAM (default `https://oci.hanzo.ai`), REGISTRY_PKG
(default `https://pkg.hanzo.ai`), REGISTRY_CLIENT_ID / REGISTRY_CLIENT_SECRET
(an IAM application's service credentials — the machine path
iam/controllers/registry_token.go privileges — KMS-synced env). The credential
rides only Basic auth to the realm; upstream 401/403 → caller sees 503
(deployment fault, never a caller-auth bug); upstream 5xx → 502; unreachable →
503.

## Tokens

POST /v1/registry/token is a CAPABILITY mint, not a login: scope pinned
server-side to `repository:<org>/<image>:pull`, lifetime the realm's (15 min at
IAM today). Push and delete are never minted — CI pushes with its own KMS-held
service account. The docker-login flow itself stays on the wire: `docker login
oci.hanzo.ai` exchanges the user's own credentials at the same realm without
cloud in the loop.

## The refused intent (and why)

hanzoai/openapi authored 13 Harbor-shaped paths for this product and deleted
them as UNSERVED (commit d86248f: every probe answered a route-level 404
everywhere; the host that did answer spoke the OCI wire, not this REST
surface). What the running backends do not have gets NO route: project CRUD
(the namespace is the IAM org, not a registry row), webhooks (distribution's
notifications are static deployment config), quotas (no quota engine), scans
(no scanner deployed), digest-level artifact reads (that IS the OCI wire),
manifest deletion (unproven destructive op on shared images), push grants
(privilege escalation), forge ecosystem listing (org→forge-owner mapping
unbuilt). `intentRefused` in typed_wire_test.go is the closed ledger, measured
(each family 404s on the live router and is absent from the document), so
reviving a family is a deliberate edit to the ledger plus a proven op, never a
drive-by.

## Tests

- `make -C apps/registry test` — fakes pin the measured wire (the /v2/ 401
  challenge, the IAM token envelope, Link-paged catalog, verdaccio search).
- `REGISTRY_E2E=1 …` — live_test.go re-proves the credential-free half against
  the real hosts; add REGISTRY_E2E_ID/SECRET (+ REGISTRY_E2E_ORG) for the
  authed half (catalog, tags, token through the real IAM realm).
