# LLM.md — apps/destination

Guidance for apps/destination. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Conversions reach Google and Meta from the bus (`apps/destination`)

The destination app is a durable consumer (`destination`) on the EVENT stream.
It was an in-process `event.AddSink`, which never fired: ingest runs in the
event process and the sink registered in the destination process.

- **Money is stated, never typed.** A browser can post any value under any name
  (the publishable key is in every page; a person's session is in their hands).
  Only a conversion stated by a peer on the plane wire (`source: plane`: commerce
  sales, CRM leads) or by the org's own secret key (`vouched` on the envelope,
  set at admission for an `sk-` bearer) carries money to a third party. Anything
  else goes with its value and item prices zeroed, and a purchase not at all.
- **What reaches an ad platform**: moments it optimises on (`adStandard`), with
  consent. No page views, no custom events. GA4 gets only what its own tag did not
  deliver (`tags` lacks `ga`); it dedups nothing but a purchase's
  `transaction_id`. Meta dedups on `event_id` and gets every one.
- **Lenses are not destinations of their own plane**: Insights and Analytics are
  sent nothing unless the org runs its own host, and then get one 5s attempt.
  Every lens payload carries `$forwarded`; an envelope carrying it is never
  forwarded.
- **Consent** rides each browser event as `consent` ("analytics,marketing,ads";
  empty is a refusal of all). Absent reads as granted (opt-out). Marketing gates
  ad platforms, Analytics gates GA4. Ads off sends Meta LDU (country 1, state
  1000: a server event has no IP to place it) and GA4 `ad_personalization:
  DENIED`. Google Ads gets `consent.adUserData` only, and only when stated.
- **Platform revenue**: an `order_completed` stated on the plane with product
  `commerce` goes to the brand org's (`hanzo`) destinations and no other org's;
  a SuperAdmin's own books are skipped. Commerce states only live money — the
  books the receipt (a top-up) or the sale (`Sale.Test`) says the charge went to,
  nothing it cannot read, nothing off mainnet — a plan sale at the amount off its
  receipt, renewals and auto-reloads (`recurring`) at the card's part alone, in
  the currency's own scale.
- **Touch** (`touch.go`): last click within 90 days (`lookback`), kept per
  (org, person IAM sub), only for an org with a destination or the brand org.
  A sale names the buyer and the wallet (`payer`); the touch is also kept under
  `payer:<wallet>`, which no browser event may write, so a renewal finds it. A
  refused consent category sticks for the touch's life (pruned 13 months after
  the person was last seen): public keys write touches, so a later grant there
  cannot re-enable server sends. The later click wins by when it happened.
  Fields and the person key are clipped; an org holds 100k people and forgets
  the one seen longest ago to take a new one. The limit, stated:
  whoever knows a person's IAM sub (a UUID) can move their recorded click and
  email hash, which misattributes Hanzo's own conversions.
- **The consumer never waits on a platform.** Plan in line, queue each send on
  its lane and ack. A lane is (org, platform, stated or not): a flood of what
  anyone can send never queues ahead of a sale commerce stated. 4 workers and
  256 waiting per lane, 1024 waiting in all, envelopes over 32 KiB not planned;
  a send past a bound is a `destination_sent` fact with status failed, written
  off the consumer's thread. Retry 1s/4s/16s on transport, 429 and 5xx, 10s
  each; stopping finishes the attempt under way, starts no retry, and records
  what was still queued. A deleted durable is made again.
- **What the warehouse keeps**: `em` (an email's SHA-256) is scrubbed like
  `email`; the bus envelope carries it raw to this consumer alone. GA4 gets
  `client_id` (the page's `_ga`, else a hash) and `user_id` hashed.
- **Test** (`POST /v1/destination/:platform/test`) records nothing where the
  platform can validate: GA4's validation server, Google Ads `validateOnly`
  (an error when no action is configured), Meta under the Test Event Code
  (which rides tests only). Elsewhere a page view.
- **Credentials** are KMS only, per org, sealed by `POST /v1/destination/:platform`:
  `/orgs/<org>/destinations/meta` `access_token`, `.../ga4` `api_secret`,
  `.../google-ads` `oauth2` (JSON: developer_token, client_id, client_secret,
  refresh_token). Google Ads names one action per moment: purchaseAction,
  signupAction, leadAction, checkoutAction, renewalAction.
- **Leads**: `POST /v1/marketing/leads` takes the page's `visit` (click, consent,
  browser ids, url) and states `generate_lead` with the email's SHA-256 as `em`.
  A job application (a body naming `job`) is a candidate and is stated nowhere.
