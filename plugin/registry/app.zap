# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package registry

struct registryImageList {
    Data      list<bytes> @0
    Truncated bool        @8
}

struct registryMint {
    Image text @0
}

struct registryPackageList {
    Data list<bytes> @0
}

struct registryPackages {
    Query text @0
}

struct registryProjectList {
    Data list<bytes> @0
}

struct registryStatus {
    Oci     bool @0
    Pkg     bool @1
    Host    text @8
    PkgHost text @16
    Realm   text @24
    Service text @32
}

struct registryTagList {
    Image text       @0
    Ref   text       @8
    Data  list<text> @16
}

struct registryTags {
    Image text @0
}

struct registryToken {
    Token   text @0
    Expires i64  @8
    Ref     text @16
}

interface registry {
    # Images lists the org's container repositories, read live from the OCI
    # catalog and filtered server-side to the org's namespace — the page can only
    # ever hold the caller's own images.
    get_registry_images() returns (rep: registryImageList)
    # Packages lists the org's npm packages — `<org>` and `@<org>/…` — from the
    # npm registry's search index, optionally narrowed by a query within that
    # scope. The org boundary is applied server-side after the search, so a query
    # can never widen it.
    get_registry_packages(req: registryPackages) returns (rep: registryPackageList)
    # Projects lists the namespaces the caller can see with what each holds: the
    # org's slug, its repository count on the OCI catalog, and its package count
    # on the npm registry. Today that is exactly one row — the caller's org.
    get_registry_projects() returns (rep: registryProjectList)
    # Status reports whether the OCI and npm registries are reachable and, when
    # the OCI half is auth-gated, which token realm its challenge advertises — an
    # honest lens for "is the registry plane up", never a fabricated ok.
    get_registry_status() returns (rep: registryStatus)
    # Tags lists one org-owned repository's tags, read live from the OCI registry.
    # The repository is addressed inside the org's namespace — a name outside it
    # cannot be expressed, and an unknown one answers 404.
    get_registry_tags(req: registryTags) returns (rep: registryTagList)
    # Token mints a short-lived, pull-only registry token for exactly one of the
    # org's images, through the same IAM realm the docker CLI authenticates
    # against. The scope is pinned server-side to `<org>/<image>` with the `pull`
    # action — no field exists to name another org's image or ask for push. Use it
    # as `Authorization: Bearer …` on the OCI wire; it expires in minutes.
    post_registry_token(req: registryMint) returns (rep: registryToken)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   registryImageList.Data  registry.registryImage (list element)
#   registryPackageList.Data  registry.registryPackage (list element)
#   registryProjectList.Data  registry.registryProject (list element)
