// Package plane is the internal call contract: the input and output of every
// op one app invokes on another, and the names those ops answer to.
//
// It is a LEAF. It imports nothing of cloud's, so both ends of a call can
// import it without either dragging the other's dependency graph in — which is
// the whole reason an aggregator can reach the ledger without linking it. One
// package, imported by both halves, so the two halves cannot drift; that
// property is what the hand-written wire codecs it replaces existed to hold,
// and it is now held by the type system instead of by matching byte offsets.
//
// # There is no Org field anywhere in this file
//
// The tenant a call acts for rides the CALLER, not the argument: forwarded from
// the gateway's assertion with [zip.Ctx.Forward], or stated once and explicitly
// by a background job with zip.WithCaller. An org in the argument is an org the
// caller chose, and a caller that can name the org can bill or read another
// tenant. The callee reads it with zip.CallerOf(ctx).Org and refuses an empty
// one.
//
// # The wire is ZAP; the tags are for the document
//
// These types cross as ZAP messages: a field IS its offset, and no name travels.
// The `json` tags name fields in the OpenAPI schema this plane also projects —
// they are the DOCUMENT's vocabulary, never the wire's. Because the layout is
// the type, the compatibility rule is structural: APPEND FIELDS AT THE END, and
// only at the end. Reordering, inserting or retyping one changes what every
// existing peer reads.
//
// # Money is an exact decimal, never a count of cents
//
// [Money] carries the amount's exact decimal text beside its currency code,
// which is what money.Amount round-trips without loss. A minor-unit integer
// cannot represent every currency booked here — HUSD carries 18 decimals, so
// "cents" is not even the smallest unit — and a second, lossy representation of
// one value is how books reconcile to a rounding difference nobody can find.
package plane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The op names. A caller and a callee that spell a name differently fail at the
// call rather than at compile time, so both ends read them from here.
//
// The token is the op's operationId, which is also its OpenAPI operation, its
// MCP tool name and its CLI command — one identity across every projection.
const (
	// SitesResolve / SitesResolveOrg answer "which published site is this host?"
	// for the site EDGE, which is the same reason FinanceScopeRules is here: the
	// reader is a cloud edge middleware and the owner of the fact is another app.
	//
	// It is on the plane because it HAS to be. The edge middleware and projects
	// (which owns the project store, and called sites.SetResolver at its Mount)
	// run in DIFFERENT processes — the pod boots ~25 single-app processes — so a
	// package-level registry is nil wherever it is consulted. Every published
	// site therefore resolved as not-found and fell through to the API pipeline,
	// and <slug>.hanzo.app served the console SPA. Measured at the pod, ingress
	// bypassed, 2026-08-03.
	SitesResolve    = "sites_resolve"
	SitesResolveOrg = "sites_resolve_org"

	FinanceAuthorize = "finance_authorize" // the prepaid gate
	FinanceBalance   = "finance_balance"
	// AIChat and AIEmbed are inference asked of the `ai` app BY NAME.
	//
	// They exist so a sibling never reaches `ai` by URL. `ai` is a plugin of the
	// same binary running as its own process, and the configured alternative was
	// its PUBLIC address — so a pod left through Cloudflare and minted an OAuth
	// token to authenticate to its own deployment, to reach code one socket away.
	// A plane op is addressed by app name (zip.SocketPath), so there is nothing
	// to configure and nothing to get wrong.
	AIChat  = "ai_chat"
	AIEmbed = "ai_embed"

	FinanceRecord    = "finance_record" // the meter
	FinanceTxns      = "finance_txns"
	FinanceUsage     = "finance_usage"

	// FinanceScopeRules reads the org's per-scope request-rate ceilings — the
	// rate-limited subset of its spend-alert rows. It is on the plane for the
	// same reason the balance is, plus one of its own: the READER is a cloud
	// EDGE middleware. Asking commerce for it over HTTP re-dispatched the whole
	// shared app back into this process, which re-ran that same middleware,
	// which asked again — an unbounded self-call the commerce transport's depth
	// guard turns into a 502 (and, before that guard, into a stack overflow).
	// A socket to the process that owns the rows has no edge chain on it at all,
	// so the recursion is not bounded here but structurally absent.
	FinanceScopeRules = "finance_scope_rules"

	KMSGet  = "kms_get"
	KMSPut  = "kms_put"
	KMSSign = "kms_sign"
	KMSDel  = "kms_delete"

	IAMMailable = "iam_mailable"

	GitFiles   = "git_files"
	GitImport  = "git_import"
	GitInbound = "git_inbound"
	GitPublish = "git_publish"

	PlatformFleet   = "platform_fleet"
	TreasuryReserve = "treasury_reserve"

	// The durable engine's org-scoped read, across the process boundary. Each app
	// embeds its OWN engine over its OWN data dir (durable.go: SQLite has one
	// writer, so a shared store would be the collision a shared port already was),
	// which makes "the engine" a per-process fact. A namespace written through one
	// app's surface is therefore invisible to every other app — and the BYO fleet
	// is exactly that: workers register through the tasks surface and visor renders
	// them, so visor read its own empty engine and reported an online GPU as no
	// fleet at all. The engine is asked, not opened, like the ledger above.
	TasksActivities = "tasks_activities"

	// The lexical index's read, across the process boundary. Same shape of bug as
	// the engine above, and it shipped as a 503 nobody could act on: `catalog`
	// guards its browse on index.Ready(), which reports whether the index is
	// mounted IN THIS BINARY — true when everything was one fused process, false
	// the moment catalog and index became two plugin rows. So /v1/catalog answered
	// {"status":503,"error":"catalog: index not mounted"} on every request, and
	// hanzo.app's Community page rendered "ERROR: CATALOG: 503".
	//
	// The index is asked, not opened: its store is one encrypted SQLite with a
	// single writer, so a second process opening the same file to read it is the
	// collision, not the fix.
	IndexQuery = "index_query"

	// The x402 rail, across the process boundary. Four ops, because the four
	// things a settlement needs live in four binaries: the RAIL is x402's, the
	// PRICE is the marketplace's, the PAYEE is wallets', and the LEDGER is
	// commerce's.
	X402Settle    = "x402_settle"    // settle one priced resource, or report it free
	MarketPrice   = "market_price"   // what a resource costs, and who is paid
	WalletsPayee  = "wallets_payee"  // resolve a payout wallet to an address + subject
	FinanceCredit = "finance_credit" // credit a subject's ledger (the payee side)

	// IntegrationsSlackSend posts to an org's Slack channel via the org's
	// KMS-custodied bot token. It lives on the plane because the token store is
	// the integrations PROCESS's alone — a peer plugin (o11y paging an alert)
	// cannot see integrations' in-memory `mounted` token map, so it asks the
	// process that owns it, over the socket, exactly like a debit asks commerce.
	IntegrationsSlackSend = "integrations_slack_send"

	// The observability plane's claim on the ONE event door. analytics owns POST
	// /v1/event and its subtree, but the o11y PROCESS owns the Sentry runtime —
	// so the door asks over the socket rather than through a package global,
	// which a peer process reads as nil (the 503 "error ingest not initialized"
	// that this replaces). A second op (obs_event_claim) offered every body to an
	// LLM-obs sink first; it retired with that sink.
	ObsErrorPost = "obs_error_post" // the Sentry envelope/store wire

	// HostStart is the fleet ROUTER's own op, not an app's. See [HostApp].
	HostStart = "host_start"
)

// HostApp is the socket name the fleet router answers on. It is not an app —
// there is no manifest row, no Mount and no prefix — it is the process that
// LOADS the apps, and the only one that can start one.
//
// It exists because a lazy app has exactly one trigger: a request reaching one
// of its prefixes. A plane call never touches the router, so an app reached only
// over its socket was never started and the socket was never bound. That is not
// a bug in laziness; it is a second door the loader has to open, and this names
// it.
const HostApp = "host"

// The three x402 wire headers, in the leaf because the settlement now crosses a
// process boundary: the process holding the REQUEST — where the proof arrives
// and the challenge must be written — is not the process holding the RAIL. Both
// ends read the names from here rather than one importing the other's subsystem.
const (
	// HeaderRequirements carries the PaymentRequirements on a 402 response.
	HeaderRequirements = "X-Payment-Required"
	// HeaderProof carries the client's signed authorization on the retry.
	HeaderProof = "X-Payment"
	// HeaderReceipt carries the settlement receipt on a served (2xx) response.
	HeaderReceipt = "X-Payment-Receipt"
)

// toolResourcePrefix namespaces a TOOL as an x402 resource. x402 resources are
// opaque ids and the Enforce middleware keys on request PATHS, so the prefix is
// what keeps the two key spaces from colliding: no path begins "tool:", so a
// price table can never accidentally price a route and a tool price can never be
// bought by hitting a URL.
//
// A tool needs an id at all because every tool call arrives on the SAME route,
// POST /v1/tools/call, with the tool named in the body — the path cannot say
// which capability is being bought.
const toolResourcePrefix = "tool:"

// ToolResource names one tool as an x402 resource. The tool plane builds it to
// ask what a dispatch costs; the marketplace keys its price table on it. One
// spelling, in the package both import, because a caller and a callee that spell
// a resource differently do not fail — they quietly agree the tool is free.
func ToolResource(tool string) string { return toolResourcePrefix + tool }

// ToolOf reads one back. ok=false means the resource is not a tool at all, which
// is every request path the payment middleware asks about.
func ToolOf(resource string) (string, bool) {
	name, ok := strings.CutPrefix(resource, toolResourcePrefix)
	return name, ok && name != ""
}

// Money is one amount, exactly. Decimal is the amount's own text and Currency
// its ISO-style code; the two travel together so they cannot be separated in
// transit and re-paired with the wrong unit.
type Money struct {
	Decimal  string `json:"decimal" validate:"required"`
	Currency string `json:"currency" validate:"required"`
}

// ---- finance.authorize — the GATE -----------------------------------------

// AuthorizeIn asks whether one spend may proceed.
//
// ProjectValidated travels because only the caller knows whether the project
// came from a signed claim or from a header a client could forge. A forgeable
// project must be able neither to hard-stop a request nor to evade a spend cap,
// so the provenance is carried rather than guessed at the far end.
type AuthorizeIn struct {
	Subject          string `json:"subject" validate:"required"`
	Amount           Money  `json:"amount"`
	Project          string `json:"project,omitempty"`
	Service          string `json:"service,omitempty"`
	ProjectValidated bool   `json:"projectValidated,omitempty"`
}

// Verdict is the gate's answer.
//
// Out of funds and a spent cap are DIFFERENT refusals with different remedies —
// one says add money, the other says wait for the period to roll over — so they
// are separate bits rather than one string a caller has to match on. Reason is
// neither: it is an upstream failure the caller must treat as UNKNOWN and fail
// closed on, and never read as permission.
type Verdict struct {
	OK       bool   `json:"ok"`
	NoFunds  bool   `json:"noFunds,omitempty"`
	CapSpent bool   `json:"capSpent,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ---- finance.record — the METER -------------------------------------------

// Usage is the attribution a debit carries beyond its amount.
type Usage struct {
	Model     string `json:"model,omitempty"`
	Project   string `json:"project,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Service   string `json:"service,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	ClientIP  string `json:"clientIp,omitempty"`
}

// RecordIn debits one metered act.
type RecordIn struct {
	Subject string `json:"subject" validate:"required"`
	Amount  Money  `json:"amount"`
	Usage   Usage  `json:"usage"`
}

// Recorded is what a debit reports back: the amount actually written. It is the
// debit's own figure rather than a balance, because the balance after a debit is
// a separate read and reporting a stale one here would invite a caller to trust
// it.
type Recorded struct {
	Amount Money `json:"amount"`
}

// ---- ai.chat / ai.embed ----------------------------------------------------

// ChatIn is one completion. Org is the EFFECTIVE tenant (the data scope: BYO
// provider keys, RAG); the billing scope stays with the CALLER, which meters
// before it asks — inference is priced where it is gated, not twice.
//
// MaxTokens is the ceiling the caller's prepaid gate reserved against. It rides
// the request so the servant cannot return a completion nobody paid for.
type ChatIn struct {
	Model     string `json:"model"`
	Prompt    string `json:"prompt" validate:"required"`
	Org       string `json:"org,omitempty"`
	Project   string `json:"project,omitempty"`
	MaxTokens int    `json:"maxTokens,omitempty"`
}

// ChatOut carries the completion and the token counts the caller settles on.
// Zero counts mean the servant reported no usage, and the caller falls back to
// its own estimate rather than treating the call as free.
type ChatOut struct {
	Content          string `json:"content"`
	PromptTokens     int    `json:"promptTokens,omitempty"`
	CompletionTokens int    `json:"completionTokens,omitempty"`
	TotalTokens      int    `json:"totalTokens,omitempty"`
}

// EmbedIn embeds Inputs in order; the result is one vector per input, aligned by
// index.
type EmbedIn struct {
	Model   string   `json:"model"`
	Inputs  []string `json:"inputs" validate:"required"`
	Org     string   `json:"org,omitempty"`
	Project string   `json:"project,omitempty"`
}

// EmbedOut is one vector per input, in the order they were given.
type EmbedOut struct {
	Vectors [][]float32 `json:"vectors"`
}

// ---- finance.balance -------------------------------------------------------

// BalanceIn reads one subject's spendable balance.
type BalanceIn struct {
	Subject  string `json:"subject"`
	Currency string `json:"currency" validate:"required"`
}

// Balance is what is left to spend.
type Balance struct {
	Amount Money `json:"amount"`
}

// ---- finance.usage / finance.txns — the statement -------------------------

// UsageRow is one recorded debit: what was metered, how much, and when.
type UsageRow struct {
	ID        string `json:"id"`
	Model     string `json:"model,omitempty"`
	Amount    Money  `json:"amount"`
	CreatedAt int64  `json:"createdAt"`
}

// UsageRows is a page of debits. It is the DATA, not a rendered view: the HTTP
// surface builds its own envelope from these, because sending the envelope
// would put the renderer in the same binary as the ledger.
type UsageRows struct {
	Rows []UsageRow `json:"rows"`
}

// Txn is one ledger entry.
type Txn struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Ref       string `json:"ref,omitempty"`
	Memo      string `json:"memo,omitempty"`
	Amount    Money  `json:"amount"`
	CreatedAt int64  `json:"createdAt"`
}

// Txns is a page of ledger entries.
type Txns struct {
	Rows []Txn `json:"rows"`
}

// ScopeRule is one scope's request-rate ceiling: the axes it covers and the
// requests/minute it allows. "" on an axis is the wildcard, so an org-wide row
// carries neither — the SAME covering rule the cap verdict reads, because both
// derive from one spend-alert row and a second spelling would let a rate limit
// and a spend cap disagree about which requests they bind.
type ScopeRule struct {
	Project      string `json:"project,omitempty"`
	Service      string `json:"service,omitempty"`
	RateLimitRpm int    `json:"rateLimitRpm"`
}

// ScopeRules is the org's whole rate-limit config in one reply. Only rows that
// SET a ceiling travel: a row with none is not a rule, and shipping it would
// make "no limit" and "a limit of zero" the same value on the wire.
type ScopeRules struct {
	Rules []ScopeRule `json:"rules"`
}

// ---- kms -------------------------------------------------------------------

// SecretIn names one secret, and carries its value on a write or the payload to
// sign. A ref is fully qualified: "orgs/<org>/…" names a tenant's material,
// anything else names the deployment's own.
type SecretIn struct {
	Ref   string `json:"ref" validate:"required"`
	Value []byte `json:"value,omitempty"`
}

// Secret is one secret's value, or a signature.
type Secret struct {
	Value []byte `json:"value,omitempty"`
}

// ---- iam.mailable ----------------------------------------------------------

// Recipient is one mailable person, projected to the four fields naming and
// reaching them takes. Handing over the whole identity record would put the
// credential columns on the wire to answer an audience count.
type Recipient struct {
	ID    string `json:"id"`    // the person's identity id, stable across a rename
	Owner string `json:"owner"` // the org that owns the record — the tenancy key
	Name  string `json:"name"`  // the person's name within that org, unique there
	Email string `json:"email"` // the address to reach them at
}

// Roster is who an org may mail.
type Roster struct {
	Recipients []Recipient `json:"recipients"` // everyone in the org who may be mailed; empty is a real answer, not an error
}

// ---- git -------------------------------------------------------------------

// ImportIn asks git to create a repo and mirror an upstream into it. It exists
// because the app that decides to import (integrations, holding the provider
// credential) and the app that owns the git store are DIFFERENT PROCESSES, so
// the in-process importer seam is nil across that boundary — the request has to
// travel.
type ImportIn struct {
	// Repo is the repository name to create locally.
	Repo string `json:"repo"`
	// Project is the sub-scope the repo lives in — the provider-side account for
	// an import, so two upstreams of the same name stay distinct.
	Project string `json:"project"`
	// CloneURL is the upstream to mirror from.
	CloneURL string `json:"cloneUrl"`
	// Token authenticates the fetch. It rides the internal socket only, and is
	// presented to git out of band (env-fed http.extraHeader), never argv.
	Token string `json:"token"`
	// MirrorURL registers an outbound mirror target; empty registers none.
	MirrorURL string `json:"mirrorUrl"`
}

// InboundIn advances ONE branch of a native repo from an upstream push. It rides
// the plane for the same reason ImportIn does: the app that receives the webhook
// and the app that owns the repos are different processes.
type InboundIn struct {
	// Project is the sub-scope — the provider-side account.
	Project string `json:"project"`
	// Repo is the native repository name.
	Repo string `json:"repo"`
	// Ref is the FULL ref, e.g. refs/heads/main or refs/tags/v1.2.3.
	Ref string `json:"ref"`
	// CloneURL is the upstream to fetch the ref from.
	CloneURL string `json:"cloneUrl"`
	// Token authenticates the fetch; env-fed downstream, never argv.
	Token string `json:"token"`
	// Origin is the source host, so the outbound mirror suppresses the echo.
	Origin string `json:"origin"`
}

// Synced reports what the fetch did. A divergence is NOT an error: native is
// canonical and was left alone, which the caller needs to know rather than retry.
type Synced struct {
	// Applied is true when native fast-forwarded.
	Applied bool `json:"applied"`
	// NoOp is true when native was already at that tip.
	NoOp bool `json:"noOp"`
	// Conflict is true when native had diverged and was NOT overwritten.
	Conflict bool `json:"conflict"`
	// Detail is the human reason for a conflict or a skip.
	Detail string `json:"detail,omitempty"`
	// Before and After are the native tips around an Applied fetch.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Imported acknowledges an import. A failure is an error, never this shape.
type Imported struct {
	// Repo names what was imported.
	Repo string `json:"repo"`
}

// FilesIn asks for a repo's files at one ref.
type FilesIn struct {
	Repo string `json:"repo" validate:"required"`
	Ref  string `json:"ref,omitempty"`
	Glob string `json:"glob,omitempty"`
}

// File is one file as git reports it. Truncated marks a file listed but larger
// than the read limit: its Data is absent, and a consumer assembling a COMPLETE
// set must refuse the whole read rather than proceed without it.
type File struct {
	Path      string `json:"path"`
	Data      []byte `json:"data,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Files is a repo's files at a resolved revision.
type Files struct {
	Rev   string `json:"rev"`
	Files []File `json:"files"`
}

// Visibility is one project's resolved publication state.
//
// Listed is the ONE derived answer — public and moderated — computed by the
// owner of that rule and never re-derived from parts here. Name and Description
// seed a repo the first time it is created and are never re-imposed, so an
// author who edits their own description keeps it.
type Visibility struct {
	Slug        string `json:"slug" validate:"required"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Listed      bool   `json:"listed"`
}

// ---- platform.fleet --------------------------------------------------------

// App is one deployed app as the platform observer sees it. Every field is a
// value the board prints; DriftSeverity is pre-rolled to the string the
// operator computed, because the board's only question is whether it is "ok".
type App struct {
	// Org is which org OWNS this app. On a reply that is a property of the thing
	// described, not a claim by the caller — a cross-org observer has to see it.
	Org         string `json:"org,omitempty"`
	Name        string `json:"name"`
	Env         string `json:"env,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Role        string `json:"role,omitempty"`
	Cluster     string `json:"cluster,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Health      string `json:"health,omitempty"`
	DeclaredTag string `json:"declaredTag,omitempty"`
	RunningTag  string `json:"runningTag,omitempty"`
	LatestTag   string `json:"latestTag,omitempty"`
	// Registry is the image repository the workload actually runs, which is what
	// the board's tier classification reads — a real property of the deployment,
	// never an operator-typed label.
	Registry      string `json:"registry,omitempty"`
	DriftSeverity string `json:"driftSeverity,omitempty"`
}

// Fleet is every app the observer can see for the calling org.
type Fleet struct {
	Apps []App `json:"apps"`
}

// ---- treasury --------------------------------------------------------------

// ReserveIn holds funds against a future spend.
type ReserveIn struct {
	Amount Money  `json:"amount"`
	Ref    string `json:"ref,omitempty"`
}

// Reserved reports what was held.
type Reserved struct {
	Amount Money `json:"amount"`
}

// ---- index.query — the lexical index, from another process ----------------

// IndexQueryIn names one index and one query. The ORG is the caller's, never an
// argument, exactly like every other op here — so a caller can only ever search
// its own corpus, and the public catalog is reached by asking AS the public org.
type IndexQueryIn struct {
	// UID is the index within the org (catalog rows all live in one).
	UID string `json:"uid" validate:"required"`
	// Q is the lexical query. Empty is a browse — every row, not none.
	Q string `json:"q,omitempty"`
	// Limit bounds the page; Offset walks it.
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// IndexQueryOut is the matching documents, as the index's OWN JSON relayed
// verbatim — the same reasoning as Activities.Rows: a struct here would be a
// second copy of a type this package does not own, free to drift from the one
// that produced the bytes.
type IndexQueryOut struct {
	Rows []json.RawMessage `json:"rows"`
}

// ---- tasks.activities — the durable engine, one page at a time -------------

// ActivitiesIn names one page of one namespace. The ORG is the caller's, never an
// argument, exactly like every other op here.
type ActivitiesIn struct {
	Namespace string `json:"namespace" validate:"required"`
	Cursor    string `json:"cursor,omitempty"`
	Size      int    `json:"size,omitempty"`
}

// Activities is one page of standalone activities, plus the cursor for the next.
//
// Rows is the engine's OWN JSON, relayed verbatim. The alternative is a struct
// here mirroring hanzoai/tasks' StandaloneActivity — a second copy of a type this
// package does not own, free to drift from the one that produced the bytes. The
// consumer already imports the engine and names the type; the plane only carries
// it. An empty Next ends the walk.
type Activities struct {
	Rows []byte `json:"rows"`
	Next string `json:"next,omitempty"`
}

// ---- x402.settle — the payment rail ----------------------------------------

// SettleIn enforces payment for one resource on behalf of the CALLING tenant.
//
// Proof is the client's signed authorization, verbatim off the request's
// X-Payment header. It travels as a field because the process that holds the
// request is not the one that holds the rail, and there is no second place a
// payer's proof could come from: the caller does not mint it and cannot alter it
// without invalidating the signature it is checked against.
//
// There is no amount and no payee here, deliberately. What a resource costs and
// who is paid are the price table's, resolved by the rail; a caller that could
// state them could buy a $1 tool for a cent or redirect the credit.
type SettleIn struct {
	Resource string `json:"resource" validate:"required"`
	Proof    string `json:"proof,omitempty"`
}

// Settled is ONE enforcement outcome, carried as data rather than as a transport
// error for the same reason [Verdict] is: a 402 carries the terms the client must
// read to pay, and an error body has no room for them.
//
// OK is the only field that means "serve it". Free says why it was OK — nothing
// was owed — so a caller can tell a settled call from an unpriced one without
// inferring it from an empty receipt. A transport error is NEITHER: it is
// unknown, and a caller must fail closed on it rather than read it as free.
type Settled struct {
	OK        bool   `json:"ok"`
	Free      bool   `json:"free,omitempty"`
	Receipt   string `json:"receipt,omitempty"`   // the X-Payment-Receipt header value
	Challenge string `json:"challenge,omitempty"` // the X-Payment-Required header value
	Status    int    `json:"status,omitempty"`    // the refusal's status: 402, 403 or 503
	Code      string `json:"code,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// ---- market.price — the price table ----------------------------------------

// PriceIn asks what one resource costs.
type PriceIn struct {
	Resource string `json:"resource" validate:"required"`
}

// Priced is a resource's payment terms, or the answer that it is free.
//
// RecipientOrg is on the REPLY and not on the request: it is a property of the
// listing — its publisher — never a claim by whoever is buying. That is the whole
// reason a buyer cannot redirect a credit.
type Priced struct {
	Priced            bool   `json:"priced"`
	Amount            Money  `json:"amount"`
	RecipientOrg      string `json:"recipientOrg,omitempty"`
	RecipientWalletID string `json:"recipientWalletId,omitempty"`
	Token             string `json:"token,omitempty"`
	Network           string `json:"network,omitempty"`
	ChainID           int64  `json:"chainId,omitempty"`
}

// ---- wallets.payee — who is paid -------------------------------------------

// PayeeIn resolves one payout wallet. The wallet's ORG rides the caller, stated
// with cloud.For from the listing row — so the lookup is scoped to the publishing
// org exactly as the in-process one is, and a wallet outside it cannot resolve.
type PayeeIn struct {
	WalletID string `json:"walletId" validate:"required"`
}

// Payee is a payout wallet resolved to the two things a settlement needs: the
// address the challenge names, and the ledger subject the credit is written to.
type Payee struct {
	Found   bool   `json:"found"`
	Address string `json:"address,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// ---- finance.credit — the payee side of a settlement -----------------------

// ObsErrorIn carries one Sentry-wire request across the plane. The DSN key rides
// the headers or the query, and the runtime authenticates it itself — there is no
// Hanzo principal on this path by design, which is why the whole request has to
// travel rather than just a tenant.
type ObsErrorIn struct {
	Path    string   `json:"path" validate:"required"`
	Query   string   `json:"query,omitempty"`
	Headers []Header `json:"headers,omitempty"`
	Body    []byte   `json:"body,omitempty"`
}

// Header is one request header, as a LIST element rather than a map entry.
//
// A map cannot cross this plane at all: zapenc carries scalars, strings, byte
// slices, structs, pointers and slices, and refuses anything else AT ENCODE so a
// field can never silently fail to arrive. Headers was a map[string]string, so
// every ObsErrorPost call failed inside zip.Call before it reached the socket —
// the Sentry envelope door answered 503 "error ingest unavailable" in dur_ms=0,
// for 24h+, with the peer up and the op registered. Its sibling op on the same
// socket (ObsClaimIn: two scalar fields) kept working throughout, which is
// exactly why POST /v1/event stayed 200 and only the envelope was dead.
//
// A slice of structs is the shape zapenc already carries — one complete ZAP
// message per element — so the list is not a workaround, it is the wire.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ObsErrorOut is the runtime's answer, relayed verbatim so a 401 stays a 401.
type ObsErrorOut struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType,omitempty"`
	Body        []byte `json:"body,omitempty"`
}

// SlackSendIn posts one message to an org's Slack channel. The ORG is the
// CALLER's (read from the plane context, never an argument): it selects which
// tenant's bot token sends, so a caller able to name it could post as another
// tenant. Channel and Text are required; Thread threads a reply when set.
type SlackSendIn struct {
	Channel string `json:"channel" validate:"required"`
	Thread  string `json:"thread,omitempty"`
	Text    string `json:"text" validate:"required"`
}

// CreditIn credits one subject's ledger.
//
// Ref is the idempotency key and is REQUIRED: this op exists to move the seller's
// half of a settlement, and a settlement that can be applied twice is not a
// settlement. Two credits carrying the same Ref move money at most once.
type CreditIn struct {
	Subject string `json:"subject" validate:"required"`
	Amount  Money  `json:"amount"`
	Ref     string `json:"ref" validate:"required"`
	Notes   string `json:"notes,omitempty"`
	Tags    string `json:"tags,omitempty"`
}

// Credited reports what the credit wrote.
type Credited struct {
	Amount Money `json:"amount"`
}

// ---- host.start — waking a lazy app ----------------------------------------

// StartIn names the app to bring up. It is the one plane input that names an APP
// rather than acting for a tenant: the router owns no tenant data, and starting a
// process is not a read of anyone's books.
type StartIn struct {
	App string `json:"app" validate:"required"`
}

// Started reports what the router did.
//
// Known is the load-bearing field and it is on a 200, not on a status code. "This
// fleet does not run that app" is the ONE answer a caller may read as free, and
// carrying it as a 404 made it indistinguishable from two other 404s on the same
// wire: zip's own "unknown op" when the router predates this op, and any framework
// 404 for the path. All three rebuild into the same *HTTPError with an empty Code,
// so only the message text differed — and matching on text is not a fact.
//
// That mattered on a rolling deploy. A host pod on an older build answers "unknown
// op: host_start", which as a status is 404, which as a fact would have been "not
// deployed here" — and a payment rail reading that concludes nothing is priced and
// serves every priced tool free, fleet-wide, for the whole skew window.
//
// So an answer states the fact and EVERY error is an outage. A router that cannot
// answer this op cannot claim anything about the fleet.
type Started struct {
	Addr  string `json:"addr"`
	Known bool   `json:"known"`
}

// ---- the runtime directory both halves must agree on ------------------------

// BindRuntimeDir points zip's socket resolution at the SHARED runtime directory and
// returns it.
//
// It lives in this leaf because both halves need it and neither may import the other:
// a callee resolves where to LISTEN and a caller resolves where to DIAL, from the same
// rule, or they miss each other in a way nothing reports. That is not hypothetical —
// with the binding done only on the caller's side, plugins listened on private temp
// paths (/tmp/zip-commerce-*/commerce.sock) while callers dialed
// /var/lib/cloud/run/commerce.sock, and a stale socket file at the shared path turned
// the miss into "connection refused" — which reads like the callee is DOWN rather than
// somewhere else. Every cross-process call was unreachable, and because the money ops
// are fail-closed, every AI completion answered 503 on a healthy fleet.
//
// Idempotent, and an externally-set ZIP_RUNTIME_DIR always wins — the operator's
// choice is not ours to overwrite, and both halves read the same one either way.
func BindRuntimeDir() string {
	if cur := strings.TrimSpace(os.Getenv("ZIP_RUNTIME_DIR")); cur != "" {
		return cur
	}
	dir := "/run/hanzo"
	// CLOUD_RUN_DIR overrides where app sockets live; default {CLOUD_DATA_DIR}/run.
	if v := strings.TrimSpace(os.Getenv("CLOUD_RUN_DIR")); v != "" {
		dir = v
	} else if v := strings.TrimSpace(os.Getenv("CLOUD_DATA_DIR")); v != "" {
		dir = filepath.Join(v, "run")
	}
	_ = os.Setenv("ZIP_RUNTIME_DIR", dir)
	return dir
}

// SiteIn names a published site to resolve: the host label for the multi-tenant
// product URL, or a bound custom domain. Org is set ONLY by the first-party
// path (ResolveOrg), which pins the lookup to one org so an internal host is
// never served by a customer's same-named project.
type SiteIn struct {
	Slug string `json:"slug"`
	Org  string `json:"org,omitempty"`
}

// Site is a published site's serving facts. Found is explicit: a site that does
// not exist is a clean answer, not an error, and the edge must be able to tell
// "no such site" (honest 404) from "the owner could not be reached" (503) —
// collapsing them is how a transient failure would start serving 404s for real
// customers' live sites.
type Site struct {
	Found                bool   `json:"found"`
	Org                  string `json:"org"`
	Slug                 string `json:"slug"`
	Bucket               string `json:"bucket"`
	Prefix               string `json:"prefix"`
	Status               string `json:"status"`
	CrossOriginIsolation bool   `json:"crossOriginIsolation"`
}
