# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package base

struct baseHealth {
    Service text @0
    Status  text @8
}

struct baseRef {
    Org text @0
}

struct baseView {
    Org    text @0
    Exists bool @8
    Bytes  i64  @16
}

interface base {
    # Lists every Base the caller can reach, one per org their token carries.
    # The orgs come from IAM's signed membership set, so the list is exactly the
    # orgs the caller is a member of and cannot be widened by asking. It is the
    # account-wide view: a Base is per org, so this is one entry per org and there
    # is nothing to page.
    # A caller with no membership set — a machine credential, an API key — reaches
    # no Base and receives an empty list rather than a refusal, because holding no
    # membership is an answer and not a failure.
    get_base_bases()
    # Describes ONE org's Base — whether its store exists, and what it occupies.
    # The org must be one the caller's token carries; any other is not found, so
    # this cannot be used to learn which orgs exist. That check is the same
    # membership set the listing is built from, which is why the two can never
    # disagree about what a caller may see.
    get_base_bases_by_org(req: baseRef) returns (rep: baseView)
    # Reports that the base subsystem is serving.
    # It is deliberately INDEPENDENT of whether this deployment actually embeds the
    # Base engine: the route answers before the CLOUD_BASE_EMBED gate and before the
    # /v1/base/* wildcard, so a liveness probe measures the process rather than an
    # optional feature, and the wildcard can never shadow it. It reads no tenant, so a
    # prober that sends no principal is answered rather than refused.
    get_base_health() returns (rep: baseHealth)
}
