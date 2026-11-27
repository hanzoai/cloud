# apps/cloudflare

## AI Search: one namespace per org, derived, never chosen (#810)

Cloudflare AI Search puts an instance in the account's `default` namespace unless it
is told otherwise, and the namespace is part of the instance's identity: REST paths
(`/accounts/{id}/ai-search/namespaces/{namespace}/instances/{id}`), Terraform import
ids (`<account_id>/<namespace>/<instance_id>`) and data sources all carry it. Moving
an instance to another namespace is a REPLACEMENT that discards its index —
`cloudflare_ai_search_instance` forces new on a `namespace` change (provider ≥ 5.29).
So the namespace is a migration boundary, not a label.

**The rule.** Every AI Search instance this plane creates lives in ONE namespace per
org, and that namespace is derived from the validated org:

    hz- + hex(sha256("hanzo/cloudflare/ai-search/namespace\x00" + principal.OrgNamespace(org, "")))[:24]

27 characters, inside Cloudflare's rule (1–28 of `[a-z0-9-]`, alphanumeric at both
ends). The org goes through `principal.OrgNamespace`, the one injective fold every
per-org store in this process is filed under, so two orgs never share a namespace
and the same org always gets the same one. The `hz-` prefix makes `default`
unreachable by construction and marks Hanzo's namespaces in an account that also
holds others. The org is the immutable scope: it is the key of every per-org
database (`{DataDir}/orgs/{org}/…`), so renaming one would orphan far more than an
index. If IAM ever lets an org be renamed, this derivation must move to an id that
survives the rename IN THE SAME CHANGE, and every index moves by the replacement
below.

**The surface** (`aisearch.go`):

| Route | Gate | Does |
| --- | --- | --- |
| `GET /v1/cloudflare/ai-search/namespace` | member | the org's derived name; no Cloudflare call, no token needed |
| `POST /v1/cloudflare/ai-search/namespace` | admin | creates it; a `name` other than the derived one is 400 |
| `GET /v1/cloudflare/ai-search/instances` | member | lists the derived namespace only |
| `POST /v1/cloudflare/ai-search/instances` | admin | creates in the derived namespace; body forwarded |
| `GET /v1/cloudflare/ai-search/instances/:instance` | member | reads from the derived namespace |
| `PUT /v1/cloudflare/ai-search/instances/:instance` | admin | updates config; body forwarded |
| `DELETE /v1/cloudflare/ai-search/instances/:instance` | admin | deletes, index included |

What is refused, each as a 400 that sends Cloudflare nothing: a body `namespace`
other than the derived name (`default` included), any `new_namespace` (Cloudflare's
move), an instance id outside Cloudflare's rule, a body that is not a JSON object.
Repeating the derived name is accepted and stripped; the URL is the only address.

What is NOT served, on purpose (`TestAISearchServesNoMoveNoQueryNoNamespacePath`):
`PATCH` on an instance (the move), any `namespaces/{name}` path (a caller-named
namespace has no URL to ride), and search or chat queries.

**The namespace is not Hanzo's boundary.** Who may act is the validated principal,
then org membership (reads) or org admin (writes), decided before custody or
Cloudflare is reached. Who pays is the org whose own KMS-sealed token made the call,
stamped as `X-Hanzo-Org` on every response. Per-document ACLs cannot be expressed
as a namespace at all — a query answered by a namespace returns every document in it
to every caller — which is why this plane relays no queries: retrieval goes through
Hanzo's own `/v1/search` and `/v1/knowledge/search`, which bound every search by the
principal (`apps/knowledge` `ownerFilter`, see its LLM.md).

### Managing it with Terraform

Ask Hanzo for the name, then pin it. Hanzo has no Cloudflare Terraform of its own;
this is the shape for an org that manages its account with Terraform:

```hcl
terraform {
  required_providers {
    cloudflare = { source = "cloudflare/cloudflare", version = ">= 5.29.0" }
  }
}

# GET /v1/cloudflare/ai-search/namespace → .namespace
variable "hanzo_ai_search_namespace" {
  type = string
  validation {
    condition     = can(regex("^hz-[0-9a-f]{24}$", var.hanzo_ai_search_namespace))
    error_message = "Use the namespace Hanzo derived for this org; default and hand-picked names are refused."
  }
}

resource "cloudflare_ai_search_namespace" "org" {
  account_id = var.account_id
  name       = var.hanzo_ai_search_namespace
  lifecycle { prevent_destroy = true }
}

resource "cloudflare_ai_search_instance" "docs" {
  account_id = var.account_id
  namespace  = cloudflare_ai_search_namespace.org.name # never omitted: omitted means default
  id         = "support-docs"
  type       = "r2"
  source     = "support-docs"
  # A namespace change plans a destroy-and-create, which this refuses to apply.
  lifecycle { prevent_destroy = true }
}
```

`prevent_destroy` is what blocks the in-place change: Terraform's plan for a new
`namespace` is a replacement, and the apply fails instead of discarding the index.
The import id is `<account_id>/<namespace>/<instance_id>`.

### Moving an index to another namespace: replace, never move

An instance's namespace changes only by replacement. The cases are an instance made
outside Hanzo that sits in `default`, and a change to the derivation itself. Never
use Cloudflare's `PATCH … {"new_namespace": …}` or edit `namespace` in Terraform.

1. **Create** the target namespace and a NEW instance in it, with the same source
   and configuration (`POST …/ai-search/namespace`, `POST …/ai-search/instances`).
   The old instance keeps serving.
2. **Dual-write.** Everything that feeds the old instance feeds the new one too. An
   R2-sourced pair can share one bucket, which makes this automatic; a crawler pair
   crawls the same site. New content must reach both before the backfill starts, or
   the backfill races it.
3. **Backfill.** Run a full sync on the new instance and wait until its indexed item
   count matches the old one's.
4. **Verify** through Hanzo's ingress, never straight from Cloudflare: a fixed query
   set returns the same documents, under the same ACLs, from both.
5. **Cut over** the readers (Worker bindings, Hanzo's search configuration) to the
   new instance in one change.
6. **Keep the old instance** through a rollback window — rolling back is pointing the
   readers back. Then delete the old instance, then its namespace (Cloudflare refuses
   to delete a namespace that still holds instances).
