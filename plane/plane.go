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
	FinanceAuthorize = "finance_authorize" // the prepaid gate
	FinanceBalance   = "finance_balance"
	FinanceRecord    = "finance_record" // the meter
	FinanceStarter   = "finance_starter"
	FinanceTxns      = "finance_txns"
	FinanceUsage     = "finance_usage"

	KMSGet  = "kms_get"
	KMSPut  = "kms_put"
	KMSSign = "kms_sign"
	KMSDel  = "kms_delete"

	IAMMailable = "iam_mailable"

	GitFiles   = "git_files"
	GitImport = "git_import"
	GitPublish = "git_publish"

	PlatformFleet   = "platform_fleet"
	TreasuryReserve = "treasury_reserve"

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

// ---- finance.starter — the welcome grant ----------------------------------

// StarterIn issues the opening credit for an org, once.
type StarterIn struct {
	Subject string `json:"subject,omitempty"`
}

// Granted reports what the grant issued. A zero amount with no error is the
// legitimate "already granted" answer, not a failure.
type Granted struct {
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
