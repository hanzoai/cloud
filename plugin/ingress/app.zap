# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package ingress

struct Route {
    ID          text       @0
    Host        text       @8
    PathPrefix  text       @16
    Service     text       @24
    Middlewares list<text> @32
    TLS         bool       @40
    Priority    i64        @48
}

struct TLSConfig {
    ACMEEmail  text       @0
    Staging    bool       @8
    ExtraHosts list<text> @16
}

struct Upstream {
    ID             text        @0
    Backends       list<bytes> @8
    PassHostHeader bool        @16
}

struct ingressRoutes {
    Routes list<bytes> @0
}

struct ingressServices {
    Services list<bytes> @0
}

struct ingressStatus {
    Role         text @0
    EdgeEnabled  bool @8
    HTTPAddr     text @16
    HTTPSAddr    text @24
    ACMEStaging  bool @32
    ACMECacheDir text @40
    LiveHosts    i64  @48
    TLSHosts     i64  @56
    Proxy        text @64
}

struct ingressTLS {
    Config        bytes      @0
    Role          text       @8
    EdgeEnabled   bool       @16
    ManagedHosts  list<text> @24
    ACMEDirectory text       @32
    ACMEEmail     text       @40
    Note          text       @48
}

struct objRef {
    ID text @0
}

interface ingress {
    # Removes one of the caller org's edge transforms and hot-applies
    # the change. Routes still naming it stop being served (they compile as skipped)
    # until they name a transform that exists. Answers 204; an id this org does not
    # hold is 404.
    delete_ingress_middlewares_by_id(req: objRef)
    # Removes one of the caller org's routing rules and hot-applies the
    # shrunken table, freeing its host for another claim. Answers 204; an id this org
    # does not hold is 404.
    delete_ingress_routes_by_id(req: objRef)
    # Removes one of the caller org's backend pools and hot-applies the
    # change. Routes still pointing at it stop being served (they compile as skipped)
    # until they name a pool that exists. Answers 204; an id this org does not hold
    # is 404.
    delete_ingress_services_by_id(req: objRef)
    # Returns every routing rule the caller's org has configured, ordered
    # by id. A route maps an exact Host (and optional path prefix) to a service.
    get_ingress_routes() returns (rep: ingressRoutes)
    # Returns one of the caller org's routing rules by id.
    get_ingress_routes_by_id(req: objRef) returns (rep: Route)
    # Returns every backend pool the caller's org has configured,
    # ordered by id. A service is the weighted round-robin target a route dispatches
    # to.
    get_ingress_services() returns (rep: ingressServices)
    # Returns one of the caller org's backend pools by id.
    get_ingress_services_by_id(req: objRef) returns (rep: Upstream)
    # Status reports the ingress edge's live posture: the role this instance runs in
    # (app or edge), whether its listeners are bound and on which addresses, the ACME
    # posture (staging flag and certificate cache directory), how many hosts the
    # compiled route table currently serves, and how many the ACME HostPolicy will
    # issue a certificate for.
    get_ingress_status() returns (rep: ingressStatus)
    # GetTLS returns the caller org's ACME intent together with the edge-wide TLS
    # facts it lands in: which role this instance runs in, whether its listeners are
    # bound, every host the ACME HostPolicy will issue a certificate for (the union
    # across ALL orgs of TLS-marked routes and configured extraHosts, because one
    # process holds one certificate cache), and the ACME directory and account email
    # the process was started with.
    get_ingress_tls() returns (rep: ingressTLS)
    # Creates or replaces one routing rule and hot-applies the new table —
    # there is no config file and no restart. POST mints an id when the body omits
    # one; PUT takes the id from the URL, which wins over any id in the body. A
    # route's host is a GLOBALLY unique DNS claim: a host another org's route already
    # holds is refused 409, so no tenant can hijack another's hostname.
    post_ingress_routes(req: Route) returns (rep: Route)
    # Creates or replaces one backend pool and hot-applies it. POST mints
    # an id when the body omits one; PUT takes the id from the URL, which wins over
    # any id in the body. A pool needs at least one backend and every backend URL
    # must be http(s)://host[:port].
    post_ingress_services(req: Upstream) returns (rep: Upstream)
    # Creates or replaces one routing rule and hot-applies the new table —
    # there is no config file and no restart. POST mints an id when the body omits
    # one; PUT takes the id from the URL, which wins over any id in the body. A
    # route's host is a GLOBALLY unique DNS claim: a host another org's route already
    # holds is refused 409, so no tenant can hijack another's hostname.
    put_ingress_routes_by_id(req: Route) returns (rep: Route)
    # Creates or replaces one backend pool and hot-applies it. POST mints
    # an id when the body omits one; PUT takes the id from the URL, which wins over
    # any id in the body. A pool needs at least one backend and every backend URL
    # must be http(s)://host[:port].
    put_ingress_services_by_id(req: Upstream) returns (rep: Upstream)
    # PutTLS replaces the caller org's ACME intent and hot-applies what can be
    # hot-applied. extraHosts are normalized and validated, then feed the ACME
    # HostPolicy on the reload this op performs, alongside the per-route tls flags.
    # acmeEmail and staging bind an ACME account for the lifetime of an edge process,
    # so they only take effect when the edge (re)starts — the returned note says so.
    put_ingress_tls(req: TLSConfig) returns (rep: TLSConfig)
}

# ---------------------------------------------------------------------
# 14 op(s) here. What follows is what this schema does not carry.
#
# blocked (6) — the op is absent; the field has no wire form:
#   get_ingress_middlewares  ingressMiddlewares.Middlewares  []ingress.Middleware  (no wire form)
#   get_ingress_middlewares_by_id  Middleware.Config  map[string]string  (map)
#   post_ingress_middlewares  Middleware  ingress.Middleware  (reaches one)
#   post_ingress_middlewares  Middleware  ingress.Middleware  (reaches one)
#   put_ingress_middlewares_by_id  Middleware  ingress.Middleware  (reaches one)
#   put_ingress_middlewares_by_id  Middleware  ingress.Middleware  (reaches one)
#
# opaque (4) — crosses, arrives without its name:
#   Upstream.Backends  ingress.Backend (list element)
#   ingressRoutes.Routes  ingress.Route (list element)
#   ingressServices.Services  ingress.Upstream (list element)
#   ingressTLS.Config  ingress.TLSConfig
