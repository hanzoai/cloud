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

	// ProjectsResolveKey answers "which project minted this publishable ingest
	// key?" for the analytics ingest door — here for the same reason as the two
	// above, and between the same two processes: the door serves api.hanzo.ai and
	// the key lives in the project store.
	ProjectsResolveKey = "projects_resolve_key"

	// ProjectsOwnership answers "does this org own the project this request
	// claims?" for the identity trust boundary. The boundary is cloud edge
	// middleware in EVERY process; the registry is the projects app's alone. With
	// no resolver the guard returned "not foreign" — so the cross-org project
	// impersonation check was off wherever projects was not co-resident, which is
	// everywhere. Absence of an ANSWER may never read as permission.
	ProjectsOwnership = "projects_ownership"

	FinanceAuthorize = "finance_authorize" // the prepaid gate
	FinanceBalance   = "finance_balance"
	FinanceRecord    = "finance_record" // the meter
	FinanceTxns      = "finance_txns"
	FinanceUsage     = "finance_usage"

	// FinanceSpend is the org's TOTAL over a window — what it consumed, beside
	// what its wallet still holds. It is the qualify signal three attributed-
	// credit programs read (referrals, affiliates, authors), the month-to-date
	// figure on the usage page, and the per-org money row every admin aggregator
	// folds over.
	//
	// It is one op rather than each caller summing FinanceUsage, because the
	// total and the rows are DIFFERENT questions with different costs: the cap
	// already reads the ledger's own windowed sum, and a caller that re-derived
	// it from a page of rows would silently answer for one page.
	FinanceSpend = "finance_spend"

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

	// IAMApproval answers "is this person off the waitlist?" about the CALLER.
	//
	// It is on the plane because the alternative was worse than a URL. admission
	// asked IAM over HTTP and, having no way to say who was asking, REPLAYED the
	// caller's own Cookie and Authorization header to do it — cloud presenting a
	// user's raw credential to another service so that service would answer about
	// that user. The plane carries the validated principal, so the credential
	// never has to be handled, let alone forwarded.
	IAMApproval = "iam_approval"

	// IAMProjects lists the projects an org owns, from the store that owns them.
	//
	// A project is IAM's noun. Platform reads it because a PaaS app is scoped to
	// one, and platform used to reach for it two different ways depending on where
	// IAM was: the in-process store when this binary IS the IAM, and — when it is
	// not — an HTTP GET to /v1/iam/projects authenticated as a per-org machine
	// identity that platform MINTED for the purpose. The second one is what this
	// replaces, and the credential goes with it: the plane already carries the
	// caller's tenant, so an org-scoped read needs no identity of its own to
	// present. The narrowest possible grant is the one you never have to issue.
	IAMProjects = "iam_projects"

	// TeamMember answers "what is this person's role in that workspace?" for a
	// caller that holds an IAM identity and no workspace claim.
	//
	// It is on the plane because the process that DECIDES a room join (meet) and
	// the process that owns the workspace membership rows (team) are different
	// ones. Before it, meet could only read a role a workspace token had signed —
	// which is the second bearer authority the estate is retiring, so the decision
	// had nowhere else to come from.
	TeamMember = "team_member"

	// TeamWorkspaces answers "which workspaces is this person in?" — the same
	// rows TeamMember reads, asked without already knowing the answer.
	//
	// A caller that must DECIDE a join asks TeamMember, because it already holds
	// the workspace the room named. A caller that must OFFER a join has nothing
	// to name yet: the meet lobby has to show a person the workspaces they can
	// open a room in, and a room is bound to its tenant by its name's leading
	// workspace segment, so without this the native client could only ask the
	// user to type a uuid it has no way to know.
	//
	// It is a person's OWN memberships and never a workspace's roster: the
	// subject is the caller's, the org rides the call, and the answer is the
	// list of rows that person holds. Nothing here tells one member about
	// another.
	TeamWorkspaces = "team_workspaces"

	GitFiles   = "git_files"
	GitImport  = "git_import"
	GitInbound = "git_inbound"
	GitPublish = "git_publish"

	// GitRev answers which commit a ref names, and nothing else.
	//
	// It is separate from GitFiles because the two questions have different
	// COSTS, not merely different shapes. A caller that pins a revision on every
	// request — the language-server proxy asks one per position query — would
	// otherwise have to read a whole tree to learn a sha, which is a monorepo
	// crossing a socket to answer forty bytes. Resolving is a ref lookup; reading
	// is a walk. One op each.
	GitRev = "git_rev"

	// GitStatus reads the per-repo import/sync status the console repo list
	// renders. Same boundary as GitImport: the app that lists the repos is
	// integrations, the app that knows whether one is imported is git.
	GitStatus = "git_status"

	// GitGrant / GitRevoke delegate — and withdraw — the right to create ONE ref
	// in ONE repository. git owns refs, so git decides who may write one, and an
	// orchestrator that dispatches untrusted work asks for this instead of
	// carrying the org's git credential. See apps/git/grant.go.
	GitGrant  = "git_grant"
	GitRevoke = "git_revoke"

	// GitMirror declares (or removes) a native repo's OUTBOUND mirror target. The
	// sync engine decides a mirror should exist; the git app owns the repos and
	// the reactor that pushes them. Two processes, so declaring it was a nil call
	// that reported "git mirror controller not registered" while git was healthy
	// next door.
	GitMirror = "git_mirror"

	// SyncRun dispatches one reconcile to the universal sync engine. The TRIGGERS
	// are webhooks arriving at integrations and pushes landing at git; the ENGINE
	// is the sync app. Three processes, one of which has the engine.
	SyncRun = "sync_run"

	// TrackerUpsert mirrors one external work item into the native tracker. The
	// feeder is integrations (it holds the GitHub App); the store is the tracker
	// app's.
	TrackerUpsert = "tracker_upsert"

	// PlatformPush turns a landed push into a build. The push lands on git's
	// embedded server and the builder belongs to platform — the single most
	// consequential split in this list, because the seam it replaces returned a
	// NIL ERROR: every push in the split fleet triggered no build and said so to
	// nobody.
	PlatformPush = "platform_push"

	// PlatformRelease patches a proven image onto its operator Service CR. Same
	// shape as PlatformPush and the same former silence: a nil error meant "rolled
	// out" to a caller whose CR was never touched.
	PlatformRelease = "platform_release"

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

	// The lexical index's WRITE, across that same boundary and for that same
	// reason — because Reconcile serves out of the same process-level global the
	// read did, and the read is the only half that was ever given a way across.
	//
	// The half that was left behind is the half that FILLS the corpus. `catalog`
	// reconciles hourly from GitHub and the sites table, and every pass since the
	// split ended at "index: not mounted" — so the corpus was never written once,
	// and GET /v1/catalog answered 200 with {"data":[],"total":0}. A well-formed
	// page of nothing reads as a young platform rather than a broken one, which is
	// why it went unnoticed far longer than the 503 the read leg failed with.
	//
	// Fixing the read alone could not have shown a single row: it was reading a
	// store nothing had ever put anything into.
	//
	// ONE writer, still. The swap runs inside the process that owns the file,
	// exactly as it always did — only the request for it crosses the boundary.
	// The index is asked, not opened, and that is precisely what keeps the single
	// writer single: a second process opening that SQLite to write it is the
	// collision, not the fix.
	IndexReconcile = "index_reconcile"

	// The cross-org live-site read, across the process boundary — the catalog's
	// OTHER source, broken by the same split and even more quietly.
	//
	// projects.LiveSites answers nil when the package is not mounted, on the
	// reasoning that "a deployment that does not host sites is not an error". That
	// is true of a DEPLOYMENT and false of a PROCESS: in the catalog process it is
	// not that nothing is serving, it is that the wrong half of the fleet was
	// asked. So the corpus lost every live site — the demo URLs, the `site` kind,
	// and the template lane's own deployed starters — and reported no error at all,
	// because nil and empty are the same answer here.
	//
	// It takes no org, exactly like sites_resolve above and for a reason of the
	// same shape: this is THE cross-org read, and the rule that makes it safe is
	// applied in the query by the app that owns the store — public visibility,
	// live status, not hidden. There is no tenant here for a caller to widen into.
	SitesLive = "sites_live"

	// The login-manager teardown, across the process boundary. A credential
	// revoke lives in link and the sessions that ran under it live in agents,
	// and the two ship as separate binaries — so the in-process call link made
	// found an unmounted package every time and answered (0, nil). The revoke
	// returned 200 {"sessionsStopped":0} while the sessions kept running under
	// the revoked account, and nothing anywhere said otherwise.
	//
	// The org is the CALLER's plane identity and never an argument, for the
	// sharpest version of the usual reason: this op tears sessions down, so a
	// caller able to name the org could tear down a co-tenant's. The actor is
	// derived from the caller's org and the revoking subject on the ANSWERING
	// side, which is what keeps a revoke bounded to its own user's sessions.
	AgentsSessionsStop  = "agents_sessions_stop"
	AgentsSessionsCount = "agents_sessions_count"
	// AgentsRunOnBehalf is the chat bridges' door onto a run. A plugin is a
	// PROCESS, so agents.RunOnBehalf — which gates on that package's `mounted`
	// global — can only ever answer when agents happens to be co-resident. It was
	// not, and every @hanzo turn in Slack died on ErrNoPeer.
	AgentsRunOnBehalf = "agents_run_on_behalf"

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

	// EventCapture states ONE occurrence onto the shared event plane, for an app
	// running in another binary.
	//
	// It is the WRITE side of the door ObsErrorPost claims a slice of, and it is
	// here for the same reason: analytics owns POST /v1/event, event.fact and the
	// /v1/insights reads over them, and the pod forks one process per app — so a
	// peer that wanted its own facts queryable beside the product's had no way to
	// state one. In-process there is a write core (ingestEvents) and it is
	// unreachable from another pid; over HTTP there is the public edge, which is a
	// second gate, a second credential and a hop through the fleet's own front
	// door to reach a table one socket away.
	//
	// So a peer ASKS the app that owns the plane, exactly as a debit asks commerce
	// and a decision asks risk. The tenant is the CALLER's, minted from the plane
	// principal, so a peer can only ever write into its own organisation's
	// partition — the same rule the HTTP door holds, at the other door.
	EventCapture = "event_capture"

	// RiskDecide judges one subject at one lifecycle moment against that
	// organisation's OWN model, for a gate running in another binary.
	//
	// It is on the plane for the reason ObsErrorPost is, and it is the same
	// mistake caught one layer earlier. cloud.SetRiskScorer hands a scoring
	// FUNCTION to a process-global, so it arms the process that installs it and
	// no other — and the risk model is in-process mutable state (apps/risk: one
	// binary learns and scores, or two hold different masses and answer one
	// question two ways), so the app that can install it is the app no other
	// process links. Every gate that is not the risk child therefore read nil and
	// allowed, unscored, fleet-wide.
	//
	// So the scorer is REACHED rather than linked: one model, one process, asked
	// over the socket. cloud.Decide's fail policy is unchanged by the distance —
	// a peer that is not deployed is ABSENT and allows, a peer that is here and
	// does not answer is an outage and denies a privileged grant.
	RiskDecide = "risk_decide"

	// RiskObserve teaches one organisation's own model from something that
	// HAPPENED, for a gate running in another binary.
	//
	// It is RiskDecide's other half and the plane needs both, because a model that
	// is only ever asked is a model that never learns. The published learn door
	// (POST /v1/risk/learn) is an ORGANISATION teaching its own model over HTTP; it
	// is the wrong door for the fact this op carries. A settled payment is observed
	// by the process that took the money, in the same request, and what it observed
	// must reach the model from a source the PAYER cannot move:
	//
	//	THE SERVER STATES IT, not a body. The value is the amount that SETTLED, the
	//	moment is the server's clock, and the subject is the one the charge
	//	credited. A payer who could state these could state a small payment for a
	//	large charge, which is the velocity bound switched off by the party it
	//	exists to bound.
	//
	//	IT IS KEYED ON THE SETTLEMENT. Settlement is at-least-once — a retried
	//	request, a replayed webhook, a redelivered event — so the observation
	//	carries the settlement's own identifier and the model's record
	//	deduplicates on it. Velocity that double-counted a retry would freeze a
	//	customer for paying once.
	//
	// Without it the aggregate halves of the credit door's rule are structurally
	// dead: a fresh organisation is its own payer, so nothing it does teaches the
	// model anything, and pace and fan-out read an empty history for exactly the
	// self-serve fraudster they were built for.
	RiskObserve = "risk_observe"

	// HostStart is the fleet ROUTER's own op, not an app's. See [HostApp].
	HostStart = "host_start"

	// The SANDBOX ops — the one compute primitive, reachable from a peer.
	//
	// They exist for the reason every op above them does: the two ends are two
	// PROCESSES. apps/exec serves the code-interpreter contract and apps/sandbox
	// owns the pods, they ship as separate plugin binaries, and a Go import would
	// not have joined them — it would have given exec its OWN sandbox Service, with
	// its own per-org SQLite handles on the same files and its own reaper racing the
	// real one. So the call crosses, and [Ask] already collapses it to a direct
	// in-process dispatch wherever the fleet happens to fuse the two.
	//
	// The op set is the sandbox's whole vocabulary and nothing more: lease one, run
	// in it, read a path, write a path, end it. There is no "upload", no "download"
	// and no "session" here, because those are the CALLER's nouns — a session is a
	// lease and a download is a read, and putting either word on this plane would
	// publish one product's vocabulary as another's contract.
	SandboxLease = "sandbox_lease"
	SandboxRun   = "sandbox_run"
	SandboxRead  = "sandbox_read"
	SandboxWrite = "sandbox_write"
	SandboxStop  = "sandbox_stop"
	SandboxEnd   = "sandbox_end"

	// The FIGURES seam: one question — "what are this org's headline numbers?" —
	// asked of every domain that can answer it, at one address each.
	//
	// It is one op repeated rather than one op shared because the answer is each
	// app's OWN: books alone knows what a dollar of revenue is, git alone knows
	// what a repository is, projects alone knows what deployed means. What they
	// share is the SHAPE of the reply ([FiguresOut]), so the advisor that reads
	// them needs no per-domain branch — a new domain is a new op and a new line,
	// never an edit to the reader.
	//
	// They are on the PLANE, not in a package, because /v1/ask is its own plugin
	// process: the pod forks one process per app, so the advisor and every domain
	// it asks are separate pids. An in-process read reaches only routes the ASK
	// binary mounts, which is /v1/ask and nothing else — which is why the advisor
	// answered every question from its fallback while books sat healthy next door.
	BooksFigures    = "books_figures"
	GitFigures      = "git_figures"
	ProjectsFigures = "projects_figures"
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
// process boundary: the process holding the REQUEST — where the payment arrives
// and the challenge must be written — is not the process holding the RAIL. Both
// ends read the names from here rather than one importing the other's subsystem.
//
// These are the x402 PROTOCOL VERSION 2 names, and they carry BASE64-ENCODED JSON
// (specs/transports-v2/http.md). The v1 spellings — X-PAYMENT, X-PAYMENT-RESPONSE
// — are gone rather than aliased: a header a client may send under either name is
// two wires, and the one the server forgot to read is the one where a payer pays
// and is never served.
const (
	// HeaderPaymentRequired carries the base64 PaymentRequired on a 402 response.
	HeaderPaymentRequired = "PAYMENT-REQUIRED"
	// HeaderPaymentSignature carries the client's base64 PaymentPayload on the retry.
	HeaderPaymentSignature = "PAYMENT-SIGNATURE"
	// HeaderPaymentResponse carries the base64 SettlementResponse on the answer.
	HeaderPaymentResponse = "PAYMENT-RESPONSE"
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
	Model    string `json:"model,omitempty"`
	Project  string `json:"project,omitempty"`
	Provider string `json:"provider,omitempty"`
	Service  string `json:"service,omitempty"`
	// Ref is the SERVER-assigned name of the metered act, and the debit's idempotency
	// key within the subject's wallet: a peer re-sending the same act debits once. It
	// carries an identity the sending process already holds (a message row's id, a
	// settlement id), never a header a client chose — the ledger dedups on it, so the
	// payer must not be the one who picks it. Empty lets the ledger mint the entry's
	// own and the debit stands alone.
	Ref string `json:"ref,omitempty"`
	// RequestID is the call's CORRELATION id, for tracing a debit back to the request
	// that made it. Attribution only: it is NOT the idempotency key (see Ref).
	RequestID string `json:"requestId,omitempty"`
	ClientIP  string `json:"clientIp,omitempty"`
	// Actor is WHO acted, when that is not the wallet being billed — an admin or an
	// agent running on behalf of the payer. The subject says whose money moved; this
	// says whose hand moved it, and a ledger entry without it cannot answer the only
	// question an audit asks. It crossed on the old HTTP body (`actor`) and had no
	// field here, so the split-deploy debit landed unattributed.
	Actor string `json:"actor,omitempty"`
	// The token counts the charge was computed from. They are the WORK the amount
	// prices, so a debit without them can be re-read but not re-derived — and they
	// likewise had no field here.
	PromptTokens     int `json:"promptTokens,omitempty"`
	CompletionTokens int `json:"completionTokens,omitempty"`
	TotalTokens      int `json:"totalTokens,omitempty"`
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

// TxnsIn selects which books to read and how much of them.
//
// Test picks the SANDBOX ledger. Sandbox money and real money live in
// physically separate files and must never mix, so the selector travels rather
// than being inferred at the far end: a reader that posts test rows into real
// revenue has restated the company's income, and nothing downstream can tell.
//
// Limit is a page size; 0 takes the ledger's own default. There is no org and
// no subject, for the usual reason — the tenant rides the caller.
type TxnsIn struct {
	Test  bool `json:"test,omitempty"`
	Limit int  `json:"limit,omitempty"`
}

// Txns is a page of ledger entries.
type Txns struct {
	Rows []Txn `json:"rows"`
}

// ---- finance.spend — the totals -------------------------------------------

// SpendIn asks what an org has consumed, and since when.
//
// There is no subject and no org, for the usual reason: the tenant rides the
// caller. Since is a unix second and 0 means the calendar month to date, which
// is the window every existing reader of this figure asks for.
type SpendIn struct {
	Since int64 `json:"since,omitempty"`
}

// Spend is one org's metered consumption over that window, beside the wallet it
// is drawn from.
//
// Consumed is the LEDGER'S OWN windowed sum — the same figure the rolling
// spend cap reads, so a program that qualifies on spend and a gate that stops
// it cannot disagree about how much was spent. The ledger reports that sum to
// the cent and this carries it as an exact decimal rather than inventing a
// precision it never had.
//
// There is ONE balance field because the ledger has one number: what it calls
// available IS the settled balance (a hold is the caller's own in-pod
// reservation, never a ledger row). Two fields would be two names for one
// value, which is how a reader comes to subtract one from the other.
type Spend struct {
	Consumed Money `json:"consumed"`
	Balance  Money `json:"balance"`
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

// ---- iam.approval ----------------------------------------------------------

// Approval is the caller's waitlist state, as the identity store holds it.
//
// Status is the RAW approvalStatus string, not a verdict. What the value MEANS —
// that only the exact word "pending" gates a person, and an absent value reads as
// approved — is admission's policy, and it stays in admission next to the gate it
// decides. A boolean here would move that rule into the identity store and leave
// two places able to disagree about who is on a waitlist.
//
// An empty Status is a real answer: the person has no approvalStatus recorded.
// "I could not tell" is an error from the call, never a value in this struct.
type Approval struct {
	Status string `json:"status"`
}

// ---- iam.projects ----------------------------------------------------------

// Project is one project, projected to what a caller outside IAM can act on:
// the (org, name) key that scopes an app, the display name and description a
// console renders, and when it was made.
//
// The identity record's other columns — workspace, tags, metadata, the default
// flag — stay in IAM. A peer that needed one of them would be reaching past the
// question it asked, and every field added here is a field the wire's positional
// layout pins forever (see zapenc: a field IS its offset).
type Project struct {
	Owner       string `json:"owner"`       // the org that owns it — the tenancy key
	Name        string `json:"name"`        // the slug, unique within the org
	DisplayName string `json:"displayName"` // the human name; may be empty, and the caller falls back to Name
	Description string `json:"description"`
	CreatedTime string `json:"createdTime"` // RFC3339, as IAM stores it; empty when IAM has none, never a fabricated time
}

// Projects is every project one org owns.
//
// The org is not in the reply and is not an argument: it is the caller's own
// tenant, taken from the call. An empty list is a real answer — an org with no
// projects — and never the shape a failed read takes.
type Projects struct {
	Projects []Project `json:"projects"`
}

// ---- team ------------------------------------------------------------------

// MemberIn names the workspace and the person a membership question is about.
// The ORG is not here and cannot be: it is the tenancy key of every workspace
// row, so a caller able to pass it could read another tenant's roster.
type MemberIn struct {
	// Workspace is the workspace uuid, scoped to the caller's org on the read.
	Workspace string `json:"workspace"`
	// Subject is the IAM subject, NOT a team account id. team owns the join from
	// one to the other — it is the join that created the rows — so a peer that
	// computed its own would be a second derivation of the same address, which is
	// how two layers end up naming different accounts for one person.
	Subject string `json:"subject"`
}

// Member is what the rows say. Role and Account are empty exactly when Member is
// false, so a caller cannot mistake "no row" for a role or an identity.
type Member struct {
	// Member reports whether the subject holds a row in that workspace.
	Member bool `json:"member"`
	// Role is the workspace role on that row (owner | admin | member | guest).
	Role string `json:"role"`
	// Account is the team AccountUuid the subject resolved to — the identity the
	// asking process attributes the person by, so it never derives one itself.
	Account string `json:"account"`
}

// WorkspacesIn names the person a workspace list is about. The ORG is absent for
// the same reason it is absent from MemberIn: it is the tenancy key of every
// workspace row, so a caller able to pass it could enumerate another tenant's.
type WorkspacesIn struct {
	// Subject is the IAM subject, NOT a team account id — team owns the join from
	// one to the other, exactly as in MemberIn.
	Subject string `json:"subject"`
}

// Space is one workspace a person holds a member row in.
//
// The ROLE is on it because the asking process decides with it: meet admits a
// privileged member and refuses a guest, and it applies that rule to the list it
// offers as well as to the join it grants, so a person is never shown a room
// they would then be refused.
type Space struct {
	// UUID is the workspace's stable id — and, in meet, the leading segment of
	// every room name bound to it.
	UUID string `json:"uuid"`
	// Name is the human label for a picker.
	Name string `json:"name"`
	// Role is the role on the caller's member row (owner | admin | member | guest).
	Role string `json:"role"`
}

// Spaces is what the rows say about one person: the account they resolved to and
// the workspaces they are in.
//
// The invariant is ONE-WAY: a non-empty Items implies a non-empty Account, so
// every workspace offered has an identity to seat the person under. The converse
// does NOT hold and must not be assumed — a subject that resolves to an account
// while holding no current membership row answers with the account and an empty
// list, which is the honest "we know who you are, and you are in nothing".
//
// (This doc used to claim the biconditional — "Account is empty exactly when
// there are no workspaces" — which the implementation never satisfied, because it
// resolves the account BEFORE walking the rows. A doc that overstates an
// invariant is worse than none: it is the one a caller writes an `if` against.)
type Spaces struct {
	// Account is the team AccountUuid the subject resolved to — the same identity
	// Member.Account carries, from the same derivation.
	Account string `json:"account"`
	// Name is the display name on the caller's member rows, empty when they have
	// not set one. A DISPLAY name only: meet passes it as the LiveKit participant
	// label, which is decoration, never identity.
	Name string `json:"name"`
	// Items is every workspace the person is in, newest membership first. Empty is
	// a real answer, not an error.
	Items []Space `json:"items"`
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

// GrantIn asks the forge to delegate ONE ref write in ONE repository.
//
// There is no Org field, on purpose: the tenant rides the caller's plane
// identity, so an app cannot delegate a write into a repository it does not act
// for by naming one.
type GrantIn struct {
	// Repo is the repository the grant addresses, and the only one it opens.
	Repo string `json:"repo" validate:"required"`
	// Project is the repository's sub-scope; empty is the org's default scope.
	Project string `json:"project,omitempty"`
	// Ref is the FULL ref the grant may create, and the only one. The forge
	// delegates the machine namespace and nothing else.
	Ref string `json:"ref" validate:"required"`
	// TTLSeconds bounds the grant. Absent, or longer than the forge's cap, gets
	// the cap — a grant is never open-ended.
	TTLSeconds int `json:"ttlSeconds,omitempty"`
}

// Granted is the delegated capability.
type Granted struct {
	// Token is the bearer. It authenticates nobody and opens nothing but the pack
	// protocol on the repository named in the request — never a log line.
	Token string `json:"token"`
	// Handle revokes the grant. It is the token's digest, so carrying it back
	// never means presenting the secret twice.
	Handle string `json:"handle"`
	// ExpiresAt is the unix second the grant stops working regardless.
	ExpiresAt int64 `json:"expiresAt"`
}

// RevokeIn withdraws a grant early, so its life is the run's life rather than
// its TTL.
type RevokeIn struct {
	// Handle is what Granted returned. An unknown handle, or one belonging to
	// another org, is a no-op rather than an error: revoking is idempotent.
	Handle string `json:"handle" validate:"required"`
}

// Revoked is the empty receipt for a withdrawal.
type Revoked struct{}

// RevIn asks which commit a ref names. An empty Ref means the repo's default
// branch.
type RevIn struct {
	Repo string `json:"repo" validate:"required"`
	Ref  string `json:"ref,omitempty"`
}

// Rev is a resolved commit and the label it was reached by, so a caller can echo
// which branch it is looking at without re-deriving it.
type Rev struct {
	Rev string `json:"rev"`
	Ref string `json:"ref,omitempty"`
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

// IndexReconcileIn is one index's WHOLE corpus, swapped in a single call: every
// document upserted, every key no longer present pruned. The ORG is the
// caller's, never an argument — the same rule the read follows, so a corpus can
// only ever be written under the tenant the call was made as.
type IndexReconcileIn struct {
	// UID is the index within the org, exactly as on the read.
	UID string `json:"uid" validate:"required"`
	// PrimaryKey names the field each document is keyed by. It is what makes the
	// swap idempotent — a re-published document updates in place instead of
	// accumulating a duplicate — and what the prune reads to find the keys that
	// left upstream.
	PrimaryKey string `json:"primaryKey" validate:"required"`
	// Docs is the corpus, relayed verbatim for the same reason IndexQueryOut.Rows
	// is raw: the documents belong to the app that assembled them, and a struct
	// here would be a second copy of a type this package does not own, free to
	// drift from the one that produced the bytes.
	Docs []json.RawMessage `json:"docs,omitempty"`
}

// IndexReconcileOut is what the swap did: how many documents are live now, and
// how many stale keys it pruned. Both are worth returning because together they
// are the sync's own health check — a pass that keeps zero, or prunes the whole
// corpus, is a source that failed rather than a corpus that emptied.
type IndexReconcileOut struct {
	Kept    int `json:"kept"`
	Removed int `json:"removed"`
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
// Payment is the client's signed PaymentPayload, verbatim off the request's
// PAYMENT-SIGNATURE header (base64, undecoded). It travels as a field because the
// process that holds the request is not the one that holds the rail, and there is
// no second place a payer's payment could come from: the caller does not mint it
// and cannot alter it without invalidating the signature it is checked against.
//
// There is no amount and no payee here, deliberately. What a resource costs and
// who is paid are the price table's, resolved by the rail; a caller that could
// state them could buy a $1 tool for a cent or redirect the credit.
type SettleIn struct {
	Resource string `json:"resource" validate:"required"`
	Payment  string `json:"payment,omitempty"`
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
	Response  string `json:"response,omitempty"`  // the PAYMENT-RESPONSE header value
	Challenge string `json:"challenge,omitempty"` // the PAYMENT-REQUIRED header value
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
//
// Network is CAIP-2 ("eip155:8453") and there is no chain id beside it, because
// the chain id is READ OUT of the network rather than carried twice. Two fields
// for one fact is one fact that can disagree with itself, and the disagreement
// lands in an EIP-712 domain no client can reproduce.
type Priced struct {
	Priced            bool   `json:"priced"`
	Amount            Money  `json:"amount"`
	RecipientOrg      string `json:"recipientOrg,omitempty"`
	RecipientWalletID string `json:"recipientWalletId,omitempty"`
	Asset             string `json:"asset,omitempty"`
	Network           string `json:"network,omitempty"`
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
	// Update, when set, EDITS the message with that timestamp instead of posting a
	// new one. It is a field of the same op rather than a second op because
	// "post" and "edit" are the same act — put this text at this address — and
	// the address is simply more specific in one case.
	//
	// It exists for the coding run. Slack has no server-sent stream and a long
	// run reporting each phase as a new message would bury a channel under a
	// dozen of them; editing ONE message in the thread is the progress indicator
	// the platform actually offers. APPENDED at the end: the wire is field
	// ORDER, so a field inserted anywhere else changes what every existing peer
	// reads.
	Update string `json:"update,omitempty"`
}

// SlackSent is the posted (or edited) message's timestamp — Slack's message id,
// and the handle a later edit addresses. The op used to answer struct{}, which
// made a progress message unaddressable: the caller could post a placeholder and
// then had no way to say which message it meant.
type SlackSent struct {
	TS string `json:"ts,omitempty"`
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

// ---- risk.decide — the scorer, from the process that holds the model --------

// Signal is one fact the asking gate observed, as a LIST element rather than a
// map entry — for the reason [Header] is one: a map cannot cross this plane at
// all (zapenc refuses it at encode), and a signal map would have failed inside
// zip.Call before it reached the socket.
//
// Free-form by design. The vocabulary belongs to the scorer's feature inventory
// rather than to the gate, so a gate states what it saw and the scorer reads the
// names it understands.
type Signal struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// The subject KINDS, and they are here rather than in either half because a
// subject kind is what NAMESPACES a subject: a person and an account sharing an
// identifier are two subjects, so a gate and a scorer that spell a kind
// differently do not disagree about a name, they judge a different entity. The
// scorer refuses a kind outside this set, which makes a misspelling a refused
// call rather than a verdict about nobody.
const (
	// KindPerson is the identified end user across the product surface.
	KindPerson = "person"
	// KindSession is one session of that surface.
	KindSession = "session"
	// KindAccount is the org's own user in the metered plane — the subject whose
	// spend velocity is what pay-as-you-go abuse moves.
	KindAccount = "account"
	// KindPayer is the party that PAYS: the billing subject a settled charge
	// credits.
	//
	// It is a kind of its own and not KindAccount, because the two name different
	// populations and a bound stated over one of them cannot be read over both. An
	// account's learned history is its metered INFERENCE SPEND; a payer's is the
	// money it moved IN. Judged under one kind they share one set of aggregates, so
	// a customer with a large inference bill accrues value on the very key a
	// PAYMENTS appetite is read from — a big spender trips a payments bound without
	// having paid anything, and the bound cannot be restated to fix it because one
	// number over two populations is two numbers.
	//
	// Naming the payer separately is the mechanism this file already relies on
	// ("a person and an account sharing an identifier are two subjects") applied to
	// the one place it was missing. It also makes the model's question at a credit
	// door answerable: a payment is scored against the payer's own payment history
	// rather than against a spend distribution it has nothing to do with.
	KindPayer = "payer"
)

// The signal names the scorer READS. Every other name a gate observes still
// travels and is still reported with the decision; these are the ones the scorer
// acts on, so they are spelled in the package both halves import rather than
// agreed by convention — a gate and a scorer that spell "nano" differently do not
// fail, they quietly score every payment as moving no money.
const (
	// SignalNano is the value moved, in nano-USD. Absent means the event moves no
	// money and the value features read BLIND, which is a different fact from zero.
	SignalNano = "nano"
	// SignalPeer is the counterparty, if any — an aggregation axis of its own.
	SignalPeer = "peer"
	// SignalDevice is the device fingerprint, if any — the axis that surfaces
	// several nominally unrelated subjects acting as one.
	SignalDevice = "device"
	// SignalAt is when it happened, RFC 3339. Absent means now.
	SignalAt = "at"
	// SignalCountry is the jurisdiction the payer acted from, ISO 3166-1 alpha-2.
	//
	// It is the one name here that is NOT a coordinate of the model's own event.
	// The model learns one organisation's own behaviour and a country is not a
	// dimension of that; this is read by the DETERMINISTIC rule beside the model,
	// which judges stated facts rather than learned mass. It is spelled here for
	// the same reason as the rest — one spelling, both halves — and a gate that
	// cannot state it omits it, which is a different fact from stating that the
	// payer is somewhere unremarkable.
	SignalCountry = "country"
)

// RiskDecideIn is one question for the scorer: what is being judged, at which
// lifecycle moment, and what the asking gate saw.
//
// There is no org here and there cannot be, exactly as everywhere else in this
// file: the organisation whose model answers is the CALLER's. A caller able to
// name it would be choosing which organisation's model judges its own request —
// and every model is trained on one organisation's own behaviour, so that choice
// is a cross-tenant read of the only thing this plane holds.
//
// It does not carry the seam's Privileged bit either. Whether silence must deny
// is the ASKING gate's rule and cloud.Decide applies it on the caller's side; a
// scorer that received it could only be tempted to answer differently for the
// same evidence.
//
// NOR THE GATE'S GUESS AT THE LANE. cloud.RiskQuery carries an Agency the scorer
// may overrule; this scorer has no opinion on agency, so it does not receive one
// and does not answer one, and the asking gate's own lane stands. A gate that
// wants it recorded states it as a signal like any other observation.
type RiskDecideIn struct {
	// Stage is the lifecycle moment, from cloud's closed set: signup, usage or
	// payment. The scorer REFUSES a stage it does not recognise rather than
	// judging a moment it does not model — the two ends of this call must agree on
	// what is being asked before the answer means anything.
	Stage string `json:"stage" validate:"required"`
	// Kind is whose behaviour this is — person, session or account. It namespaces
	// the subject, so a person and an account sharing an identifier stay two
	// subjects.
	Kind string `json:"kind" validate:"required"`
	// Subject is the identifier on that kind, within the caller's own tenant.
	Subject string `json:"subject" validate:"required"`
	// Signals are the facts the gate observed. The scorer reads the names above;
	// the rest are the asking gate's own record of why it asked.
	Signals []Signal `json:"signals,omitempty"`
}

// RiskDecided is the scorer's answer: what to do, and what makes the decision
// defensible after the fact.
//
// SCORE IS ONLY MEANINGFUL WHEN THERE IS NO REFUSAL. A model that declined has a
// populated score in its own engine — the arithmetic runs before the warm check —
// and publishing that number would turn "the model has no opinion" into "the model
// says this is fine". So a refusal carries no score, and Refusal is the field to
// read first.
type RiskDecided struct {
	// Action is what to do, from cloud's action vocabulary: allow, review,
	// challenge, restrict or block.
	Action string `json:"action" validate:"required"`
	// Refusal names why this is NOT a scored answer — warming, unusable or
	// unidentified — and is empty when it is one. None of them is a clean bill of
	// health.
	Refusal string `json:"refusal,omitempty"`
	// Score is where the event sat in that organisation's own density, in [0,1].
	// Present only on a scored answer.
	Score float64 `json:"score,omitempty"`
	// Cause is the scorer's short reason, for the record the gate writes.
	Cause string `json:"cause,omitempty"`
	// Shape is the model SPACE the verdict was reached in, `<family>:<digest>`. It
	// is what pins an adverse decision to a model: a score is only meaningful
	// against the space that produced it.
	Shape string `json:"shape,omitempty"`
	// Policy is the version of that organisation's decision regime the verdict was
	// reached under. Zero means no regime was ever stated and the default posture —
	// shadow — was in force.
	Policy int `json:"policy"`
}

// ---- risk.observe — one thing that HAPPENED, into the caller's own model -----

// RiskObserveIn is one settled fact for the calling organisation's own model.
//
// It is [RiskDecideIn]'s shape plus the one field a question does not need and a
// record cannot do without: the SETTLEMENT this observation is of. Everything
// else is deliberately identical, so the gate that asked about an event teaches
// the model from the same values it asked with — a screen and a record that
// resolved their subject by two different rules are two subjects, and the
// velocity of one of them is always empty.
//
// THERE IS NO ORG HERE for [RiskDecideIn]'s reason: the model this lands in is
// the CALLER's, minted from the plane principal. A field naming one would be a
// peer teaching another organisation's model, which is worse than reading it.
type RiskObserveIn struct {
	// Stage is the lifecycle moment this happened at, from cloud's closed set.
	Stage string `json:"stage" validate:"required"`
	// Kind is whose behaviour this is. A settled payment is KindPayer.
	Kind string `json:"kind" validate:"required"`
	// Subject is the identifier on that kind, within the caller's own tenant.
	Subject string `json:"subject" validate:"required"`
	// Settlement identifies what settled, and it is the IDEMPOTENCY KEY: the
	// model's own record deduplicates on it, so a retried request or a replayed
	// webhook converges instead of counting the same money twice.
	//
	// It must be the SETTLEMENT's own identifier — the processor's reference for
	// the charge, or the ledger receipt where the processor states none — and never
	// a counter, a timestamp or anything a caller of the settling door chose. A key
	// the payer can predict is a key the payer can claim first, after which the
	// real observation is inert.
	Settlement string `json:"settlement" validate:"required"`
	// Signals are the facts the settling process observed, in the scorer's own
	// vocabulary: the value that moved, the counterparty, the device, when.
	Signals []Signal `json:"signals,omitempty"`
}

// RiskObserved is what the model learned. Zero with no error means the
// settlement was ALREADY in that organisation's record — the idempotent answer,
// and a different fact from nothing having been sent.
type RiskObserved struct {
	// Learned is how many observations entered the model on this call: one, or zero
	// for a settlement already recorded.
	Learned int `json:"learned"`
}

// ---- event.capture — one occurrence, onto the shared event plane ------------

// EventIn is ONE occurrence a peer states onto the shared event plane — the same
// plane POST /v1/event fills, /v1/insights reads and every product lens groups
// over. It is what makes a peer's own facts answerable in the SAME query as the
// product's, rather than in a private table beside it.
//
// THERE IS NO ORG HERE AND THERE CANNOT BE, for the reason RiskDecideIn states:
// the organisation this lands under is the CALLER's, stamped server-side from the
// plane principal. A field naming one would be a caller writing into another
// tenant's partition, which is the one thing the event plane's tenancy stamp
// exists to prevent.
//
// It is deliberately the SMALL shape and not the ingest wire's whole vocabulary.
// The HTTP wire carries a browser's world — referrers, campaign parameters,
// replay clips, exception frames — because a browser is what fills it. A peer
// states a fact about its own work, so what it needs is a name, a surface, whom
// it concerned and what it says; anything richer belongs on the wire the richer
// caller already has.
type EventIn struct {
	// Name is what happened, in the emitter's own verb-object vocabulary
	// (risk_decided, secret_rotated). It is the column every lens groups by, so it
	// must come from a CLOSED set the emitter owns — never from a string a caller
	// of the emitter chose, which is unbounded cardinality in the one column the
	// plane indexes.
	Name string `json:"name" validate:"required"`
	// Product is the emitting SURFACE — the app's own name. Empty attributes the
	// row to no surface, which is honest and unhelpful.
	Product string `json:"product,omitempty"`
	// Subject is whom the occurrence concerned, as the value the emitter is
	// willing to have stored: the row's distinct id. An identifying value belongs
	// here only in an opaque form — the plane is a SHARED store read by every lens
	// the organisation has, not the emitter's private record.
	Subject string `json:"subject,omitempty"`
	// At is when it happened, RFC 3339. Empty means now, decided by the app that
	// owns the plane (which is also the one that clamps a stated time).
	At string `json:"at,omitempty"`
	// Attributes are the occurrence's own facts, as [Signal] — the plane's ONE
	// name/value pair — because a map cannot cross this plane at all (see
	// [Header], which learned it the expensive way). They land in the row's
	// attributes map, so a new fact is a new value and never a schema change.
	Attributes []Signal `json:"attributes,omitempty"`
}

// EventCaptured is the honest receipt: what landed and what did not. A peer that
// states an occurrence the plane cannot route is TOLD so, rather than being given
// a 200 for a row that was never written — the exact silence that let an 88% loss
// run unnoticed on the HTTP door.
type EventCaptured struct {
	// Accepted is how many occurrences were admitted and published.
	Accepted int `json:"accepted"`
	// Dropped is how many were not, because nothing about them named a landable
	// row.
	Dropped int `json:"dropped"`
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

// KeyIn names a publishable ingest key to resolve. No org: the KEY is the tenant
// key, and accepting one would let a caller file a beacon under someone else's.
type KeyIn struct {
	Key string `json:"key"`
}

// Attribution is the write scope a publishable key names. Found is explicit for the
// same reason it is on Site: a key no project holds is a clean refusal, and a
// failure to ASK is not — collapsing them would silently drop every site's
// analytics during any transient failure of the owning app, which is the exact
// class of silent loss this key exists to end.
type Attribution struct {
	Found   bool   `json:"found"`
	Org     string `json:"org"`
	Project string `json:"project"`
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

// LiveSitesIn asks for every site this deployment is serving. It carries no
// fields, and that is the contract rather than an omission: this is THE
// cross-org read, so there is no tenant to name, and the rule that makes it safe
// — public, live, not hidden — is applied in the query by the app that owns the
// store. A field here could only ever be a way to ask for something narrower
// than what is already public, or wider than what is.
type LiveSitesIn struct{}

// LiveSitesOut is every serving site, newest first.
type LiveSitesOut struct {
	Sites []LiveSite `json:"sites,omitempty"`
}

// LiveSite is one deployed site in the terms a directory needs: where it is,
// what it was built from, and whose work it credits.
//
// Repo and ForkedFrom are the trace back OUT of a demo — a live URL nobody can
// get from to the source is a screenshot, not a starting point. Upstream and
// License are carried exactly as stored and never inferred: a guessed credit is
// worse than no credit, and a directory that guesses authorship in its own
// favour is not making an error, it is making a claim.
//
// There is no authorship field. Who published a site is Org — the account that
// paid for it, which the tenancy boundary enforces and no request can forge.
type LiveSite struct {
	Org        string `json:"org"`
	Slug       string `json:"slug"`
	Name       string `json:"name,omitempty"`
	URL        string `json:"url,omitempty"`
	Repo       string `json:"repo,omitempty"`
	ForkedFrom string `json:"forkedFrom,omitempty"`
	UpdatedAt  int64  `json:"updatedAt,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
	License    string `json:"license,omitempty"`
}

// ---- agents: the login-manager session teardown -----------------------------

// SessionMatchIn selects the live sessions a credential revoke tears down.
//
// It carries NO org: the tenant is the caller's plane identity, resolved on the
// answering side. Subject travels instead of a fully-formed actor because the
// actor is org-qualified (org/user) and the org is exactly what the caller may
// not state — so the one place that can build the actor is the one place that
// knows the org is real.
//
// An empty Subject yields an empty actor, which the agents guard reads as "stop
// nothing". That is fail-closed by construction: a match that lost its caller
// identity sweeps nothing rather than sweeping the org.
type SessionMatchIn struct {
	// Subject is the revoking user, unqualified. The answering side qualifies it.
	Subject string `json:"subject"`
	// Host/Provider/Account narrow WITHIN the actor's own sessions; empty is any.
	Host     string `json:"host,omitempty"`
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
}

// SessionCount is how many sessions the op stopped, or found live.
//
// A count is the whole answer here, and the reason it can be is that failure is
// an error: "0" now means zero sessions matched and never "the store could not
// be reached", which is the distinction whose absence let a revoke report
// success having revoked nothing.
type SessionCount struct {
	Count int `json:"count"`
}

// ---- git: status + mirror --------------------------------------------------

// StatusIn asks git which of these repos are imported, for the caller's org.
// Names travel as a slice because a map cannot cross this wire at all.
type StatusIn struct {
	// Project is the sub-scope; empty means the org's default store.
	Project string `json:"project,omitempty"`
	// Names are the repo names to report on.
	Names []string `json:"names"`
}

// RepoStatus is one repo's import + sync state. Name is IN the row rather than a
// map key: the reply is a slice, so the row has to carry its own identity.
type RepoStatus struct {
	Name string `json:"name"`
	// Imported is true when a native repo exists for this name.
	Imported bool `json:"imported"`
	// Conflict is true when a branch diverged on a prior inbound sync and native
	// was preserved.
	Conflict bool `json:"conflict"`
	// LastSyncedAt is unix seconds of the last import/sync; 0 means never.
	LastSyncedAt int64 `json:"lastSyncedAt"`
}

// Statuses is the per-repo report. A name git knows nothing about is simply
// absent from Rows — that is a real answer, and the caller reads it as
// not-imported rather than as a failure.
type Statuses struct {
	Rows []RepoStatus `json:"rows"`
}

// MirrorIn declares or removes a native repo's outbound mirror target. Enabled
// false REMOVES it; the call is idempotent either way.
type MirrorIn struct {
	Project string `json:"project,omitempty"`
	Repo    string `json:"repo"`
	// URL is the outbound target to push to.
	URL string `json:"url"`
	// Enabled registers the target when true and removes it when false.
	Enabled bool `json:"enabled"`
}

// Mirrored acknowledges a mirror declaration. A failure is an error, never this.
type Mirrored struct {
	Repo string `json:"repo"`
}

// ---- sync ------------------------------------------------------------------

// SyncIn is one provider-agnostic reconcile trigger. It carries no Org: the
// tenant is the caller's, and a trigger that could name the org could reconcile
// another tenant's repositories.
type SyncIn struct {
	// Kind is the sync kind, e.g. "git".
	Kind string `json:"kind"`
	// Provider is the endpoint the event came from: github | gitlab | hanzo-git.
	Provider string `json:"provider"`
	// Locator is the source repo locator — a clone URL, or "<owner>/<repo>".
	Locator string `json:"locator,omitempty"`
	// Repo is the short repo name.
	Repo string `json:"repo,omitempty"`
	// Ref is the FULL ref that moved.
	Ref    string `json:"ref,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Actor is who made the upstream push; the engine's loop guard compares it to
	// the sync's own actor.
	Actor string `json:"actor,omitempty"`
	// Token is an OPTIONAL short-lived credential the trigger already minted. It
	// rides the internal socket only and is never logged.
	Token string `json:"token,omitempty"`
	// Manual marks a /run or an initial reconcile rather than a specific push.
	Manual bool `json:"manual,omitempty"`
	// Hop is the chained-propagation depth, bounded by the engine's hop limit.
	Hop int `json:"hop,omitempty"`
}

// SyncRan reports what the dispatch did across the resolved syncs.
type SyncRan struct {
	// Ran is the number of syncs that reconciled a change.
	Ran int `json:"ran"`
	// Skipped is the number resolved but skipped — loop guard, idempotent, or
	// direction off.
	Skipped int `json:"skipped"`
}

// ---- tracker ---------------------------------------------------------------

// IssueIn is one external work item to mirror into the native tracker, keyed
// idempotently by ExtRef so a webhook redelivery updates the same row. No Org:
// the tenant is the caller's, resolved from the signed installation at the edge.
type IssueIn struct {
	// Project is the IAM project scope; empty means the org's default store.
	Project string `json:"project,omitempty"`
	// Key is the tracker team the item files under, e.g. "GH"; ensured on first use.
	Key string `json:"key,omitempty"`
	// TeamName is the display name used when that team is first created.
	TeamName string `json:"teamName,omitempty"`
	// Repo is the git repo the item belongs to — the per-repo filter discriminator.
	Repo string `json:"repo,omitempty"`
	// ExtRef is the external anchor AND the idempotency key, e.g.
	// "github:owner/repo#123".
	ExtRef string `json:"extRef"`
	// Kind is what it is — "issue" | "pr".
	Kind string `json:"kind,omitempty"`
	// Source is which surface opened it, e.g. "git".
	Source      string `json:"source,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// State is the upstream open/closed state; the tracker maps it to a column.
	State    string   `json:"state,omitempty"`
	Assignee string   `json:"assignee,omitempty"`
	Labels   []string `json:"labels,omitempty"`
}

// IssueUpserted reports what the upsert did, so a feeder can count precisely.
type IssueUpserted struct {
	// Created distinguishes a new row from an update.
	Created bool `json:"created"`
	// Number is the tracker's own item number.
	Number int `json:"number"`
	// Identifier is KEY-<number>.
	Identifier string `json:"identifier,omitempty"`
}

// ---- platform: push→build and image→CR -------------------------------------

// PushIn is a push that landed on the native git server, offered to the builder.
// No Org: the tenant is the caller's, and a push event that could name the org
// could build in another tenant's project.
type PushIn struct {
	Project string `json:"project,omitempty"`
	Repo    string `json:"repo"`
	// Ref is the FULL ref that moved — refs/heads/<b> or refs/tags/<t>. Tags reach
	// the builder too: releases are cut by tag.
	Ref string `json:"ref"`
	// Commit is the new tip.
	Commit string `json:"commit,omitempty"`
	// CloneURL is the canonical clone URL of the repo, which is the exact value an
	// Application's RepoURL carries, so the builder can resolve which app tracks it.
	CloneURL string `json:"cloneUrl,omitempty"`
}

// Built acknowledges that the builder ACCEPTED the push. A failure is an error,
// never this shape — the same contract as Imported and Mirrored.
//
// It deliberately does NOT report how many applications the push matched. Most
// pushes track no app, so that count would be interesting, but nothing consumes
// it today and the builder does not return it; adding the field would mean
// widening buildFromPush's signature to produce a value no caller reads. Fields
// append at the END of a ZAP type, so the day something needs the count it can
// be added without disturbing any peer.
type Built struct {
	Repo string `json:"repo"`
}

// ReleaseIn is a proven, clean-semver image ready to roll onto its Service CR.
type ReleaseIn struct {
	// Service is the target CR metadata.name.
	Service string `json:"service"`
	// Image is the full registry ref; the tag MUST be clean semver (vX.Y.Z) and
	// the releaser refuses every mutable/sha/suffixed form.
	Image string `json:"image"`
	// SHA is the source commit, for provenance. Logged, never gated on.
	SHA string `json:"sha,omitempty"`
}

// Released acknowledges a rollout. Patched=false means the releaser ran and
// matched no CR — again a real answer, distinct from not being asked at all.
type Released struct {
	Patched bool `json:"patched"`
}

// ---- projects: the org-scope ownership answer ------------------------------

// OwnerIn names a project identifier — a slug or an opaque id — to be judged
// against the CALLER's org. It carries no org for the usual reason, and here the
// reason is sharper than usual: the org is exactly what the answer is relative
// to, so a caller that could state it could ask the question about somebody else
// and act on the answer.
type OwnerIn struct {
	IDOrSlug string `json:"idOrSlug"`
}

// Ownership is the registry's verdict on one project identifier.
//
// Mine and Other are SEPARATE booleans rather than one enum because three
// distinct facts have to survive the trip: the caller's org owns it (keep), some
// OTHER org owns it (refuse), or NOBODY has registered it — a free-form
// within-org label, which is neither and must be kept. Collapsing the third into
// either of the first two breaks a working surface or opens the guard.
type Ownership struct {
	// Mine is true when the caller's own org owns a project with this id/slug.
	Mine bool `json:"mine"`
	// Other is true when some org OTHER than the caller's owns one.
	Other bool `json:"other"`
}

// RunOnBehalfIn asks agents to run one turn AS a linked user.
//
// The bridges (Slack, Discord, Teams, Telegram) are their own plugin, so this
// crosses a process boundary and must be a typed plane op rather than a Go call:
// a package global cannot be reached from another process, and the in-process
// shortcut answered ErrNoPeer for every deployment that did not happen to place
// agents and integrations in one binary.
//
// No field here is a map — the encoder refuses one at the plane boundary.
type RunOnBehalfIn struct {
	// Org is the isolation gate, the tenant, and the balance the run bills.
	Org string `json:"org"`
	// Subject is the caller's LINKED Hanzo identity, unqualified. Attribution and
	// authorization both hang off it, so a turn can never run as nobody: the
	// answering side refuses an empty subject rather than falling back to the org.
	Subject string `json:"subject"`
	// Ref names the agent to run.
	Ref string `json:"ref"`
	// Input is the user's message, already stripped of the leading @mention.
	Input string `json:"input"`
	// Model is the ASKER's own choice, empty when they have not made one. It is a
	// preference of the person, not a property of the agent, which is why it rides
	// the turn instead of being written into an agent row: two people in one
	// workspace can prefer different models of the same assistant.
	Model string `json:"model,omitempty"`
}

// RunOnBehalfOut is one finished turn.
//
// Status is carried EXPLICITLY rather than inferred from a non-empty Output,
// because "the agent ran and had nothing to say" and "the agent failed" are
// different answers and a bridge must not post the second as the first.
type RunOnBehalfOut struct {
	Status string `json:"status"`
	Output string `json:"output"`
	RunID  string `json:"runId,omitempty"`
}

// ---- sandbox ---------------------------------------------------------------

// LeaseIn asks for a sandbox. ID names one to RESUME: a lease that is still
// running comes back as it is, and one that has ended is replaced by a fresh
// sandbox rather than refused — the caller is resuming a conversation whose lease
// expired while they were reading, and a working sandbox with a new id is the
// honest answer to that.
type LeaseIn struct {
	ID      string `json:"id,omitempty"`
	Class   string `json:"class,omitempty"`
	Project string `json:"project,omitempty"`
	TTLSec  int    `json:"ttlSec,omitempty"`
}

// Leased is the sandbox a lease got.
//
// Workdir is carried rather than assumed. Where a sandbox keeps its files is a
// property of its CLASS (/work for a project sandbox, /mnt/data for a code-
// interpreter one), and a caller that hardcoded either would hold a second copy of
// a fact only the sandbox knows — which is how a run writes its plots to one
// directory and the collector lists the other.
//
// There is no Pod and no address. A sandbox is reached by asking its owner, never
// by dialing it, and a peer that could learn a pod name would be a peer that could
// try.
type Leased struct {
	ID      string `json:"id"`
	Class   string `json:"class"`
	Status  string `json:"status"`
	Workdir string `json:"workdir"`
}

// RunIn runs one command in a sandbox. Argv is the honest form; Command is the
// convenience for a caller holding a shell line.
type RunIn struct {
	ID         string   `json:"id"`
	Argv       []string `json:"argv,omitempty"`
	Command    string   `json:"command,omitempty"`
	Stdin      string   `json:"stdin,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	TimeoutSec int      `json:"timeoutSec,omitempty"`
	// Session is the live agent session this command narrates into: its output is
	// appended there AS IT IS PRODUCED, so every surface watching that session
	// watches the command work instead of a blank pause. A long run is otherwise a
	// silence with a verdict at the end.
	//
	// It names a SESSION and never a tenant. The org is the one the caller already
	// proved, so a session belonging to somebody else is simply absent from the org
	// this call acts for and the append is refused there. Empty means nothing is
	// watching, and then nothing is sent.
	Session string `json:"session,omitempty"`
}

// Ran is what a command produced. A non-zero ExitCode is DATA, not an error: the
// call succeeded and the program failed, and a caller has to be able to tell those
// apart.
type Ran struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// PathIn names one path inside a sandbox. An empty Path means the sandbox's own
// working directory.
type PathIn struct {
	ID   string `json:"id"`
	Path string `json:"path,omitempty"`
}

// Blob is what a path IS: a file's bytes, or a directory's entries. One answer for
// both, because one command answers both and a caller that had to stat first would
// pay two round trips to learn what the first one already knew.
type Blob struct {
	Path    string   `json:"path"`
	Dir     bool     `json:"dir,omitempty"`
	Data    []byte   `json:"data,omitempty"`
	Entries []string `json:"entries,omitempty"`
}

// WriteIn puts bytes at one path inside a sandbox, creating parents.
type WriteIn struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Data []byte `json:"data,omitempty"`
}

// Wrote reports the RESOLVED path and how much landed there. The path is resolved
// because a caller's path is relative far more often than not, and echoing back
// what it asked for would tell it nothing it did not already know.
type Wrote struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// StopIn interrupts what a sandbox is running. It names the SANDBOX and not a
// command, because whoever is watching a run holds the sandbox's id and never the
// process id of whatever is inside it.
type StopIn struct {
	ID string `json:"id"`
}

// Stopped is how many commands the stop interrupted.
//
// Zero is an ANSWER and not a failure: a command that finished a moment ago is
// one there was nothing left to stop. The distinction a caller actually needs is
// "already over" versus "not yours", and the second is a 404 from the ordinary
// org lookup rather than a zero here.
type Stopped struct {
	Stopped int `json:"stopped"`
}

// EndIn ends a lease. Purge drops the project VOLUME as well and is opt-in,
// because ending a lease is reversible and deleting someone's uncommitted work is
// not.
type EndIn struct {
	ID    string `json:"id"`
	Purge bool   `json:"purge,omitempty"`
}

// ---- coding: every seam one autonomous coding run reaches across ------------
//
// A coding run (`@hanzo code: <repo> <task>`) is the most cross-app act in the
// estate: it opens an agents SESSION, resolves and gates an agents TARGET, reads
// git's CLONE URL, verifies in git that the pushed branch LANDED, and files the
// PR row in tracker. Five apps, and since the fused monolith went away, five
// PROCESSES.
//
// It was written as five direct Go calls. Each of those reads a package global
// (`mounted`) that belongs to the OTHER process, so each answered the zero value
// in the one deployment that exists: CloneURL returned "" ("git is not
// available"), OpenSession returned "not mounted", VerifyRef reported the branch
// absent and the run failed CLOSED with no PR. The same shape that killed every
// @hanzo chat turn until the on-behalf-of run moved onto this plane.
//
// So the seams travel. The orchestration itself (apps/coding) is unchanged and
// stays ONE library — it always spoke to its collaborators through injected
// seams, which is exactly what made this a re-binding rather than a rewrite.
const (
	// AgentsSessionOpen / Event / Close are the live agent session a coding run
	// streams into: the durable record mission-control renders and the stream the
	// operator watches. Target, when set, tags the session with the MACHINE the
	// run was routed to (agents.OpenSessionOn), so a routed run shows up where it
	// is actually executing.
	AgentsSessionOpen  = "agents_session_open"
	AgentsSessionEvent = "agents_session_event"
	AgentsSessionClose = "agents_session_close"

	// AgentsResolveTarget turns a human's `on <machine>` reference — a target id
	// or the friendly label the CLI registered — into a target id, org-scoped and
	// fail-closed. The Slack front door parses the word; agents owns the registry
	// that knows whether the org has such a machine.
	AgentsResolveTarget = "agents_resolve_target"

	// AgentsTargetGate is the liveness+existence check a routed run passes before
	// it is enqueued: the target exists in this org, is online, and has a live
	// runner. Fail-closed — an error here means the run does not dispatch, never
	// that it quietly runs somewhere else.
	AgentsTargetGate = "agents_target_gate"

	// AgentsRouteRun enqueues a routed run on the durable engine.
	//
	// This one is not merely a store that lives elsewhere: the run is offered to
	// an IN-MEMORY mailbox that the machine long-polls through agents' own HTTP
	// surface. Enqueued in any other process, the delivery activity would offer
	// the run to a mailbox nobody polls and the workflow would burn its whole
	// budget before failing. The engine that owns the workflow and the mailbox
	// that hands it out have to be the same process, and that process is agents.
	AgentsRouteRun = "agents_route_run"

	// GitCloneURL is the canonical clone URL for an org's native repo — the only
	// thing the sandbox is ever pointed at, so the run cannot reach another
	// tenant's namespace.
	GitCloneURL = "git_clone_url"

	// GitVerifyRef reports the tip of a branch by reading the bare repo on git's
	// own storage. It is the INTEGRITY gate: cloud trusts the ref it can read,
	// not the remote runner's claim to have pushed one. Found=false is
	// fail-closed and costs the run its PR.
	GitVerifyRef = "git_verify_ref"

	// TrackerAgentPR opens the native PR work item for a finished run.
	TrackerAgentPR = "tracker_agent_pr"

	// CodingStart is the ONE door onto a coding run, for a caller in another
	// process.
	//
	// The engine has to live in exactly one process and that process is agents:
	// it already holds the live session store the run streams into, the durable
	// tasks engine, and the in-memory mailbox a routed run is handed through.
	// A second process assembling its own Dispatcher would be a second engine —
	// same orchestration, different pool, different in-flight set, a run started
	// from chat unobservable from the app. So the ENGINE stays put and the START
	// travels, exactly as AgentsRunOnBehalf made one brain reachable from every
	// chat platform.
	//
	// The chat adapter and the /v1/coding route are then two DOORS onto the same
	// coding.Start, which is what makes "one engine, two adapters" a fact about
	// the code rather than a claim about it.
	CodingStart = "coding_start"
)

// The ORG rides in these arguments rather than on the caller's plane identity,
// which is the exception [RunOnBehalfIn] documents and for the same reason: the
// tenant is the one that connected the Slack workspace, resolved server-side
// from the signature-VERIFIED team_id, and the calling plugin's own identity is
// not it. A coding run only ever touches the named org's own session, its own
// repo and its own board — it reads nothing across tenants — and the org it
// names was never a field the human could set.
//
// No field below is a map. The encoder refuses one at this boundary (zapenc
// layoutOf), and a refused encode is a call that dies before the socket.

// SessionOpenIn opens the live session a coding run streams into.
type SessionOpenIn struct {
	Org   string `json:"org"`
	Actor string `json:"actor,omitempty"` // the linked Hanzo subject the run is attributed to
	Agent string `json:"agent"`           // agent label, e.g. "hanzo"
	Title string `json:"title,omitempty"`
	// Target tags the session with the machine a ROUTED run was sent to. Empty is
	// the ordinary cloud-sandbox session.
	Target string `json:"target,omitempty"`
}

// SessionOpened is the new session's id — the branch suffix, the PR body's link,
// and the handle every later event carries.
type SessionOpened struct {
	SessionID string `json:"sessionId"`
}

// SessionEventIn appends one event to a live session. Payload is the event's
// already-encoded JSON body, carried as bytes because its SHAPE belongs to the
// event kind and not to this contract — and because the map it would otherwise
// be cannot cross.
type SessionEventIn struct {
	Org       string `json:"org"`
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"` // "tool-call" | "log" | "status"
	Actor     string `json:"actor,omitempty"`
	Payload   []byte `json:"payload,omitempty"`
}

// SessionCloseIn transitions a session out of running. Status is "done" or
// "error" — the two terminals a coding run has.
type SessionCloseIn struct {
	Org       string `json:"org"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}

// CodingAck is the answer of a coding seam that either worked or returned an
// error. It carries a field because a void op answers 204 and a caller cannot
// tell 204 from "the op is not there".
type CodingAck struct {
	OK bool `json:"ok"`
}

// TargetRefIn names a machine the way a human did: an id, or the friendly label
// the `hanzo code --serve` daemon registered.
type TargetRefIn struct {
	Org string `json:"org"`
	Ref string `json:"ref"`
}

// TargetRef is the resolved machine. An id that resolves to no target IN THIS
// ORG is an error, never another tenant's machine and never a silent fallback to
// a local run.
type TargetRef struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// TargetGateIn asks whether a run may be dispatched to a machine.
type TargetGateIn struct {
	Org      string `json:"org"`
	TargetID string `json:"targetId"`
}

// RepoRefIn names one of an org's native repos.
type RepoRefIn struct {
	Org  string `json:"org"`
	Repo string `json:"repo"`
}

// RepoCloneURL is the canonical clone URL. Empty means git could not answer,
// which the run reads as "git is not available" and refuses to proceed.
type RepoCloneURL struct {
	URL string `json:"url"`
}

// RefIn names one branch of one repo.
type RefIn struct {
	Org    string `json:"org"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

// RefTip is what git's own storage says about that branch. Found is EXPLICIT
// rather than inferred from an empty SHA: "the branch is not there" and "the
// read failed" both have to fail the integrity gate, and an absent tip that read
// as a present-but-unknown one would file a PR for a branch nobody can fetch.
type RefTip struct {
	SHA   string `json:"sha,omitempty"`
	Found bool   `json:"found"`
}

// AgentPRIn opens the native PR work item for a pushed branch.
//
// IT CARRIES NO ORG, deliberately, and for the same reason [IssueIn] carries
// none: the org is the CALLER's plane identity (cloud.Who), and a field here
// would let a caller state the tenant it is filing into — which is a
// cross-tenant WRITE the caller asserted for itself. Two ops share this socket
// and they must not disagree about where tenancy comes from; the one that reads
// it off the wire is the one that is wrong.
type AgentPRIn struct {
	Project  string `json:"project,omitempty"`
	Repo     string `json:"repo"`
	Base     string `json:"base,omitempty"`
	Head     string `json:"head"`
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	Assignee string `json:"assignee,omitempty"`
}

// AgentPROut is the created row's stable handle.
type AgentPROut struct {
	Identifier string `json:"identifier"`
	ProjectKey string `json:"projectKey,omitempty"`
	Number     int    `json:"number,omitempty"`
}

// RouteRunIn is the NON-SECRET spec of a run to be executed on a registered
// machine. It carries no credential by design: the machine authenticates git
// with its own already-held one, so nothing here is a secret and the run can be
// durably persisted by the engine without holding a token.
type RouteRunIn struct {
	Org            string `json:"org"`
	TargetID       string `json:"targetId"`
	SessionID      string `json:"sessionId"`
	Repo           string `json:"repo"`
	Project        string `json:"project,omitempty"`
	Base           string `json:"base,omitempty"`
	Branch         string `json:"branch"`
	Prompt         string `json:"prompt"`
	CloneURL       string `json:"cloneUrl"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	// Actor + AgentRef are cloud-side attribution for the completion path (the
	// session close and the PR assignee). They never cross to the machine.
	Actor    string `json:"actor,omitempty"`
	AgentRef string `json:"agentRef,omitempty"`
}

// CodingStartIn asks the engine to begin one coding run.
//
// IT CARRIES NO CREDENTIAL, and that absence is the point. The org's agent git
// secret used to be read by the Slack surface and handed down through the
// request, so a token with write access to every repo in the org existed in the
// chat process, crossed a socket, and sat in a struct that three packages
// touched. None of them needed it. The ENGINE needs it, at the one moment it
// dispatches a sandbox, so the engine now reads it from KMS itself in the org
// the run belongs to. Custody shrinks from three processes to one, and a door
// onto this op can no longer be a door onto the secret.
//
// Subject is the linked Hanzo identity the run is attributed to, already proved
// by the door (a Slack account link, or the authenticated caller of /v1/coding).
// It is refused when empty rather than defaulted: a run that lost its human must
// not execute AS THE ORG.
//
// The tenant is NOT here. It rides the caller — stated on a detached context
// before the hop — because a run spends the org's balance and reaches the org's
// repos, and a field the caller can set is not an identity.
type CodingStartIn struct {
	Subject        string `json:"subject"`
	Repo           string `json:"repo"`
	Prompt         string `json:"prompt"`
	Project        string `json:"project,omitempty"`
	Base           string `json:"base,omitempty"`
	AgentRef       string `json:"agentRef,omitempty"`
	TargetID       string `json:"targetId,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	// ReplyChannel / ReplyThread are WHERE THE RUN NARRATES ITSELF, when the door
	// that started it has somewhere for it to talk. Empty means nobody is
	// listening and the run simply does not narrate — which is the app door's
	// case, because /v1/coding hands back a session id and the session stream is
	// a better progress feed than any message could be.
	//
	// It is an ADDRESS and not a token: the engine says "put this text there",
	// and the process that owns the workspace's bot credential is the one that
	// actually posts. So a run reports into a Slack thread without the engine
	// ever holding the token that could post anywhere else in that workspace.
	ReplyChannel string `json:"replyChannel,omitempty"`
	ReplyThread  string `json:"replyThread,omitempty"`
}

// CodingStarted is the ACCEPTED run's handle. A coding run takes minutes, so the
// op answers when the run is admitted, not when it is finished: the session id is
// the handle every later question about the run is asked with, and the branch is
// the ref the run is permitted to write (nothing else — see the forge's ref
// policy). Progress streams at /v1/agents/sessions/{sessionId}/stream for every
// door equally, which is why neither door grew a progress endpoint of its own.
type CodingStarted struct {
	SessionID string `json:"sessionId"`
	Branch    string `json:"branch"`
	Repo      string `json:"repo"`
	Routed    bool   `json:"routed,omitempty"`
	TargetID  string `json:"targetId,omitempty"`
}

// ---- ask.figures -----------------------------------------------------------

// Figure is ONE grounded number a domain read for the caller's own org: what it
// is called, its value ALREADY FORMATTED by the domain that owns it, and the
// window it covers.
//
// The value is a string, and that is the whole point. books owns what a dollar
// looks like, git owns what a byte count looks like; a float on this wire would
// invite the reader to format it a second way, and two spellings of one number
// is how a figure starts disagreeing with the page it came from.
type Figure struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Period string `json:"period,omitempty"`
}

// FiguresIn is empty, and stays empty.
//
// There is nothing to ask for because there is nothing a caller may choose. The
// org is the CALLER's — forwarded from the gateway's assertion by [Ask] — so the
// one field this struct might plausibly grow is exactly the field that would let
// one tenant read another's books. An empty In is that rule made structural: not
// "we validate the org argument", but "there is no org argument".
type FiguresIn struct{}

// FiguresOut is a domain's headline figures for the caller's org, in the order
// the domain thinks they should be read.
//
// An org with nothing in it answers an EMPTY slice, never an error: "you have no
// projects" is a true answer to "what have I deployed", and an outage that
// arrived as a zero would be indistinguishable from it. The domains keep the two
// apart — absence is an empty slice, failure is an error — because the advisor
// above them states figures verbatim and cannot audit what it is handed.
type FiguresOut struct {
	Figures []Figure `json:"figures"`
}
