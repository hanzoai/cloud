# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package cloudflare

struct D1Query {
    Database text        @0
    SQL      text        @8
    Params   list<bytes> @16
}

struct WorkerScriptPut {
    Name               text       @0
    Script             text       @8
    MainModule         text       @16
    CompatibilityDate  text       @24
    CompatibilityFlags list<text> @32
    Bindings           bytes      @40
}

struct analyticsIn {
    Zone       text @0
    Since      text @8
    Until      text @16
    Continuous text @24
}

struct bucketCreateIn {
    Name text @0
}

struct bucketRef {
    Bucket text @0
}

struct bucketsIn {
    PerPage      text @0
    Cursor       text @8
    NameContains text @16
    Order        text @24
    Direction    text @32
}

struct databaseCreateIn {
    Name text @0
}

struct databaseRef {
    Database text @0
}

struct databasesIn {
    Page    text @0
    PerPage text @8
    Name    text @16
}

struct domainAddIn {
    Project text @0
    Name    text @8
}

struct domainRef {
    Project text @0
    Domain  text @8
}

struct namespaceCreateIn {
    Title text @0
}

struct namespaceRef {
    Namespace text @0
}

struct namespacesIn {
    Page      text @0
    PerPage   text @8
    Order     text @16
    Direction text @24
}

struct projectRef {
    Project text @0
}

struct purgeIn {
    Zone       text       @0
    Everything bool       @8
    Files      list<text> @16
}

struct routeCreateIn {
    Zone    text @0
    Pattern text @8
    Script  text @16
}

struct routeRef {
    Zone  text @0
    Route text @8
}

struct scriptRef {
    Script text @0
}

struct subdomainSetIn {
    Script  text @0
    Enabled bool @8
}

struct valueRef {
    Namespace text @0
    Key       text @8
}

struct zoneRef {
    Zone text @0
}

struct zonesIn {
    Page      text @0
    PerPage   text @8
    Name      text @16
    Status    text @24
    Order     text @32
    Direction text @40
}

interface cloudflare {
    # Deletes a D1 database and everything stored in it. Requires
    # org admin.
    delete_cloudflare_d1_databases_by_database(req: databaseRef)
    # KVNamespaceDelete deletes a Workers KV namespace and every key in it. Requires
    # org admin.
    delete_cloudflare_kv_namespaces_by_namespace(req: namespaceRef)
    # KVValueDelete removes one key from a Workers KV namespace. Requires org admin.
    delete_cloudflare_kv_namespaces_by_namespace_values_by_key(req: valueRef)
    # Deletes a Cloudflare Pages project, and with it every deployment it
    # has ever made. Requires org admin.
    delete_cloudflare_pages_projects_by_project(req: projectRef)
    # Detaches a custom domain from a Cloudflare Pages project.
    # Requires org admin.
    delete_cloudflare_pages_projects_by_project_domains_by_domain(req: domainRef)
    # Deletes an R2 bucket. Requires org admin. Cloudflare refuses a
    # bucket that still holds objects, and that refusal is relayed.
    delete_cloudflare_r2_buckets_by_bucket(req: bucketRef)
    # Removes a Worker script from the org's Cloudflare account.
    # Requires org admin. Routes bound to the script stop serving it.
    delete_cloudflare_workers_scripts_by_script(req: scriptRef)
    # Unbinds a Worker route, so its pattern stops dispatching to a
    # script. Requires org admin.
    delete_cloudflare_workers_zones_by_zone_routes_by_route(req: routeRef)
    # Lists the D1 databases on the org's Cloudflare account. Any org
    # member may read.
    get_cloudflare_d1_databases(req: databasesIn)
    # KVNamespaceList lists the Workers KV namespaces on the org's Cloudflare
    # account. Any org member may read.
    get_cloudflare_kv_namespaces(req: namespacesIn)
    # Lists the org's Cloudflare Pages projects. Any org member may read.
    get_cloudflare_pages_projects()
    # Reads one Cloudflare Pages project — its build config, deployment
    # configs and latest deployment. Any org member may read.
    get_cloudflare_pages_projects_by_project(req: projectRef)
    # Lists the R2 buckets on the org's Cloudflare account. Any org
    # member may read.
    get_cloudflare_r2_buckets(req: bucketsIn)
    # Lists the Worker scripts on the org's Cloudflare account. Any
    # org member may read.
    get_cloudflare_workers_scripts()
    # Reads the org account's workers.dev subdomain — the name
    # under which every subdomain-enabled script is served. Any org member may read.
    get_cloudflare_workers_subdomain()
    # Lists the Worker routes bound within one zone — the URL
    # patterns that dispatch to a script. Any org member may read. Routes are
    # zone-scoped, so no account is resolved.
    get_cloudflare_workers_zones_by_zone_routes(req: zoneRef)
    # Lists the Cloudflare zones the org's connected API token can see,
    # paged and filtered by the query parameters Cloudflare itself accepts. Zones are
    # token-scoped by Cloudflare, so no account is resolved. Any org member may read.
    # Zone and DNS-record MANAGEMENT is not here: it stays on the Hanzo DNS plane
    # (/v1/dns). This only surfaces the Cloudflare zone objects the asset plane needs
    # — a zone id is what addresses a Worker route or an analytics read.
    get_cloudflare_zones(req: zonesIn)
    # Reads one Cloudflare zone the org's token can see. Any org member may
    # read. A zone id the token cannot see is Cloudflare's own not-found, relayed.
    get_cloudflare_zones_by_zone(req: zoneRef)
    # Reads a zone's Cloudflare traffic dashboard — requests, bandwidth,
    # threats and pageviews over the since/until window. Any org member may read.
    # A zone whose Cloudflare plan does not serve this endpoint yields Cloudflare's
    # OWN error, never a fabricated success.
    get_cloudflare_zones_by_zone_analytics(req: analyticsIn)
    # Creates a D1 database on the org's Cloudflare account.
    # Requires org admin.
    post_cloudflare_d1_databases(req: databaseCreateIn)
    # Runs one SQL statement against a D1 database. It executes on the org's
    # OWN Cloudflare account and relays D1's result set. The body is checked for a
    # non-empty `sql` and then forwarded VERBATIM, so every field D1 accepts reaches D1
    # even though only two are named here.
    # Requires ORG ADMIN — a statement may INSERT, UPDATE or DROP, so a query takes the
    # write gate rather than the read one — and a caller who is only an org member is
    # refused 403. A missing `sql` is 400; 503 if the org has never connected a
    # Cloudflare token.
    post_cloudflare_d1_databases_by_database_query(req: D1Query)
    # KVNamespaceCreate creates a Workers KV namespace on the org's Cloudflare
    # account. Requires org admin. Cloudflare mints the namespace id the value routes
    # address.
    post_cloudflare_kv_namespaces(req: namespaceCreateIn)
    # Attaches a custom domain to a Cloudflare Pages project. Requires
    # org admin. Cloudflare owns validation and certificate issuance from here on.
    post_cloudflare_pages_projects_by_project_domains(req: domainAddIn)
    # Creates an R2 bucket on the org's Cloudflare account. Requires
    # org admin.
    post_cloudflare_r2_buckets(req: bucketCreateIn)
    # Publishes or withdraws one Worker script on the
    # account's workers.dev subdomain. Requires org admin.
    post_cloudflare_workers_scripts_by_script_subdomain(req: subdomainSetIn)
    # Binds a URL pattern in a zone to a Worker script. Requires
    # org admin — a route is what puts a script in front of live traffic.
    post_cloudflare_workers_zones_by_zone_routes(req: routeCreateIn)
    # Drops a zone's Cloudflare edge cache — either the whole zone
    # (purge_everything) or exactly the listed file URLs. Requires org admin.
    # Purging is the one zone-scoped WRITE this plane owns. It is not DNS — no record
    # changes — so it does not belong on /v1/dns, and it is not a connection, so it does
    # not belong on the integrations plane. It is a cache operation on a zone, which is
    # what this asset plane is for. It takes the admin gate because dropping a zone's
    # cache sends every subsequent request to the origin: on a site fronting a small
    # origin that is a self-inflicted load spike, so it is a change, not a look.
    # Exactly one selector is required. Cloudflare treats a body with neither as a
    # no-op and answers 200, which reads as "purged" to a caller that never purged
    # anything — the failure we refuse to pass through.
    post_cloudflare_zones_by_zone_purge(req: purgeIn)
    # Uploads or replaces a module Worker script. It publishes to the
    # org's OWN Cloudflare account under the name in the path, replacing whatever was
    # there, and relays Cloudflare's result. The compatibility date, compatibility
    # flags and bindings are packed into the multipart upload Cloudflare expects,
    # beside the module source.
    # Requires ORG ADMIN — a Worker is arbitrary code on the org's own account and
    # domains — so a caller who is only an org member is refused 403. An empty source
    # is 400, as is a `mainModule` that is not a plain file name; 503 if the org has
    # never connected a Cloudflare token.
    put_cloudflare_workers_scripts_by_script(req: WorkerScriptPut)
}

# ---------------------------------------------------------------------
# 28 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   post_cloudflare_pages_projects  PagesProjectCreate.DeploymentConfigs  cloudflare.PagesDeploymentConfigs  (reaches one)
