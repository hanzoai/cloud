# LLM.md — apps/crawl

Guidance for apps/crawl. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Fetching the web (`apps/crawl`, `/v1/crawl`)

In-binary fetch + extract + markdown. It replaced a call to a standalone crawler
at `crawl.hanzo.svc.cluster.local:11235` — a name that had stopped resolving, which
nothing noticed because every caller is allowed to degrade: the answer engine's read
stage falls back to the ~600-char search snippet, so research answers silently
grounded on snippets and looked fine.

**`Fetch` and `Read` are different endpoints, deliberately.** `Fetch` is the pure network
primitive (URL in, `Page` out, no IO beyond the request) — testable with no store.
`Read` is what callers use: archive first, network second, keep what came back. One
endpoint, so no caller has to remember to persist and no two can disagree about where
pages live.

Kept pages ride the ONE object client the binary already has (`object.Blobs` over
the shared S3 gateway) — no second client, no second bucket, no second credential.
Key is `crawl/<org>/<project>/<sha256(url)>.json`, and both halves of that path are
load-bearing:

- The URL is HASHED, never spliced in. A URL carries `/`, `?`, `#`, `%` and arbitrary
  unicode; embedding one lets a crafted URL walk out of its prefix into another
  tenant's, which is the whole isolation boundary.
- Scope comes from the VERIFIED principal, never the body, because it selects that
  prefix.
- Keyed by the REQUESTED url, not `Page.URL` (where it landed after redirects) —
  filing under the latter means a cache that never hits for exactly the pages that
  redirect.
- The scope segments are readable + digest, because sanitising alone is LOSSY:
  fold-to-`-` maps `a/b` and `a-b` onto one segment, and two orgs that collide share
  a corpus prefix. The digest decides identity; the readable half is for browsing.

SSRF is guarded IN THE DIALER, not by inspecting the hostname: a name check is
TOCTOU (DNS rebinding), and redirects re-enter the same dialer for free. The dialer
is `hanzoai/outbound`'s, the one rule for every URL a tenant supplies (crawl,
`core.http_request`, custom_api_call, MCP servers, webhooks, destinations):
`Public` refuses every IANA special-purpose prefix and every IPv6 spelling of an
IPv4 address, judged in the dialer's Control hook on the address being connected;
`Host` judges a name for a caller that hands the URL to another dialler (the git
CLI, the crawl browser, client-go), and only that name. A link's Go requests to its
host (`apps/git` `upstream`: the fetch advertisement, the push relay) dial through
`outbound.Client`, so a name `admit` passed cannot connect somewhere else. The browser follows
redirects, frames and scripts no first-host check sees, so its bound is its
pod's egress policy (universe `charts/app/values/hanzo/crawl.yaml`), and `Read`
never escalates a fetch outbound refused. A kubeconfig that names a proxy is
refused: client-go would dial the proxy, not the judged apiserver. Storage
failures are best-effort throughout — a store that is down costs a cache hit,
never the page.

## Drawing a page (`render_page`, `POST /v1/crawl/render`)

`read_page` answers what a page says; `render_page` answers what it looks like: the
first screen as a PNG (`screenshot`, base64 on the wire), the DOM it ends in with scripts
and styles removed and cut at 256 KiB (`dom`, `cut`), its markdown and its links (internal
first, at most 100). It is the renderer's own `/crawl` asked for a screenshot
(`crawler_config.screenshot` + `force_viewport_screenshot`, both on the renderer's
untrusted-field allowlist), so it is the same browser, the same pooled `browser_config`
and the same signed navigation as an escalation. `visit` (browser.go) is the one place a
render is gated, sent and paid for; `browse` (markdown) and `Render` (picture) differ only in
what they ask and in what makes a render worth its fee: a picture for `Render`, text for
`browse`. Nothing is archived.

Admitted and judged as `read_page` is: validated principal, then a guarded GET (`open`) that
follows the redirect chain through the dialer and refuses with `outbound.ErrRefused` before
the browser is asked, then `reachable` on the first host, and the pod's egress policy for
the rest. A page that cannot be drawn is a 200 with `success:false` and the reason; no url is
a 400 with the same body.

## Agents sign what they fetch: Web Bot Auth (#423)

Every request a Hanzo agent sends to read the web carries an HTTP Message Signature
(RFC 9421) made with the deployment's Ed25519 key, and crawl publishes the public
half at `GET /.well-known/http-message-signatures-directory` (the manifest names that
one address for crawl, as it names iam's three). The signer is `sdk/botauth`, one per
process, installed by `internal/host` (`useBotAuth`) from the KMS handle `BuildDeps`
makes. A client signs by sending through `botauth.Transport` / `botauth.Client`;
`botauth.Signs` pins it in each package's tests.

- **Signed:** crawl's `client` (so `/v1/crawl`, websearch's page reads, the answer
  engine's research reads and ai's fetch tool), world's feed and GDELT client, and
  reference's publisher downloads. Each redirect hop is signed for its own host.
- **Signing for a service that holds no key:** `POST /v1/crawl/sign` (sign.go) takes
  `{authority, scheme}` and answers the three header values for one request to that
  host, from the same signer, so the key never leaves KMS. Only the host crosses, never
  the path. It admits only the brand org's own listed applications acting as
  themselves (`agents`: `hanzo-bot`, the hanzo.bot gateway's client_credentials
  identity), refusing people, keys, other orgs' applications, an org switch, another
  brand's issuer and an impersonation. A plane op cannot serve this: the plane is a
  unix socket inside cloud's pod. It is in `openapi/operator.go`'s `operated` set, so
  it is in no customer document, SDK or tool list. The caller asks once per request,
  each redirect hop included (hanzoai/bot `web-bot-auth.ts`).
- **The browser render** (`browse`) sends the renderer (hanzoai/crawl-v1) a signature
  per host it may start at, `crawler_config.request_signatures` (`signatures`, keyed
  by `botauth.AuthorityOf`). The renderer (`crawl/signature.py`) puts each on the
  main-frame navigation to its host, once, through a CDP Fetch client asked at every
  redirect hop (`page.route()` is not, and Chromium keeps a header across the
  redirect, so a signature for host A would reach host B). No signer or key sends
  no field; a renderer without the field drops it. The bot's browser tool (CDP from
  the gateway) is unsigned.
- **Wire:** Cloudflare's form (draft-meunier-http-message-signatures-directory-03):
  `Signature-Agent` is a bare sf-string naming the directory URL on the brand's API
  host, components `("@authority" "signature-agent")`, `tag="web-bot-auth"`, keyid the
  JWK thumbprint, a 64-byte nonce, valid 5 minutes. The later Dictionary form of
  Signature-Agent is what the IETF draft now asks for; Cloudflare's docs say it fails
  there today. The base and signature are pinned to Cloudflare's published vector.
- **The directory** is the JWK Set (`kty`, `crv`, `kid` = thumbprint, `x`), media
  `application/http-message-signatures-directory+json`, `Cache-Control: max-age=3600`,
  and is itself signed: one `("@authority";req)` signature per key, tag
  `http-message-signatures-directory`, so it proves the server holds every key it
  publishes. No signer is 404; a key store that cannot be read is 503, never an empty
  set a verifier would cache.
- **The key** is KMS `orgs/<brand>/botauth/WEB_BOT_AUTH_KEYS@prod`, a custodied
  subtree (apps/kms custody.go): read by the platform's processes, never by an org
  member over REST. The first directory request mints it when KMS holds none; a fetch
  never mints. The value is a SET, one base64url seed per line: the first signs, all
  are published, and a process re-reads it every 5 minutes.
- **Rotating** (SuperAdmin, through `/v1/kms` on the brand's org, audited): add the new
  seed as line two; wait an hour (the directory's max-age); move it to line one; wait
  an hour; delete the old line. A request signed with either key verifies throughout.
- **Registering** with Cloudflare (dashboard Bot Submission Form, "Request Signature",
  the directory URL) is what makes a signed request a verified bot there. Until then
  `https://crawltest.com/cdn-cgi/web-bot-auth` answers a well-formed signature from an
  unregistered key with 401.
