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
// # Money is an exact decimal, never a count of cents
//
// [Money] carries the amount's exact decimal text beside its currency code,
// which is what money.Amount round-trips without loss. A minor-unit integer
// cannot represent every currency booked here — HUSD carries 18 decimals, so
// "cents" is not even the smallest unit — and a second, lossy representation of
// one value is how books reconcile to a rounding difference nobody can find.
package plane

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

	IAMMailable = "iam_mailable"

	GitFiles   = "git_files"
	GitPublish = "git_publish"

	PlatformFleet   = "platform_fleet"
	TreasuryReserve = "treasury_reserve"
)

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
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Roster is who an org may mail.
type Roster struct {
	Recipients []Recipient `json:"recipients"`
}

// ---- git -------------------------------------------------------------------

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
