// Package dns is your DNS records: the zones and records behind every name you
// point at Hanzo.
//
// It relays the console's DNS dashboard traffic to the Hanzo DNS control plane
// (dns/plugin/hanzodns), which owns the authoritative zone/record store. cloud
// serves console.hanzo.ai (the DnsModule) but holds no DNS state of its own, so
// without this thin head console.hanzo.ai/v1/dns 404s and the dashboard shows
// empty zones.
//
// SHAPE. The plane's own API, address for address: twelve addresses over six
// patterns ([routes]), each answered by the same relay — path and query
// passthrough to the DNS control plane this deployment selected. It builds a
// FRESH upstream request and sets only the headers it means to send, so no
// inbound header (a stray cookie, a forged X-*, an injected Authorization copy)
// crosses.
//
// ADDRESSES, NOT A WILDCARD. This was one All("/*") registration, and a greedy
// wildcard publishes an address no caller can use: the document renders
// /v1/dns/{wildcard1}, openapi/public.go refuses it the customer document
// (audience: "a `{wildcardN}` address publishes whatever grows behind it and
// names nothing a client can call"), and the whole DNS product therefore reached
// no generated SDK, no CLI command and no agent tool. The addresses behind it are
// a KNOWN, FINITE set — hanzoai/dns registers them in plugin/hanzodns/api.go,
// registerRoutes and apiRouter — so they are declared here and the wildcard is
// gone. An address the plane does not serve is now 404 at cloud rather than a
// relayed 404 from the plane.
//
// STILL NOT TYPED OPS, AND THE REASON IS THE WIRE. A typed op's only response
// path is c.JSON(out) under the status it declared (zip typed.go:567), and this
// relay carries the plane's OWN status, its Content-Type and its Location on a
// 3xx. So each address is a plain per-method registration carrying declared
// prose, which is everything a relay CAN state about itself. typed_wire_test.go
// runs the fact rather than trusting it.
//
// WHERE THE PLANE IS is configuration; that it is Hanzo's is code. The head
// validates the caller, scopes the path, and hands the plane a Call (call.go);
// hanzo.go issues it at HANZO_DNS_URL. There is one send rather than an operation
// per address because the plane's API IS this contract — an address the routes
// name and one they do not travel identically, so telling them apart would only
// choose between identical calls.
//
// ISOLATION -- BEARER RELAY, NO STANDING CRED. The DNS plane is OIDC-gated and
// keys every zone per-org: it re-validates the caller's OWN bearer and derives
// the org from the `owner` claim. This head relays that identity UNCHANGED --
// the caller's validated bearer as Authorization (cloud.CallerBearer), plus the
// server-validated org as X-Org-Id -- and substitutes NO service credential
// (which would collapse tenants). So a caller in org A can reach only org A's
// zones, exactly as if it had called the DNS plane directly. Fail-closed: a
// request with no validated principal is refused 403 before any byte leaves
// cloud.
package dns
