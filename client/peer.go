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

package client

import (
	"encoding/json"
	"errors"
	"github.com/hanzoai/cloud/sdk/iam"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/zip"
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

	// SitesClaim / SitesRelease take and give back a name in the one public
	// namespace, <name>.hanzo.app, which app and site names share. The platform
	// asks them for an app of the CALLER's org; the namespace is the projects
	// store's site_hosts table, so the answer belongs to that app.
	SitesClaim   = "sites_claim"
	SitesRelease = "sites_release"

	// SitesPurge clears URL prefixes from the edge cache through the one edge
	// client, which the project app holds with its credential, its coalescing and
	// the deployment's purge ceiling. The platform alone asks it.
	SitesPurge = "sites_purge"

	// ProjectsResolveKey answers "which project minted this publishable ingest
	// key?" for the analytics ingest endpoint — here for the same reason as the
	// two above, and between the same two processes: the endpoint serves
	// api.hanzo.ai and the key lives in the project store.
	ProjectsResolveKey = "projects_resolve_key"

	// ProjectsOwnership answers "does this org own the project this request
	// claims?" for the identity trust boundary. The boundary is cloud edge
	// middleware in EVERY process; the registry is the projects app's alone. With
	// no resolver the guard returned "not foreign" — so the cross-org project
	// impersonation check was off wherever projects was not co-resident, which is
	// everywhere. Absence of an ANSWER may never read as permission.
	ProjectsOwnership = "projects_ownership"

	// ProjectsCreate makes one project in the CALLER's org under the exact slug
	// asked for, through the same create path POST /v1/project takes. A coding run
	// asked for with no repository starts here: the run lives in the agent app and
	// the project store in this one.
	ProjectsCreate = "projects_create"

	// ProjectsLink makes a repository the CALLER's org linked into one of its
	// projects, or answers the project that already builds from it. The link is
	// the sync app's and the project store this one's.
	ProjectsLink = "projects_link"

	// ProjectsWorkspace answers where the CALLER's org keeps its code on the
	// forge, and makes that namespace when asked to and the org has none. The
	// record of which namespace the platform made for which org lives in the
	// project store, and a coding run — which needs the answer before it can
	// work — lives in the agent app.
	ProjectsWorkspace = "projects_workspace"

	// ProjectsSiteOpen opens a deployment of one of the CALLER's projects that a
	// sandbox builds from the project's own repository, and answers the grant the
	// sandbox uploads the built site with. The sandbox runner is the agent app's
	// and the site store is this one's.
	ProjectsSiteOpen = "projects_site_open"

	// ProjectsSiteClose ends a deployment ProjectsSiteOpen opened: live, with the
	// keys the sandbox uploaded, or failed, with why.
	ProjectsSiteClose = "projects_site_close"

	// ProjectsSiteBuilds lists the CALLER's sandbox-built deployments, newest
	// first, for the platform's builds board.
	ProjectsSiteBuilds = "projects_site_builds"

	// ProjectsSiteApps answers the CALLER's sites as deployed applications: what
	// each serves, and for one named site the releases it was kept as, which are
	// what it can be rolled back to. The deploy console lists and reads a site
	// through it; the site store is this one's.
	ProjectsSiteApps = "projects_site_apps"

	// ProjectsSiteRollback serves one of the CALLER's site's kept releases again:
	// the activation of an earlier release, the deploy console's rollback.
	ProjectsSiteRollback = "projects_site_rollback"

	// AllowanceRead answers what a subject has left of their plan's free calls in the
	// window that holds them, without counting anything: the usage page asks it.
	AllowanceRead = "allowance_read"

	// AllowanceTake admits one free call against a subject's plan allowance, counting
	// it as it is admitted, and answers what is left. A call is counted when it is let
	// through and not when it answers, so a failing upstream cannot be retried without
	// spending the allowance; a call refused at the ceiling counts nothing.
	//
	// Both are on the plane because the caller is the AI gate, which runs in its own
	// process, and the count is the deployment's: the allowance app keeps it on the
	// KV every pod shares (sdk/coord Quota).
	AllowanceTake = "allowance_take"
	// AllowanceGive hands back the unit a take counted for a call that was admitted
	// and then not served (it failed or was refused), so only served calls spend the
	// allowance. At names when the call was taken and Start the session it counted
	// in, so the unit returns to the windows it was counted in and no other.
	AllowanceGive = "allowance_give"

	FinanceAuthorize = "finance_authorize" // the prepaid gate
	FinanceBalance   = "finance_balance"
	FinanceRecord    = "finance_record" // the meter
	FinanceCosts     = "finance_costs"  // our vendor COGS, the fleet god-view
	StoreCurrent     = "store_current"  // the org's own storefront
	StoreListing     = "store_listing"  // upsert one product listing
	FinanceSubs      = "finance_subs"   // the org's subscriptions
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

	// ReferralCode answers one org's referral code and share link. Marketing asks
	// it for the member a beta invite is from, so the invite's link credits them;
	// the org is an argument because the caller is a peer, never the member.
	ReferralCode = "referral_code"

	// MarketingInviteJoined marks the beta invites a new signup answered. The
	// referral claim calls it once a referee has claimed a member's code, naming
	// the invite its link carried and the referee's verified address.
	MarketingInviteJoined = "marketing_invite_joined"

	// IAMDirectory is the whole fleet's orgs and people, projected to what the
	// operator console lists. The platform alone may ask for it: it is every
	// tenant at once, which no org-scoped caller may read.
	IAMDirectory = "iam_directory"

	// NotifySend delivers ONE message on the org's configured provider (Twilio for
	// SMS today). It is on the plane because the alternative was a credential: a
	// sibling subsystem calling the HTTP send surface must authenticate as a
	// principal, and notify derives the sending tenant FROM that principal — so a
	// service could only ever send as its own org, while IAM answers for every
	// white-label identity host. Here the org is an argument a peer is trusted to
	// name, which is what lets one process send for every tenant with no secret to
	// mint, mount or rotate.
	NotifySend = "notify_send"

	// MarketingClearance is the ONE suppression check a commercial email asks from
	// another process before it leaves (POST /v1/email): whether the org may mail
	// the address, and the signed unsubscribe link the message carries when it may.
	// The list is marketing's, so the check is too; a sender that asked anything
	// else would be a second gate.
	MarketingClearance = "marketing_clearance"

	// MarketingSuppress puts an address on the org's suppression list: a hard
	// bounce or a complaint a provider reported for mail the org sent.
	MarketingSuppress = "marketing_suppress"

	// MarketingSubscribe mails an address the brand newsletter's double opt-in: a
	// link that subscribes it only when pressed. A form a stranger answered asks
	// it for an address the stranger only claimed.
	MarketingSubscribe = "marketing_subscribe"

	// MarketingSuppressions reads every address on an org's suppression list,
	// following next pages until the walk ends (use total from page one).
	MarketingSuppressions = "marketing_suppressions"

	// EmailSend sends one commercial email for an org from another process, the
	// way POST /v1/email does: the recipient asked of the one suppression list,
	// the signed unsubscribe link in the footer and the headers, the message filed
	// on the CRM. A broadcast to a marketing list reaches each recipient by it, so
	// commercial mail has one sender.
	EmailSend = "email_send"

	// IAMApproval answers "is this person off the waitlist?" about the CALLER.
	//
	// It is on the plane because the alternative was worse than a URL. admission
	// asked IAM over HTTP and, having no way to say who was asking, REPLAYED the
	// caller's own Cookie and Authorization header to do it — cloud presenting a
	// user's raw credential to another service so that service would answer about
	// that user. The plane carries the validated principal, so the credential
	// never has to be handled, let alone forwarded.
	IAMApproval = "iam_approval"

	// IAMEmail answers what address IAM holds for the CALLER, and whether they
	// have PROVED it.
	//
	// It is on the plane and not in the token because the token does not carry it:
	// this deployment's JWT emits `email` and no `email_verified`
	// (hanzoai/iam internal/oidc/jwt.go), while the identity store records the
	// proof on the account (pkg/schema/user.go EmailVerified) and a direct
	// password signup records it FALSE until the address is confirmed. Reading it
	// here means every existing token gets a true answer immediately, where a new
	// claim would answer only for tokens minted after it shipped — and would
	// refuse everyone else in the meantime.
	//
	// The subject is the CALLER'S and can never be an argument, exactly as
	// [IAMApproval] states: a caller able to name a subject could read another
	// person's address, which is a leak on its own and an account oracle in bulk.
	IAMEmail = "iam_email"

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

	// TeamMember answers "what is this person's role in that space?" for a
	// caller that holds an IAM identity and no space claim.
	//
	// It is on the plane because the process that DECIDES a room join (meet) and
	// the process that owns the space membership rows (team) are different
	// ones. Before it, meet could only read a role a space token had signed —
	// which is the second bearer authority the estate is retiring, so the decision
	// had nowhere else to come from.
	TeamMember = "team_member"

	// TeamToken answers "is this bearer a team session token this deployment
	// signed, and what does it name?" for a caller that must DECIDE on one.
	//
	// It is on the plane because the key that signs a team token belongs to
	// team and to nothing else: the process that admits an ingest write under
	// one (event) used to resolve the same secret itself, which is a second copy
	// of a signing key and a second place for "verified" to drift. Only the
	// VERDICT crosses; the key never does.
	TeamToken = "team_token"

	// TeamSpaces answers "which spaces is this person in?" — the same
	// rows TeamMember reads, asked without already knowing the answer.
	//
	// A caller that must DECIDE a join asks TeamMember, because it already holds
	// the space the room named. A caller that must OFFER a join has nothing
	// to name yet: the meet lobby has to show a person the spaces they can
	// open a room in, and a room is bound to its tenant by its name's leading
	// space segment, so without this the native client could only ask the
	// user to type a uuid it has no way to know.
	//
	// It is a person's OWN memberships and never a space's roster: the
	// subject is the caller's, the org rides the call, and the answer is the
	// list of rows that person holds. Nothing here tells one member about
	// another.
	TeamSpaces = "team_spaces"

	// TeamBots reads the org's bots as space members, and TeamBotsSync reconciles
	// them. team owns the rows; the ADDRESS is /v1/bot, because a bot is a bot
	// wherever it happens to be stored.
	TeamBots     = "team_bots"
	TeamBotsSync = "team_bots_sync"

	// ToolsSkills replaces what one repository contributes to the caller's org's
	// skills with the SKILL.md files just read from its default branch. The org's
	// skills are the files in its repositories; a push that adds one adds it, a
	// push that removes one removes it, and nothing is configured to make that so.
	ToolsSkills = "tools_skills"

	// SkillsCatalog answers the deployment brand's agent skills. The skills app
	// embeds the catalogue and the tool app serves /v1/tool; they are two
	// processes, so the tool plane lists the brand's skills by asking for them.
	SkillsCatalog = "skills_catalog"

	// SkillsDocuments answers the brand's SKILL.md documents by skill name.
	SkillsDocuments = "skills_documents"

	// ToolsSkillDocs answers the documents of the skills an agent names, for the
	// org on the call: only the skills that org enabled, the brand's before its
	// own. It is how a skill attached to an agent reaches the agent's prompt.
	ToolsSkillDocs = "tools_skill_docs"

	// ToolsKit answers what an agent run carries of its org's tool plane: the
	// documents of every skill an admin of the org activated, and every MCP
	// server an admin registered with the tools of it an admin activated. A
	// server is answered by its id and nothing more: the run reaches it through
	// ToolsRelay, and its address and credential never leave the tool plane.
	ToolsKit = "tools_kit"

	// ToolsRelay carries one MCP request an agent run makes of one of its org's
	// servers — tools/list or tools/call — to that server, with the server's
	// credential added by the tool plane and the server dialled through its
	// guarded client. It answers only a server an admin registered, and only the
	// tools of it an admin activated.
	ToolsRelay = "tools_relay"

	FrameworkInstalled = "framework_installed"
	FrameworkModule    = "framework_module"
	FrameworkDocs      = "framework_docs"
	FrameworkDoc       = "framework_doc"
	FrameworkIngest    = "framework_ingest"
	FrameworkUpdate    = "framework_update"
	FrameworkDelete    = "framework_delete"
	FrameworkFind      = "framework_find"
	FrameworkLease     = "framework_lease"
	FrameworkRelease   = "framework_release"

	GitFiles   = "git_files"
	GitImport  = "git_import"
	GitInbound = "git_inbound"
	GitPublish = "git_publish"

	// GitLanded tells git that a push landed on a repository's HOST. The
	// trigger is a webhook the provider app verifies; the repositories linked
	// to that host address are git's, which reads the push on their next read
	// and hands a default-branch push to the code index and the skills.
	GitLanded = "git_landed"

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

	// GitMirror declares (or removes) a native repo's OUTBOUND mirror target. The
	// sync engine decides a mirror should exist; the git app owns the repos and
	// the reactor that pushes them. Two processes, so declaring it was a nil call
	// that reported "git mirror controller not registered" while git was healthy
	// in the other process.
	GitMirror = "git_mirror"

	// SyncRun dispatches one reconcile to the universal sync engine. The TRIGGERS
	// are webhooks arriving at integrations and pushes landing at git; the ENGINE
	// is the sync app. Three processes, one of which has the engine.
	SyncRun = "sync_run"

	// TaskUpsert puts one issue on the board, idempotent by ExtRef. A tracker
	// mirror and a coding run both use it: the run files its issue when it starts
	// and moves it as it works.
	TaskUpsert = "task_upsert"

	// PlatformPush turns a landed push into a build. The push lands on git's
	// embedded server and the builder belongs to platform — the single most
	// consequential split in this list, because the client it replaces returned a
	// NIL ERROR: every push in the split fleet triggered no build and said so to
	// nobody.
	PlatformPush = "platform_push"

	// PlatformRelease patches a proven image onto its operator Service CR. Same
	// shape as PlatformPush and the same former silence: a nil error meant "rolled
	// out" to a caller whose CR was never touched.
	PlatformRelease = "platform_release"
	// PlatformJob turns a queued workflow job into a runner that lives for it. The
	// delivery lands at integrations, which holds the provider's credential and
	// mints the registration; the cluster belongs to platform.
	PlatformJob = "platform_job"

	// PlatformBuild builds one image from a repository at a commit — the native
	// pipeline's per-image ask, from git to the builder over the plane. It
	// replaced an HTTP call from this process to its own /v1/build
	// carrying a shared PLATFORM_BUILD_CALLBACK_TOKEN: a hop across the edge to
	// reach a builder this binary already carries, proven by a secret.
	PlatformBuild = "platform_build"

	PlatformFleet   = "platform_fleet"
	TreasuryReserve = "treasury_reserve"

	// O11yAlerts reads what the platform's own ruler holds firing, for the admin
	// console. The rules live in one o11y org and the console's SuperAdmin is not a
	// member of it, so the read crosses to the process that runs the ruler.
	O11yAlerts = "o11y_alerts"

	// The durable engine's org-scoped read, across the process boundary. Each app
	// embeds its OWN engine over its OWN data dir (durable.go: SQLite has one
	// writer, so a shared store would be the collision a shared port already was),
	// which makes "the engine" a per-process fact. A namespace written through one
	// app's surface is therefore invisible to every other app — and the BYO fleet
	// is exactly that: workers register through the tasks surface and visor renders
	// them, so visor read its own empty engine and reported an online GPU as no
	// fleet at all. The engine is asked, not opened, like the ledger above.
	TasksActivities    = "tasks_activities"
	WorkflowActivities = "workflow_activities"

	// The GPU lane's writes, beside the read: put one studio.render job on the
	// org's gpu-jobs lane, read one back, cancel one. The fleet claims from the
	// workflow app's engine, so an app that renders on it (studio) enqueues there
	// rather than in its own engine, where no linked GPU would ever look. They
	// accept that lane and that job type and nothing else.
	WorkflowEnqueue  = "workflow_enqueue"
	WorkflowActivity = "workflow_activity"
	WorkflowCancel   = "workflow_cancel"

	// The lexical index's read, across the process boundary. Same shape of bug as
	// the engine above, and it shipped as a 503 nobody could act on: `catalog`
	// guards its browse on index.Ready(), which reports whether the index is
	// mounted IN THIS BINARY — true when everything was one fused process, false
	// the moment catalog and index became two plugin rows. So /v1/catalog answered
	// {"status":503,"error":"catalog: index not composed"} on every request, and
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

	// The lexical index's per-document writes, across that same boundary: one
	// document upserted, or documents removed by key. A subsystem's save hook runs
	// wherever the framework runs, which is not where the index is, so these
	// answered "index: not mounted" on every save and every delete, and the index
	// kept rows for documents that were gone and none for documents that were new.
	IndexPut    = "index_put"
	IndexRemove = "index_remove"

	// IndexPutAll upserts a batch of documents in one call and one transaction.
	// A file's passages are written a page at a time, thousands of them, and one
	// call apiece put a round trip and a commit behind every passage.
	IndexPutAll = "index_put_all"

	// KnowledgeIndex makes one knowledge document's passages and lexical row
	// match the framework store, in the process that holds the org's passage
	// store. knowledge's save and trash hooks run in the framework's process,
	// which holds none, so a write made through /v1/framework, a connector sync or
	// an import reached the vector leg only through a manual reindex.
	KnowledgeIndex = "knowledge_index"
	// KnowledgeFiles searches the passages of the caller org's workspace files
	// — the files leg of /v1/search, answered where the file index lives.
	KnowledgeFiles = "knowledge_files"
	// KnowledgeSemantic is the vector leg of /v1/search, answered where the
	// org's passage store lives. An empty query asks only whether the leg is
	// here.
	KnowledgeSemantic = "knowledge_semantic"

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
	// AgentsRunOnBehalf is the chat bridges' entry point into a run. A plugin is a
	// PROCESS, so agents.RunOnBehalf — which gates on that package's `mounted`
	// global — can only ever answer when agents happens to be co-resident. It was
	// not, and every @hanzo turn in Slack died on ErrNoPeer.
	AgentsRunOnBehalf = "agents_run_on_behalf"
	// AgentsTurnStart runs a chat turn as a live session and answers at once
	// with its id; AgentsTurnEvents reads what the turn has said since a
	// sequence number, waiting briefly when there is nothing new. Together they
	// replace a blocking AgentsRunOnBehalf for a surface that shows progress:
	// the surface renders the stream and stops the session, and the run's
	// lifetime stays with agents.
	AgentsTurnStart  = "agents_turn_start"
	AgentsTurnEvents = "agents_turn_events"
	// AgentsSessionControl is the steering API for a surface acting for a person
	// it has already identified: stop, pause, resume, or say something to one
	// session, as that person. It is how a chat surface steers a run from where the
	// run is being read — a message said into a running turn becomes the turn's
	// next input, and a run's buttons stop, pause and continue it. The session must
	// be the person's own, in the caller's org.
	AgentsSessionControl = "agents_session_control"
	// AgentsQuestionAnswer answers a question a run asked its person (ask_user),
	// from wherever they answered it: a press on one of its options, or what they
	// said in its thread. Only that person may, and a question is answered once.
	AgentsQuestionAnswer = "agents_question_answer"
	// AgentsRoster is the READ half of the same fact AgentsRunOnBehalf is the write
	// half of, and it was missing for exactly as long. agents.ListForOrg gates on
	// the same per-process `mounted` global, so a surface that projects agents as
	// members — apps/team's Chunter roster — read an empty list in every deployment
	// where agents is its own process, which is every deployment. Worse than the
	// run's ErrNoPeer, because the caller renders absence as "this org has no
	// agents" rather than as a failure.
	AgentsRoster = "agents_roster"

	// AIChat buys one chat completion from the fleet's own model API for the
	// caller. The edge re-mints identity from the credential presented, so a
	// completion sent through the router named a service key and nobody else; on
	// the plane the org and the user ride the call and no credential crosses — ai
	// authenticates to its own model API as this deployment (apps/ai/chat_rpc.go).
	AIChat = "ai_chat"
	// AIChatStream buys one chat completion asked for with "stream": true, exactly
	// as AIChat buys one, and answers a unix socket (Streamed) the model API's
	// answer is framed onto as it is produced, its price last (sdk/infer
	// planestream.go). A typed op is one request and one reply, so a streamed
	// answer cannot ride AIChat and still arrive a delta at a time.
	AIChatStream = "ai_chat_stream"
	// AIEmbed buys embeddings of a sibling's inputs and AIRerank orders its
	// documents against a query, both for the caller, exactly as AIChat buys a
	// completion: the org and project ride the call and the model API is entered
	// as this deployment. They carry the same request and answer bytes AIChat does.
	AIEmbed  = "ai_embed"
	AIRerank = "ai_rerank"
	// AITranscribe buys a transcription of a sibling's recording for the caller,
	// exactly as AIChat buys a completion: the multipart body crosses as it would
	// over HTTP, with its content type, and the price comes back on the answer.
	AITranscribe = "ai_transcribe"
	// AIMedia buys one media call — an image, speech, music, a video or a 3D
	// model, or a read of a video or 3D job it started — from the model API for
	// the caller, exactly as AIChat buys a completion: the org and the person ride
	// the call, and the caller's own meter charges for it.
	AIMedia = "ai_media"
	// AIPool answers the free pool's standing: how many of the platform's vendor
	// accounts for free models there are, how many take a free request now, and
	// when the first that does not comes back. The accounts' state lives in the
	// ai process, which is the only one that sends through them.
	AIPool = "ai_pool"
	// AISpend answers what each of an org's API keys has spent on models, this
	// month and in all. The count lives in the ai process, which records every
	// call a key makes; the key list asks it.
	AISpend = "ai_spend"
	// AIRates answers each Enso model's one fixed price and the models served free;
	// and, to a SuperAdmin, how each price was set, each model's route held to it,
	// why a model is refused, and what the family's calls did over the last day. The
	// prices and the guard live in the ai process, which bills them and records
	// every call (hanzoai/ai rates.go).
	AIRates = "ai_rates"
	// AIRouting answers a model family's routing catalog as the family serves it:
	// the catalog, the newest applied version, and whether it serves that version.
	// AIRoutingApply proposes an edited catalog (Dry), answering its diff and why
	// the family's price guard would refuse it, or applies it against the version it
	// was made from and records a version. Both are a SuperAdmin's alone; the
	// catalog, its versions and the guard live in the ai process (hanzoai/ai
	// routing.go), the one write path onto a family's routing.
	AIRouting      = "ai_routing"
	AIRoutingApply = "ai_routing_apply"

	// TrainCapabilities answers an org's published capabilities, as ai attaches
	// them to each decision it forwards (X-Org-Capabilities). They live in the
	// org's train store, which the train process alone opens.
	TrainCapabilities = "train_capabilities"

	// TransportIngest carries one authenticated inbound chat event from the platform
	// adapters to the channels inbox. It replaces provider.RegisterIngress — a
	// package-global function pointer that channels.Use installed, which is the
	// same mistake AgentsRunOnBehalf above was written to undo. In production
	// integrations, channels and agents are three separate processes, so that
	// pointer was nil on the emitting side and EVERY event was dropped: the inbox
	// took nothing, and the pairing and allowlist gates never saw real traffic.
	// Nothing failed loudly, because a nil consumer simply returns.
	TransportIngest = "transport_ingest"

	// ChatIdentity resolves the Hanzo account a chat user has linked, for the
	// process that runs the turn. Token custody stays in integrations — this
	// answers WHO, never with what. It exists because an in-process lookup gates
	// on integrations' `mounted` global and channels is a different process, so
	// such a call answers "not mounted" every time and the turn runs as nobody.
	ChatIdentity = "chat_identity"

	// ChatSend posts one message back to a chat platform. Token custody lives in
	// integrations — the per-org bot token IS the tenancy gate — so the send has
	// to happen there and only the intent crosses. The four Send* helpers are Go
	// calls gated on that package's `mounted` global, which is nil in the process
	// that now runs the turn: channels could receive and never reply.
	ChatSend = "chat_send"

	// ChatHistory reads the recent conversation transcript from the chat
	// platform itself (e.g. Slack conversations.replies / conversations.history).
	// Token custody stays in provider — the per-org bot token is spent there and
	// only the formatted turns cross. Without this, transport's inbox only sees
	// messages previously addressed to @hanzo, leaving the agent tabula rasa when
	// summoned after prior channel or thread discussion.
	ChatHistory = "chat_history"

	// TransportRecent reads the last turns of ONE channel back out of the inbox, so a
	// chat bridge can answer with the conversation in front of it instead of a
	// single message.
	//
	// It exists because the inbox was already recording every inbound turn and
	// nothing ever read them. A bridge that sends only the newest message makes an
	// assistant that cannot follow a two-line exchange: asked "weather in Benicia",
	// told "try again", it asks WHICH CITY — and by the third turn it is looking
	// "Benicia" up as an org, because a bare noun with no conversation around it
	// looks like a lookup. The record was there the whole time.
	TransportRecent = "transport_recent"

	// TransportAgent names the agent that answers one channel: the org's binding for
	// the channel, else its default for the transport, else the built-in. It exists
	// so the bridges that answer OUTSIDE the channels turn — a slash command that
	// must reply on its response_url — pick the agent the same way a mention
	// does, from the same row, instead of from a deployment variable.
	TransportAgent = "transport_agent"

	// TransportStop stops the turns the asker has running in one channel. It is what
	// `/hanzo stop` reaches: a slash command arrives in the provider, and the
	// conversation a turn is rendering in is known to channels.
	TransportStop = "transport_stop"

	// TransportAct is a press of a run's button — stop, pause or continue — carried
	// from the platform that drew it to channels, which knows who is pressing and
	// where the run is being shown, exactly as it does for the words.
	TransportAct = "transport_act"

	// The x402 rail, across the process boundary, because the four things a
	// settlement needs live in four binaries: the RAIL is x402's, the PRICE is the
	// marketplace's, the PAYEE is wallets', and the LEDGER is commerce's, which
	// draws the payer's half and credits the payee's. One more answers the event
	// plane what one settlement records, because only the rail may say what it
	// moved (economic.go).
	X402Settle    = "x402_settle"    // settle one priced resource, or report it free
	X402Economic  = "x402_economic"  // what one completed settlement records, as an economic event
	MarketPrice   = "market_price"   // what a resource costs, and who is paid
	WalletsPayee  = "wallets_payee"  // resolve a payout wallet to an address + subject
	FinanceCredit = "finance_credit" // credit a subject's ledger (the payee side)
	FinanceDraw   = "finance_draw"   // draw a payment the wallet must cover (the payer side)
	// A payment agreed before it is made — a job's — sets its amount aside when
	// it is agreed, and the draw that makes it takes the held money first; one
	// never made is returned to the wallet.
	FinanceHold   = "finance_hold"   // set a payment's amount aside in the payer's wallet
	FinanceReturn = "finance_return" // return what a hold still keeps to the wallet

	// FinanceReverse undoes one deposit in the caller org's books with its mirror
	// entry, exactly once. The admin grant reversal reaches the ledger through it.
	FinanceReverse = "finance_reverse"

	// FinanceSource records which of the caller org's earlier deposits were
	// payments, and FinanceCash reads what its books owe customers in cash. Both
	// are the ledger owner's: the split between cash and credit is computed in one
	// process only.
	FinanceSource = "finance_source"
	FinanceCash   = "finance_cash"

	// FinanceWires reads the caller org's deposits keyed on a bank wire, each with
	// whether a reversal undid it. A recorded wire is decided against the books of
	// every org the trail names for it, so a lost trail row cannot hide money.
	FinanceWires = "finance_wires"

	// FinanceLine reads the caller org's own account and credit line: what it holds,
	// what it owes, and the line left to draw. FinanceLimit sets the line — a
	// SuperAdmin's act, audited before and after, refused when the trail does not
	// take it. Both are the ledger owner's, because the limit lives in the org's
	// books beside the debt it allows.
	FinanceLine  = "finance_line"
	FinanceLimit = "finance_limit"
	FinanceWaive = "finance_waive"

	// IntegrationsSlackSend posts to an org's Slack channel via the org's
	// KMS-custodied bot token. It lives on the plane because the token store is
	// the integrations PROCESS's alone — a peer plugin (o11y paging an alert)
	// cannot see integrations' in-memory `mounted` token map, so it asks the
	// process that owns it, over the socket, exactly like a debit asks commerce.
	IntegrationsSlackSend = "integrations_slack_send"

	// IntegrationsConnection answers whether the CALLER'S OWN org has connected a
	// provider, and hands back the non-secret metadata describing it.
	//
	// It is on the plane because integrations OWNS that store and every other app
	// is a different PROCESS. The package used to export the same answer as a
	// function reading a package-level handle, which meant a caller in another
	// process got a silent "not connected" for a workspace that was plainly
	// connected — no error, no log, just the wrong answer. A question that crosses
	// a process boundary has to look like one.
	IntegrationsConnection = "integrations_connection"

	// IntegrationsGoogleSend sends one email from the CALLER'S own connected Gmail,
	// as them. The person and the org are the caller's, read from the plane
	// context; the token never crosses — the process that custodies it sends.
	IntegrationsGoogleSend = "integrations_google_send"

	// IntegrationsRun runs one connector action as the CALLER'S org, with the
	// credential the org connected. The credential never crosses: the process
	// that holds it is the one that acts, which is the rule IntegrationsSlackSend
	// follows for Slack, applied to every connector whose program runs here.
	IntegrationsRun = "integrations_run"

	// IntegrationsPost publishes one text post to a social account the CALLER'S
	// org connected — x, linkedin or facebook — with the org's own credential,
	// and answers the post's link or the platform's reason it did not go. The
	// credential never crosses: an automation's post is made where it is kept.
	IntegrationsPost = "integrations_post"

	// SocialRecordTarget stores one connected account on the social plane, so
	// the org manages every account of a network in one list. The token does
	// not cross: only the platform id and the handle do.
	SocialRecordTarget = "social_record_target"

	// SocialForgetTarget removes a connected account from the social plane when
	// the provider disconnects it, so the list the org manages never holds an
	// account with nothing behind it.
	SocialForgetTarget = "social_forget_target"

	// IntegrationsConnect starts an OAuth consent or seals a verified credential
	// on the provider plane for the caller's org, returning either the authorize
	// URL or the connected account metadata without exposing any secret.
	IntegrationsConnect = "integrations_connect"

	// The unified channel and post peer operations on the social plane.
	// apps/channel (/v1/channel) and apps/post (/v1/post) are stateless address
	// surfaces that ask social over the plane so the single-writer social SQLite
	// store and background scheduler stay owned by one process.
	SocialChannelsList      = "social_channels_list"
	SocialChannelsGroups    = "social_channels_groups"
	SocialChannelsProviders = "social_channels_providers"
	SocialChannelsConnect   = "social_channels_connect"
	SocialChannelGet        = "social_channel_get"
	SocialChannelUpdate     = "social_channel_update"
	SocialChannelSetGroup   = "social_channel_set_group"
	SocialChannelDelete     = "social_channel_delete"
	SocialChannelAnalytics  = "social_channel_analytics"
	SocialPostsList         = "social_posts_list"
	SocialPostCreate        = "social_post_create"
	SocialPostGet           = "social_post_get"
	SocialPostUpdate        = "social_post_update"
	SocialPostDelete        = "social_post_delete"
	SocialPostPublish       = "social_post_publish"
	SocialPostAnalytics     = "social_post_analytics"

	// IntegrationsGitHubGrant mints a GitHub App token that reaches ONE
	// repository of an account the CALLER'S org connected, with contents and
	// pull_requests write and nothing else.
	//
	// It is the one op on this process that hands a credential out, because the
	// thing that acts is a sandbox's git, which only a credential can reach. The
	// App key stays here; what leaves is an hour-long token the caller revokes
	// when its run ends.
	IntegrationsGitHubGrant = "integrations_github_grant"

	// IntegrationsGitHubAccount lists the repositories the installation on ONE
	// GitHub account the CALLER'S org connected grants, as GitHub states the grant
	// now — the archived and disabled left out, because they cannot be fetched.
	//
	// It is how an account sync learns which repositories it covers. The App key
	// stays here and no credential crosses: what leaves is names and addresses.
	IntegrationsGitHubAccount = "integrations_github_account"

	// IntegrationsGitHubRunners starts a runner for each job GitHub holds queued,
	// in every organization an org connected, that no runner will take — what
	// platform's queue loop asks once its own queue is drained, stating the runner
	// Jobs it holds. The registrations are minted here, where the App key is, and
	// launched the way a delivery launches one.
	IntegrationsGitHubRunners = "integrations_github_runners"

	// IntegrationsGitHubRun admits a coding run on a GitHub repository that no
	// forge mirror stands for, and mints its one-repository token.
	//
	// The forge's ACL cannot answer for a repository the forge does not hold, so
	// the answer is GitHub's: the CALLER's own GitHub access, read with the token
	// they connected, must cover the repository with push; failing that, an org
	// admin may run on the installations their org holds. Everyone else is
	// refused, and nothing is minted for them.
	IntegrationsGitHubRun = "integrations_github_run"

	// IntegrationsGitHubRepo answers whether the CALLER may see one GitHub
	// repository, and what GitHub says it is — the same view the paged listing
	// answers from: the person's own connected GitHub, else the org's
	// installations for an org admin. A repository outside it is not found, and
	// nothing about it is said.
	IntegrationsGitHubRepo = "integrations_github_repo"

	// IntegrationsGitHubRef answers whether one ref of a GitHub repository is
	// one its maintainers publish from: the default branch, a protected branch,
	// a tag, or a full commit one of those branches contains. A pull request's
	// ref, an unprotected branch and a commit only a fork holds are not. It is
	// asked with the App's own installation on the repository, so the answer is
	// GitHub's and holds for every caller alike.
	IntegrationsGitHubRef = "integrations_github_ref"

	// IntegrationsGitHubIssue opens, edits, comments on or assigns one issue on a
	// GitHub repository an account the CALLER'S org connected holds, through the
	// App. It is the one door cloud writes a GitHub issue through, so an issue
	// filed from a chat thread and a card moved on the board are the same write.
	// Who may is the caller's to have established — the forge's ACL on a mirror,
	// or IntegrationsGitHubRepo on a repository no mirror stands for — and the org
	// decides which installations exist to write through.
	IntegrationsGitHubIssue = "integrations_github_issue"

	// SettingsFleet reads the PLATFORM's own configuration of a product, so a
	// deployment knob is a value an operator edits at admin.hanzo.ai rather than
	// an environment variable that needs a rollout to change.
	//
	// It answers for the reserved platform org and takes no org argument. That is
	// the whole of its safety: settings holds tenants' product config, and an op
	// that let a caller name the org would be a cross-tenant read of it over a
	// socket that carries no principal.
	SettingsFleet = "settings_fleet"

	// SettingsFleetSet replaces the PLATFORM's own configuration of a product —
	// the write half of SettingsFleet, asked by the app that owns that product's
	// knobs once it has checked them. Like the read it names no org: the row is
	// the reserved platform org's, so no argument can address a tenant's. The
	// stated caller must be SuperAdmin, and the change is audited before it is
	// made.
	SettingsFleetSet = "settings_fleet_set"

	// SettingsOrg reads the CALLER ORGANIZATION's non-secret configuration of one
	// product, resolved strictly from cloud.Tenant(ctx) — never from an In field.
	// Secrets are never returned here; an app that needs a secret value reads KMS
	// directly with its own identity under orgs/{org}/settings/{product}/{key}.
	SettingsOrg = "settings_org"

	// SettingsOrgSet updates the CALLER ORGANIZATION's non-secret configuration
	// document and KMS secret-key index for one product, resolved strictly from
	// cloud.Tenant(ctx) — never from an In field. Secret values never travel here:
	// the caller seals secret values directly into KMS at
	// orgs/{org}/settings/{product}/{key} and passes only the key names to index.
	SettingsOrgSet = "settings_org_set"

	// PrefOwn reads the CALLER's own preference document, which is how a run
	// started for a person learns what that person set for their runs. The
	// subject is the org and the person on the call, the same `<owner>/<name>`
	// GET /v1/pref keys on, so there is no argument that could name another
	// person's document.
	PrefOwn = "pref_own"

	// EnvironmentPrepare is what a sandbox run on a codebase carries of that
	// codebase's environment: its install script, its start command, and where
	// each of its secrets lives. It answers refs and never values — the run reads
	// each value from KMS itself, at the moment of use, acting for its own org.
	EnvironmentPrepare = "environment_prepare"

	// EnvironmentSuggest keeps a setup run's answer as its codebase's proposed
	// environment, beside whatever is saved. Nothing proposed runs until a person
	// saves it.
	EnvironmentSuggest = "environment_suggest"

	// EventCapture states ONE occurrence onto the shared event plane, for an app
	// running in another binary.
	//
	// analytics owns POST /v1/event, event.fact and the
	// /v1/event/insights reads over them, and the pod forks one process per app — so a
	// peer that wanted its own facts queryable beside the product's had no way to
	// state one. In-process there is a write core (ingestEvents) and it is
	// unreachable from another pid; over HTTP there is the public edge, which is a
	// second gate, a second credential and a hop through the fleet's own public
	// endpoint to reach a table one socket away.
	//
	// So a peer ASKS the app that owns the plane, exactly as a debit asks commerce
	// and a decision asks risk. The tenant is the CALLER's, minted from the plane
	// principal, so a peer can only ever write into its own organisation's
	// partition — the same rule the HTTP endpoint holds, at the other entry point.
	EventCapture = "event_capture"

	// RiskDecide judges one subject at one lifecycle moment against that
	// organisation's OWN model, for a gate running in another binary.
	//
	// It is on the plane because a process-global cannot cross a process, the same
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
	// is only ever asked is a model that never learns. The published learn endpoint
	// (POST /v1/risk/learn) is an ORGANISATION teaching its own model over HTTP; it
	// is the wrong one for the fact this op carries. A settled payment is observed
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
	// Without it the aggregate halves of the credit endpoint's rule are structurally
	// dead: a fresh organisation is its own payer, so nothing it does teaches the
	// model anything, and pace and fan-out read an empty history for exactly the
	// self-serve fraudster they were built for.
	RiskObserve = "risk_observe"

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
	// in it, read a path, write a path, park it, end it. There is no "upload", no
	// "download" and no "session" here, because those are the CALLER's nouns — a
	// session is a lease and a download is a read, and putting either word on this
	// plane would publish one product's vocabulary as another's contract. There is
	// no "resume" either: a lease that names a parked sandbox is the resume.
	SandboxLease = "sandbox_lease"
	SandboxRun   = "sandbox_run"
	SandboxRead  = "sandbox_read"
	SandboxWrite = "sandbox_write"
	SandboxStop  = "sandbox_stop"
	SandboxPark  = "sandbox_park"
	SandboxEnd   = "sandbox_end"
	// SandboxComputer acts on a desktop sandbox's screen, mouse, keyboard, browser
	// or shell: screenshot, click, move, drag, scroll, type, key, navigate, wait or
	// shell.
	SandboxComputer = "sandbox_computer"
	// SandboxPorts is what listens in a sandbox: the ports a preview can open.
	SandboxPorts = "sandbox_ports"

	// SandboxAttach says SOMEBODY IS WATCHING a project right now.
	//
	// It is another op and not another verb: nothing about the sandbox changes,
	// which is why it is not "resume" or "extend". It reports a fact the sandbox
	// cannot observe for itself — that a human has this project open — and the
	// sandbox's idle clock is a different length depending on the answer.
	//
	// It crosses the plane because the two ends really are two processes. The
	// stream that proves presence is agents' (GET /v1/agent/sessions/stream); the
	// pods are apps/sandbox's; they ship as separate plugin binaries. Nothing else
	// in this fleet knows the fact and nothing else needs it.
	SandboxAttach = "sandbox_attach"

	// SandboxStream opens ONE long-lived ZAP stream from the plane into a unix
	// socket of a sandbox, and answers where to read and write it.
	//
	// It is an op and not a call: a typed op is one request and one reply, and a
	// session with a cell is neither. The op authorises, and what it returns is a
	// unix socket here that carries the stream's bytes untouched, so an envelope that goes
	// in at one end comes out of the other as it went in. It is the plane's only
	// way into a cell that is not an exec: Dev's server, and later the terminal
	// and the screen.
	SandboxStream = "sandbox_stream"

	// The FIGURES client: one question — "what are this org's headline numbers?" —
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
	// answered every question from its fallback while books sat healthy in another
	// process.
	FinanceFigures  = "finance_figures"
	GitFigures      = "git_figures"
	ProjectsFigures = "projects_figures"

	// FlagsHold answers whether the CALLER'S ORG holds one flag — the question a
	// capability that is not yet ga asks about every request that reaches its
	// prefix (HIP-0139 §8, cloud.Stage).
	//
	// It is on the plane because the two ends are different processes: the refusal
	// runs in whichever binary serves the capability, and the definitions live in
	// the flags app's per-org store. It is a plane op rather than a call to
	// /v1/flag because the refusal runs BEFORE the capability's own endpoint, on a
	// request that may carry no credential this deployment can replay — and asking
	// the edge would mean cloud presenting a customer's bearer to cloud.
	//
	// The subject is the org, and the org rides the caller. A flag keyed by
	// capability is a fact about a TENANT — whether that customer has been let into
	// the product — so there is nothing finer to name and no argument that could
	// name it.
	FlagsHold = "flags_hold"
	// SourcesOwns answers whether a person owns a linked source that is not revoked.
	SourcesOwns = "sources_owns"
	// SourcesDispatch resolves and leases a linked desktop login for a /v1/dev session.
	SourcesDispatch = "sources_dispatch"
	// SourcesRelease drops a lease taken by SourcesDispatch.
	SourcesRelease = "sources_release"
	// ComputeListMachines returns the caller org's cloud compute & BYO fleet machines
	// so /v1/machines can present linked desktops and cloud compute in one unified inventory.
	ComputeListMachines = "compute_list_machines"
	// FlagsValue resolves one flag's value for the caller's org.
	FlagsValue = "flags_value"
	// FlagsBoard is the operator switchboard: every declared flag with the value
	// in force.
	FlagsBoard = "flags_board"
	// FlagsSet writes one platform switch. Only a SuperAdmin's call is answered.
	FlagsSet = "flags_set"
	// FlagsOn answers whether a PRINCIPAL switch — a platform switch decided per
	// caller, `beta` or an app's beta.<id> — is on for the caller: its validated org
	// and user, which ride the call and are never a field. An error is not an
	// answer, and a caller reads it as off.
	FlagsOn = "flags_on"
	// FlagsGrant lets one person into a principal switch, or out of it: their IAM
	// id added to or taken from the switch's "user" rule, every other rule kept.
	// Only a SuperAdmin's call is answered, and every grant is on the audit trail.
	FlagsGrant = "flags_grant"

	// EntitlementHolds answers whether the caller's org has turned on one product.
	// A framework module asks it on every document op (apps/framework/elective.go).
	//
	// Distinct from [FlagsHold]: a flag says whether a capability is finished
	// enough to show a customer, this says whether the customer asked for it. The
	// subject is the org, which rides the caller.
	EntitlementHolds = "entitlement_holds"
	// EntitlementGrant turns one product in beta on for the caller's org. The
	// public intake calls it for the org of a person who applies to the beta while
	// signed in (apps/marketing leads.go); only a product in beta can be granted
	// this way, and every grant is on the audit trail.
	EntitlementGrant = "entitlement_grant"

	// S3UploadForm signs a POST form a browser uploads ONE object through, into a
	// bucket of the caller's org: the key, the content type and the size bound
	// are the app's, so the form can carry nothing else. The careers form's
	// resume (apps/marketing resume.go) goes up this way.
	S3UploadForm = "s3_upload_form"

	// AutoProviderRun runs one connector action in auto's in-process runtime with
	// a credential the caller resolved itself, for the caller's org. It is the
	// in-process form of POST /v1/auto/provider/{id}/run.
	AutoProviderRun = "auto_provider_run"

	// IAMRoles answers the caller's effective role names in their own org — what a
	// capability enforcing per-role permissions needs, and the reason no subsystem
	// keeps a role table of its own.
	//
	// The subject is the caller: both the org and the user ride the call, so a
	// caller can name neither and cannot read anyone else's grants.
	IAMRoles = "iam_roles"

	// IAMMembers lists who may act in one scope of the caller's org, with the role
	// each holds and the name IAM has for them. It is the read that lets a
	// subsystem show a roster without keeping one.
	IAMMembers = "iam_members"

	// IAMGrant records that a user may act in a scope of the caller's org, with a
	// role. Idempotent.
	IAMGrant = "iam_grant"

	// IAMSeats counts the caller's org's billable people. IAM answers it because
	// IAM holds what decides it — who is a machine, who is disabled, and who holds
	// a grant — and a subsystem computing its own would bill from a second roster.
	IAMSeats = "iam_seats"

	// IAMFederated resolves the caller's org member who signed in through an
	// external identity — GitHub today — from that provider's own subject. It
	// exists so a webhook that arrives with a GitHub user id can run a turn as
	// the Hanzo person it belongs to, without a second sign-in and without
	// trusting a mention to name its author. Absent means no member of THIS org
	// carries that identity; it never reaches across tenants.
	IAMFederated = "iam_federated"

	// IAMLinked is IAMFederated read the other way: the caller's OWN subject at
	// an external provider — the numeric GitHub user id IAM wrote at sign-in. It
	// is how "assign it to me" becomes a GitHub login without a table of our own
	// mapping people to accounts. Absent means the caller never signed in with it.
	IAMLinked = "iam_linked"

	// AdminAuditAppend appends one record to the deployment's audit trail. admin
	// holds the trail — the one store /v1/admin/audit reads — and is its one
	// writer, so every other process hands its records here. An append that did
	// not land is an error, and the caller fails closed on it.
	AdminAuditAppend = "admin_audit_append"

	// AdminAuditQuery reads the trail for the caller's org, newest first: the
	// org-scoped reads (/v1/audit, compliance, the trust center) of a process
	// that does not hold it. The org is the caller's; there is no fleet-wide
	// read here, because that one is admin's own.
	AdminAuditQuery = "admin_audit_query"

	// LibraryPut writes one thing a person made into the Library: the bytes go to
	// the library's own bucket under the caller's org and person, the row to its
	// one table. The org and the owner are the caller; an input names neither.
	// The same (owner, source, key) answers the item already made.
	LibraryPut = "library_put"
	// LibraryReserve opens an item too large to send inline: the row, and a
	// presigned PUT for the item's own key. LibraryCommit finishes it.
	LibraryReserve = "library_reserve"
	// LibraryCommit reads back what was PUT against a reservation and makes the
	// item ready, refusing an object larger than reserved.
	LibraryCommit = "library_commit"
	// LibraryGet reads one item the caller may see — theirs, or shared in their
	// org — with fresh signed URLs.
	LibraryGet = "library_get"

	// AutoJobTrack records one run of background work a product started — a
	// Studio render, a long video — so it is listed in Scheduled with the rest:
	// upserted by (org, product, ref), owned by the caller, and never moved to
	// another person.
	AutoJobTrack = "auto_job_track"

	// BaseImport grows the caller's org Base with a module Base ships or with
	// collections the calling app owns: what is missing is created and nothing
	// that exists is dropped or changed. It is how an app gives an org's Base the
	// schema it writes into.
	BaseImport = "base_import"
	// BaseUpsert writes records into one collection of the caller's org Base, in
	// one transaction: a record carrying an id, or a value of the key field that
	// matches, is updated; any other is created. It is the one way an app writes
	// to an org's Base.
	BaseUpsert = "base_upsert"
	// BaseGet reads one record of the caller's org Base.
	BaseGet = "base_get"
	// BaseList reads a page of one collection of the caller's org Base, under a
	// filter.
	BaseList = "base_list"
	// BaseDelete removes records from one collection of the caller's org Base, by
	// id, in one transaction; a record already gone is not an error.
	BaseDelete = "base_delete"
)

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
// POST /v1/tool/call, with the tool named in the body — the path cannot say
// which capability is being bought.
const toolResourcePrefix = "tool:"

// ToolResource names one tool as an x402 resource: the source the dispatch
// resolved it from, and its name. The tool plane builds it to ask what a dispatch
// costs; the marketplace keys its price table on it. One spelling, in the package
// both import, because a caller and a callee that spell a resource differently do
// not fail — they quietly agree the tool is free.
//
// The source is in it because a name is not a tool. Names are per org — every org
// that wrote a function "summarize" has its own function_summarize — so a price
// keyed on the name alone charged one org for running its own function and paid
// whoever listed another of the same name. The source says whose row ran.
func ToolResource(source, tool string) string { return toolResourcePrefix + source + ":" + tool }

// ToolOf reads one back. ok=false means the resource is not a tool at all, which
// is every request path the payment middleware asks about.
func ToolOf(resource string) (source, tool string, ok bool) {
	rest, ok := strings.CutPrefix(resource, toolResourcePrefix)
	if !ok {
		return "", "", false
	}
	source, tool, ok = strings.Cut(rest, ":")
	return source, tool, ok && source != "" && tool != ""
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
// are separate bits rather than one string a caller has to match on. CapUnread is
// a funded caller whose spend cap could not be checked — unknown, but unknown about
// the CAP, so the caller can say so; Reason carries why. A Reason alone is an
// upstream failure. Both are UNKNOWN, failed closed on, and never read as permission.
type Verdict struct {
	OK        bool   `json:"ok"`
	NoFunds   bool   `json:"noFunds,omitempty"`
	CapSpent  bool   `json:"capSpent,omitempty"`
	CapUnread bool   `json:"capUnread,omitempty"`
	Reason    string `json:"reason,omitempty"`
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
	// Cash says only the wallet's cash pays: the usage policy refused granted credit
	// for the model, so the debit draws nothing from a grant.
	Cash bool `json:"cash,omitempty"`
	// Class is the model's usage class: premium, ours or free.
	Class string `json:"class,omitempty"`
	// Units is the work served, in the unit the call is priced in (tokens for a model).
	Units int64 `json:"units,omitempty"`
	// PaidBy is who pays: plan or hanzo move no money and are counted; empty is the
	// wallet, and the ledger records which of prepaid, line and credits paid.
	PaidBy string `json:"paidBy,omitempty"`
	// Key is the API key the call arrived on, "<org>/<id>": attribution beside Actor.
	Key string `json:"key,omitempty"`
	// Cost is what serving the call cost the platform, exact; absent when unknown.
	Cost *Money `json:"cost,omitempty"`
}

// DrawIn draws one payment from the caller org's prepaid wallet, all or nothing.
// A metered act is admitted before it runs and debited after, so its debit
// ([RecordIn]) lands whatever the wallet holds; a payment is decided before
// anything is sold, so it is refused when the wallet cannot cover it.
type DrawIn struct {
	// Subject is the account within the caller's org; empty draws the org's own.
	Subject string `json:"subject,omitempty"`
	// Amount is the payment, exact.
	Amount Money `json:"amount"`
	// Ref names the payment, and is the idempotency key: a second draw under the
	// same ref answers the first and moves nothing.
	Ref string `json:"ref" validate:"required"`
	// Memo says what was paid for.
	Memo string `json:"memo,omitempty"`
}

// Drawn is a draw that landed.
type Drawn struct {
	// Entry is the ledger entry the payment is posted under.
	Entry string `json:"entry"`
	// Posted is whether THIS call moved the money; false for a replay of a draw
	// already posted.
	Posted bool `json:"posted"`
}

// HoldIn sets one payment's amount aside in the caller org's prepaid wallet,
// until a draw under the same ref makes the payment or a return gives it up.
type HoldIn struct {
	// Subject is the account within the caller's org; empty holds from the org's own.
	Subject string `json:"subject,omitempty"`
	// Amount is the payment to hold, exact.
	Amount Money `json:"amount"`
	// Ref names the payment, and is the idempotency key: the same hold again
	// answers the first, and the draw that makes the payment names it too.
	Ref string `json:"ref" validate:"required"`
	// Memo says what the money is held for.
	Memo string `json:"memo,omitempty"`
}

// Holding is a hold that stands.
type Holding struct {
	// Entry is the ledger entry the hold is posted under.
	Entry string `json:"entry"`
	// Posted is whether THIS call set the money aside; false for a replay.
	Posted bool `json:"posted"`
}

// ReturnIn gives up one hold in the caller org's books.
type ReturnIn struct {
	// Subject is the account within the caller's org; empty is the org's own.
	Subject string `json:"subject,omitempty"`
	// Ref names the payment the hold was for.
	Ref string `json:"ref" validate:"required"`
	// Memo says why the payment was given up.
	Memo string `json:"memo,omitempty"`
}

// Returned is what a return put back in the wallet: zero once the payment was
// made or the hold already returned.
type Returned struct {
	Amount Money `json:"amount"`
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

// ---- allowance -------------------------------------------------------------

// AllowanceIn names the subject whose plan allowance is being counted or read. The
// ORG is the caller's and cannot be named here, so one tenant can never spend
// another's allowance.
type AllowanceIn struct {
	// Subject is whose count this is: for free AI calls the person (Person), for
	// sandbox leases the wallet.
	Subject string `json:"subject" validate:"required"`
	// Meter is which allowance: empty counts free AI calls, AllowanceSandbox counts
	// free sandbox leases. Each has its own ceilings and its own rows.
	Meter string `json:"meter,omitempty"`
	// Payer is the wallet whose plan and balance decide the ceiling, within the
	// caller's org; empty is Subject. A person in a pooled org is counted alone and
	// held to the plan the pool pays for.
	Payer string `json:"payer,omitempty"`
	// At is when the call being given back was taken, unix seconds (give only): the
	// unit returns to the hour and day it was counted in.
	At int64 `json:"at,omitempty"`
	// Start is the session the call being given back was counted in: the Start its
	// take answered, unix seconds (give only). The unit returns to that session and
	// no other; 0 returns nothing to a session.
	Start int64 `json:"start,omitempty"`
}

// Person is whose free AI calls a principal spends: "<home org>/<username>", the IAM
// owner and username the identity boundary minted — the same two halves a wallet is
// keyed by — so a browser session and an API key of one user share a count, and two
// members of one org never do. Without a username it is the user id. A user already
// spelled "<org>/<name>" — the person a machine credential acts for — is that person.
// A principal with no user is its org's own: a service counted as itself. Empty when
// there is nobody at all.
func Person(who zip.Caller) string {
	user := strings.TrimSpace(who.User)
	if name := strings.TrimSpace(who.Name); name != "" && !strings.Contains(name, "/") && !strings.Contains(user, "/") {
		user = name
	}
	org := strings.TrimSpace(who.Owner)
	if org == "" {
		org = strings.TrimSpace(who.Org)
	}
	switch {
	case strings.Contains(user, "/"):
		return user
	case user == "":
		return org
	case org == "":
		return ""
	}
	return org + "/" + user
}

// AllowanceSandbox is the meter of sandbox leases a plan grants without payment:
// the free lane of compute, taken when a wallet cannot cover a lease's first hour.
const AllowanceSandbox = "sandbox"

// AllowanceAutomations is the meter of automation runs a paid plan includes: a run
// on a paid plan is counted here instead of charged (apps/auto plan.go).
const AllowanceAutomations = "automations"

// Allowance is how much of a plan's call allowance a subject has left in the window
// that holds them, and when that window reopens.
//
// Limit 0 means the subject's plan does not bound them; Used and Spent are then 0
// and false, and Resets is meaningless — there is no window to end.
type Allowance struct {
	Plan  string `json:"plan,omitempty"` // the tier the limit came from
	Limit int64  `json:"limit"`          // calls the plan allows in Window; 0 = unbounded
	// Used is how many free calls this subject has been admitted in the window
	// ending at Resets. A call counts when it is admitted, whatever its upstream then
	// does; a refused call counts nothing. It stops AT Limit rather than climbing
	// past it, so Limit-Used is what remains and never goes negative.
	Used int64 `json:"used"`
	// Spent says the ceiling refuses: on a take, that THIS call was refused and
	// nothing was counted; on a read, that the next call would be.
	Spent bool `json:"spent"`
	// Window is which ceiling these numbers describe — "hour", "session" or "day" —
	// because a caller can be held to more than one and only one of them is the
	// answer. A session is the Free plan's: it opens at the subject's first call
	// after the last one ended and runs five hours (ai_plan_session_hours). It is
	// the window that REFUSED where one did, and otherwise the one with least left,
	// so Limit-Used is always the number that will actually stop them next. Empty
	// where no window bounds the subject at all.
	Window string `json:"window,omitempty"`
	// Resets is the instant THAT window reopens, unix seconds: the next UTC hour or
	// midnight, or a session's start plus its length — exact, to the second, from
	// the call that opened it. 0 for a session that is not running: it opens at the
	// next call.
	Resets int64 `json:"resets"`
	// Start is when the session this answer counts in opened, unix seconds: on a
	// take, the session the call was counted in, which a give names back
	// (AllowanceIn.Start); on a read, the session running. 0 where no session bounds
	// the subject or none runs.
	Start int64 `json:"start,omitempty"`
	// Pooled says the calls counted here are served from the pool every free user
	// shares — the platform's vendor accounts for free models — rather than from
	// the subject's own plan. True exactly where a window bounds the subject.
	Pooled bool `json:"pooled"`
	// Pool is that shared pool's standing, where it could be read. Absent when the
	// subject is not pooled, and when the process that holds it did not answer —
	// never a guess.
	Pool *Pool `json:"pool,omitempty"`
}

// PoolIn asks for the free pool's standing. It names nothing: the pool is one,
// shared by every free caller.
type PoolIn struct{}

// Pool is the free pool's standing: the platform's vendor accounts for free
// models, spent as one by every free user. It is what the accounts' own answers
// said. It carries no key, no key id and no count of accounts: the state is what
// a person sharing the pool needs, and how many accounts stand behind it is ours.
type Pool struct {
	// State is "available" (every account takes free requests), "busy" (some
	// account is at its vendor limit, or all are and one is back within a minute)
	// or "exhausted" (none is until Resets).
	State string `json:"state"`
	// Resets is when the pool refills, unix seconds: set when it is exhausted,
	// and when it is busy for less than a minute. Absent otherwise.
	Resets int64 `json:"resets,omitempty"`
}

// CapabilitiesIn asks for the caller org's published capabilities. It names
// nothing: the org rides the call.
type CapabilitiesIn struct{}

// Capabilities is an org's published capabilities as the decision service reads
// them attached to the org's decisions.
type Capabilities struct {
	// Set is the X-Org-Capabilities value: a JSON array, by name, each
	// {name, sha256, base_sha256, form, route}; "[]" when the org has published none.
	Set string `json:"set"`
}

// KeysIn names API keys by id within the caller's org.
type KeysIn struct {
	Keys []string `json:"keys"`
}

// KeySpends is what each named API key has spent, in the order asked.
type KeySpends struct {
	Keys []KeySpend `json:"keys"`
}

// KeySpend is one API key's spend on models in cents, rounded up: this calendar
// month (UTC) and in all.
type KeySpend struct {
	Key   string `json:"key"`
	Month int64  `json:"month"`
	Total int64  `json:"total"`
}

// Balance is what is left to spend, and where it came from: Cash is money the
// customer paid, Credit is money the platform minted, Line what is left of the org's
// credit line, and all three buy work. Cash + Line + Credit == Amount, and none is
// negative. Owed is what the account owes — usage the line paid for, invoiced, paid
// down by the next payment — and Limit the line itself. A reader that finds Line,
// Owed or Limit empty is talking to a ledger that does not report them: unknown, not
// zero.
type Balance struct {
	Amount Money `json:"amount"`
	Cash   Money `json:"cash"`
	Credit Money `json:"credit"`
	Line   Money `json:"line"`
	Owed   Money `json:"owed"`
	Limit  Money `json:"limit"`
}

// ---- finance.usage / finance.txns — the statement -------------------------

// UsageRow is one served call: what was metered, how much, and when — a $0 call
// included. Class, Units, Cost and PaidBy are what the usage log counted; a debit
// written before the log carries none of them.
type UsageRow struct {
	ID        string `json:"id"`
	Model     string `json:"model,omitempty"`
	Amount    Money  `json:"amount"`
	CreatedAt int64  `json:"createdAt"`
	Class     string `json:"class,omitempty"`
	Units     int64  `json:"units,omitempty"`
	Cost      Money  `json:"cost"`
	PaidBy    string `json:"paidBy,omitempty"`
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

// VendorCost is one line of what WE pay a vendor in a period.
//
// Source says whether the figure is the vendor's own invoice or our meter's
// estimate, and Note carries the honest reason when it is the latter. Both
// travel: a total that cannot say which half is measured is a number nobody can
// act on.
type VendorCost struct {
	Vendor      string `json:"vendor"`
	Service     string `json:"service"`
	AmountCents int64  `json:"amountCents"`
	Period      string `json:"period"`
	Source      string `json:"source"`
	Note        string `json:"note,omitempty"`
	Currency    string `json:"currency"`
}

// CostsIn names the billing period, YYYY-MM. Empty means the current one, which
// is what the endpoint fronting this has always defaulted to. Test selects the
// sandbox books, which are physically separate from real money, so the selector
// travels rather than being inferred at the far end.
type CostsIn struct {
	Period string `json:"period,omitempty"`
	Test   bool   `json:"test,omitempty"`
}

// Costs is the vendor breakdown for a period, and its total.
type Costs struct {
	Period     string       `json:"period"`
	Vendors    []VendorCost `json:"vendors"`
	TotalCents int64        `json:"totalCents"`
	Currency   string       `json:"currency"`
}

// StoreIn asks for the caller org's own store. It has no fields: the org rides
// the caller, and an org has one store, so there is nothing else to name.
type StoreIn struct{}

// Store identifies an org's storefront.
type Store struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// ListingIn upserts one product listing, keyed by the product slug.
//
// Patch is decoded ONTO the existing listing rather than replacing it, so a
// caller setting a header image preserves the curated name, price and copy it
// says nothing about. It is raw JSON because the shape belongs to the listing
// model, and re-declaring it here would be a second copy of it.
type ListingIn struct {
	StoreID string          `json:"storeId"`
	Key     string          `json:"key"`
	Patch   json.RawMessage `json:"patch"`
}

// Listed reports whether the listing already existed — the difference between a
// 200 and a 201 on the endpoint that fronts this.
type Listed struct {
	Existed bool `json:"existed"`
}

// Sub is one subscription: which plan, what state, and the period it is in.
//
// It carries the plan SLUG rather than the plan, because a caller deciding "is
// this org on a paid tier" needs the name and not the catalog entry, and sending
// the entry would make every reader inherit the plan model.
type Sub struct {
	ID        string `json:"id"`
	UserID    string `json:"userId,omitempty"`
	PlanID    string `json:"planId,omitempty"`
	PlanSlug  string `json:"planSlug,omitempty"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt,omitempty"`

	// PlanName is what a person calls this plan, for a surface that shows it.
	PlanName string `json:"planName,omitempty"`

	// MRRCents is what this subscription contributes per month — commerce's own
	// figure, interval-normalized and multiplied by its seats.
	//
	// It travels rather than being derived by the reader, because a reader that
	// derived it needed its own copy of commerce's interval normalization AND the
	// seat count, and the admin surface that did exactly that read a ten-seat plan
	// as one seat. Commerce bills price x quantity; it is the authority on what a
	// subscription is worth, and everyone else is a display.
	MRRCents int64 `json:"mrrCents,omitempty"`
}

// SubsIn filters the org's subscriptions. Both fields are optional and an empty
// one does not filter — the same meaning an absent query parameter has always
// had on the HTTP endpoint this shares its query with.
//
// There is no org field, for the reason every input here lacks one: the tenant
// rides the caller, so a caller that could name an org could read another
// tenant's subscriptions.
type SubsIn struct {
	UserID string `json:"userId,omitempty"`
	Status string `json:"status,omitempty"`
	Claim  string `json:"claim,omitempty"`
}

// Subs is the org's subscriptions.
type Subs struct {
	Rows []Sub `json:"rows"`
}

// TxnsIn selects which books to read and how much of them.
//
// Test picks the SANDBOX ledger. Sandbox money and real money live in
// physically separate files and must never mix, so the selector travels rather
// than being inferred at the far end: a reader that posts test rows into real
// revenue has restated the company's income, and nothing downstream can tell.
//
// Limit is a page size; 0 takes the ledger's own default.
//
// Subject names the WALLET whose entries to read, exactly as [BalanceIn] names
// the wallet whose balance to read — one vocabulary, so the movement list and
// the standing balance answer for the same account. It is NOT an org: the
// tenant rides the caller, and a subject can only ever address a wallet inside
// the ledger that caller's identity already pinned. Empty reads every wallet in
// the org, which is what the revenue ingest wants and what a pooled org's own
// subject resolves to anyway.
type TxnsIn struct {
	Test    bool   `json:"test,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Subject string `json:"subject,omitempty"`
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
	// Paid is the part of Consumed that cash paid for — money customers paid, never
	// minted credit. Payout programs (affiliate, referral, author) accrue on it.
	Paid Money `json:"paid"`
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
	ID    string `json:"id"`              // the person's identity id, stable across a rename
	Owner string `json:"owner"`           // the org that owns the record — the tenancy key
	Name  string `json:"name"`            // the person's name within that org, unique there
	Email string `json:"email"`           // the address to reach them at
	Admin bool   `json:"admin,omitempty"` // an admin of the org: who billing mail for the org's own account goes to
}

// Roster is who an org may mail.
type Roster struct {
	Recipients []Recipient `json:"recipients"` // everyone in the org who may be mailed; empty is a real answer, not an error
}

// ---- iam.directory ---------------------------------------------------------

// DirectoryOrg is one organization as the operator console lists it.
type DirectoryOrg struct {
	Name    string `json:"name"`    // the org's slug, the tenancy key
	Display string `json:"display"` // what the org calls itself; empty when unset
	Created string `json:"created"` // RFC 3339, as IAM recorded it
}

// DirectoryUser is one account as the operator console lists them. It carries no
// credential column: what a directory row shows, and nothing a row could be used
// to sign in with.
type DirectoryUser struct {
	ID         string `json:"id"`         // the stable identity id, the token's sub
	Owner      string `json:"owner"`      // the org that owns the record
	Name       string `json:"name"`       // unique within Owner
	Email      string `json:"email"`      // empty when the person has none
	Verified   bool   `json:"verified"`   // the person proved Email; false when they have not, or have none
	Display    string `json:"display"`    // empty when unset
	Type       string `json:"type"`       // IAM's account type: normal-user, service-account or application; empty on a legacy row
	Tag        string `json:"tag"`        // IAM's free-form tag
	Created    string `json:"created"`    // RFC 3339
	LastSignin string `json:"lastSignin"` // RFC 3339; empty when never
	Admin      bool   `json:"admin"`      // an admin of Owner
	Forbidden  bool   `json:"forbidden"`  // barred from signing in
}

// DirectoryMember is one person's grant in an org: they are its member.
type DirectoryMember struct {
	User string `json:"user"` // the member's "<owner>/<name>", as a DirectoryUser names it
	Org  string `json:"org"`  // the org the grant is in
	Role string `json:"role"` // owner, admin or member
}

// Directory is every org, every live person in the identity store, and every org
// each person is a member of.
type Directory struct {
	Orgs    []DirectoryOrg    `json:"orgs"`
	Users   []DirectoryUser   `json:"users"`
	Members []DirectoryMember `json:"members"`
}

// ---- notify.send -----------------------------------------------------------

// Send is one message to deliver: who it goes to, over which channel, on whose
// behalf. The provider is chosen by notify from the ORG's own credentials, so it
// is deliberately absent — a caller able to name one could route another tenant's
// message through an account it does not own.
type Send struct {
	Org     string `json:"org"`               // the tenant to send as; notify resolves ITS provider credential
	Channel string `json:"channel"`           // "sms" or "email"
	To      string `json:"to"`                // phone number for sms, address for email
	Subject string `json:"subject,omitempty"` // carried on email only
	Body    string `json:"body"`              // the message, sent verbatim; an email's body is HTML
	Text    string `json:"text,omitempty"`    // email only: the same message as plain text, sent beside the HTML body
}

// ---- marketing.clearance ---------------------------------------------------

// Addressee is one address an org mails, on one channel.
type Addressee struct {
	Org     string `json:"org"`              // the org sending; its list, and the platform's, are read
	Channel string `json:"channel"`          // email, the only channel a provider reports on; empty is email
	Address string `json:"address"`          // the recipient
	Reason  string `json:"reason,omitempty"` // MarketingSuppress only: why the address goes on the list
	List    string `json:"list,omitempty"`   // MarketingClearance only: the marketing list the mail is for, whose own one-click link it then carries
}

// SubscribeAsk is an address to mail the brand newsletter's double opt-in, and
// where the ask came from.
type SubscribeAsk struct {
	Address string `json:"address"`
	Source  string `json:"source,omitempty"`
	// From is the address the ask was made from: counted against the same
	// per-caller window the public subscribe counts a browser's against.
	From string `json:"from,omitempty"`
}

// Clearance is whether an org may send commercial mail to an address.
type Clearance struct {
	Refused     string `json:"refused,omitempty"`     // why not: unsubscribed, company unsubscribed or blocklisted; empty when it may
	Unsubscribe string `json:"unsubscribe,omitempty"` // when it may: the signed one-click link, for the footer (email.Footer) and List-Unsubscribe
}

// ---- email.send ------------------------------------------------------------

// EmailLetter is one commercial email to one recipient.
type EmailLetter struct {
	Org      string `json:"org"`                // the org sending; its suppression list and its provider credentials are read
	To       string `json:"to"`                 // the recipient
	From     string `json:"from,omitempty"`     // the sender's address; empty is the provider's default for the org
	Name     string `json:"name,omitempty"`     // the sender's name
	ReplyTo  string `json:"replyTo,omitempty"`  // where replies go
	Subject  string `json:"subject"`            // the subject line
	HTML     string `json:"html,omitempty"`     // the message as HTML
	Text     string `json:"text,omitempty"`     // the message as plain text
	Provider string `json:"provider,omitempty"` // ses or sendgrid; empty is the org's configured provider
	List     string `json:"list,omitempty"`     // the marketing list it is for: its one-click link is the list's
	Key      string `json:"key,omitempty"`      // makes a retried send one message
}

// EmailPosted is what EmailSend did with one letter.
type EmailPosted struct {
	Message string `json:"message,omitempty"` // the provider and its id for the message (ses:0100…), which its delivery events name
	Refused string `json:"refused,omitempty"` // why the suppression list held the recipient; nothing was sent
}

// ---- marketing.suppressions ------------------------------------------------

// SuppressionsIn asks for one page of an org's suppression list, newest first.
// Org is required; Channel and Prefix are optional filters; Cursor continues a
// previous page.
type SuppressionsIn struct {
	Org     string `json:"org"`               // the org whose list to read
	Channel string `json:"channel,omitempty"` // email, sms, ...; empty is email
	Prefix  string `json:"prefix,omitempty"`  // address prefix to match
	Cursor  string `json:"cursor,omitempty"`  // where the previous page ended; empty is the first
	Limit   int    `json:"limit,omitempty"`   // page size; 0 means the server's default
}

// SuppressionListPage is one page of an org's suppression list.
type SuppressionListPage struct {
	Data  []Suppression `json:"data"`
	Next  string        `json:"next,omitempty"` // the next page's cursor; empty after the last
	Total int64         `json:"total"`          // the true count of the org's opt-outs on this channel
}

// Suppression is one opt-out record shared by marketing's client and server.
type Suppression struct {
	Org       string `json:"org"`
	Channel   string `json:"channel,omitempty"`
	Address   string `json:"address,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

// ---- flags.hold ------------------------------------------------------------

// Beta is the principal switch that decides beta access: whether the caller sees
// catalog items and console products that are not yet generally available. A
// SuperAdmin targets it; nobody sets it for themselves. Ask it with [FlagsOn].
const Beta = "beta"

// NetTerms names the organizations approved to start a plan they ordered by
// invoice before they pay it: the plan opens with the order and the invoice stays
// open. A platform switch whose value is the approved org names, comma-separated;
// empty, as it is with nothing stored, approves none. Only a SuperAdmin sets it,
// and the switch store audits every change. Read it with [FlagsValue].
const NetTerms = "billing_net_terms"

// OnAccount names the organizations on account: billed in arrears and settled out
// of band, so no gate refuses one of their calls for money. Their usage is metered,
// limited and logged as anyone's, and what it costs accrues on the org's own
// account as what it owes until a recorded payment pays it down. A platform switch
// whose value is the org names, comma-separated; empty puts none on account. Only a
// SuperAdmin sets it, and the switch store audits every change. Read it with
// [FlagsValue].
const OnAccount = "billing_on_account"

// OnAccountAlert is what an org on account may owe, in US cents, before the
// SuperAdmins are told: a row on the audit trail and a pulse on the live feed, once
// a UTC day per org while it owes that much or more. 0, as with nothing stored,
// tells nobody. A platform switch; read it with [FlagsValue].
const OnAccountAlert = "billing_on_account_alert"

// PlatformBuilds and PlatformBuildsPerOrg are how many builds the build door runs at once, in
// all and for one org. A build past either waits its turn and starts as one finishes; none is
// refused for them. Platform switches a SuperAdmin sets on the board, read with [FlagsValue]; a
// plane that cannot be read leaves the seeds.
const (
	PlatformBuilds       = "platform_builds"
	PlatformBuildsPerOrg = "platform_builds_per_org"

	PlatformBuildsSeed       = 48
	PlatformBuildsPerOrgSeed = 20
)

// FlagIn names the flag to evaluate, and nothing else. The subject rides the
// caller — its org for [FlagsHold], its org and user for [FlagsOn] — which is
// why there is no field for one.
// SourceOwnsIn names a linked source (apps/source) and the person asking about it.
type SourceOwnsIn struct {
	ID      string `json:"id"`      // the source id, src_ and 32 hex digits
	Subject string `json:"subject"` // the person's IAM subject
}

// SourceOwned is the answer to SourcesOwns.
type SourceOwned struct {
	Owned bool `json:"owned"`
}

// SourceDispatchIn asks the sources app to resolve and lease a linked desktop login
// for a /v1/dev session (HIP-1350).
type SourceDispatchIn struct {
	ID       string `json:"id"`       // the source id, src_ and 32 hex digits
	Subject  string `json:"subject"`  // the person's IAM subject
	Provider string `json:"provider"` // the provider catalog name, e.g. claude
	Instance string `json:"instance"` // the login instance id on the source, e.g. claude-1
}

// SourceRouteOut is the answer to SourcesDispatch.
type SourceRouteOut struct {
	Zap      string `json:"zap"`
	Provider string `json:"provider"`
	Instance string `json:"instance"`
	Lease    string `json:"lease"`
	Expires  string `json:"expires"`
}

// SourceLeaseIn asks the sources app to drop a lease held by Subject on ID.
type SourceLeaseIn struct {
	ID      string `json:"id"`      // the source id
	Subject string `json:"subject"` // the person's IAM subject
	Lease   string `json:"lease"`   // the lease id returned by SourcesDispatch
}

// SourceReleasedOut is the answer to SourcesRelease.
type SourceReleasedOut struct {
	Lease    string `json:"lease"`
	Released bool   `json:"released"`
}

// ComputeMachine is one cloud-compute or BYO fleet machine returned by ComputeListMachines.
type ComputeMachine struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider,omitempty"`
	Region      string `json:"region,omitempty"`
	Type        string `json:"type,omitempty"`
	Status      string `json:"status,omitempty"`
	PublicIP    string `json:"public_ip,omitempty"`
	PrivateIP   string `json:"private_ip,omitempty"`
	OS          string `json:"os,omitempty"`
	GPU         string `json:"gpu,omitempty"`
	Mem         string `json:"mem,omitempty"`
	Vcpu        *int   `json:"vcpu,omitempty"`
	Agent       string `json:"agent,omitempty"`
	CreatedTime string `json:"created_time,omitempty"`
}

// ComputeMachinesOut is the answer to ComputeListMachines.
type ComputeMachinesOut struct {
	Machines []ComputeMachine `json:"machines"`
}

type FlagIn struct {
	Key string `json:"key"` // the flag's key; for a capability's stage it is the capability's name
}

// Unit is the empty value, for an op that takes nothing or answers nothing.
//
// It is an ALIAS of struct{}, and so is cloud.Unit — the same type under two
// names rather than two types. The name exists here because this package
// deliberately imports no cloud (the root imports plane clients, so the edge
// only runs one way), and an op declared with cloud.Unit would close that loop.
type Unit = struct{}

// FlagSwitch is one row of the operator switchboard.
type FlagSwitch struct {
	// Key is the flag's key.
	Key string `json:"key"`
	// Category groups it on the board.
	Category string `json:"category"`
	// Label is the switch's name as an operator reads it.
	Label string `json:"label"`
	// Description says what turning it does.
	Description string `json:"description"`
	// Type is "bool", "int" or "string". The field is spelled as the admin board
	// already spells it on the wire, because this type replaces flags.SwitchView
	// in that response and a rename here would move a live API.
	Type string `json:"type"`
	// Value is the value in force.
	Value string `json:"value"`
	// Source is where that value came from: "flags", "env" or "default".
	Source string `json:"source"`
	// Env names the environment variable supplying a fallback, when one does.
	Env string `json:"env,omitempty"`
	// ReadOnly marks a switch the board shows but cannot write. NOT omitempty: the
	// board it replaces always wrote the field, and dropping a false would change
	// the shape a rendered switch has.
	ReadOnly bool `json:"readOnly"`
	// Principal marks a switch decided per caller (its validated org and user)
	// rather than for the deployment; Value is then whether a stored definition
	// is in force, and Definition says who it is on for.
	Principal bool `json:"principal"`
	// Definition is a principal switch's stored definition, byte-for-byte: its
	// targeting groups on the "org" and "user" properties and its rollout.
	// Absent when nothing is stored, and on every other switch.
	Definition json.RawMessage `json:"definition,omitempty"`
	// Users is who a principal switch's rules name by IAM id, read off
	// Definition: the people a grant let in. Absent on every other switch.
	Users []string `json:"users,omitempty"`
	// Orgs is the orgs a principal switch's rules name, read off Definition.
	Orgs []string `json:"orgs,omitempty"`
}

// FlagBoard is the whole switchboard for the caller's own org.
//
// It carries no org and could not: the tenant is the validated caller, read from
// the peer context, and a board that took one as an argument would be a board of
// any tenant's switches for whoever asked.
type FlagBoard struct {
	// Engine names the evaluator that answered.
	Engine string `json:"engine"`
	// Configured is whether the definition stores opened. False means every value
	// below is a default or an env fallback and a write will be refused.
	Configured bool `json:"configured"`
	// ManageURL is where the definitions behind these switches are read and written.
	ManageURL string `json:"manageUrl"`
	// AuditURL is where the change log for those writes lives.
	AuditURL string `json:"auditUrl"`
	// Switches is every declared switch with the value in force.
	Switches []FlagSwitch `json:"switches"`
}

// FlagSetIn writes one platform switch.
//
// The ACTOR travels and the org does not, and the difference is the point: the
// tenant is who the caller IS, which the plane already carries and no argument may
// restate, while the actor is which human the surface authenticated — a fact the
// store audits and only the caller knows.
type FlagSetIn struct {
	// Key is the flag to write.
	Key string `json:"key"`
	// Definition is the flag-definition document, kept byte-for-byte.
	Definition []byte `json:"definition"`
	// Actor is the human the write is recorded against.
	Actor string `json:"actor"`
}

// FlagGrantIn lets one person into a principal switch, or out of it.
type FlagGrantIn struct {
	// Key is the principal switch, such as "beta.crm".
	Key string `json:"key"`
	// User is the person's IAM id: their token's `sub`.
	User string `json:"user"`
	// On is true to let them in and false to take them out.
	On bool `json:"on"`
	// Actor is the human the grant is recorded against.
	Actor string `json:"actor"`
}

// FlagValueIn asks what an operator has set one flag to.
//
// It carries the caller's OWN default and type, and that is the whole reason this
// op can exist without a catalog. A flag's definition is declared where the flag is
// used — sometimes in a loop over tiers, sometimes per service registered at run
// time — so the app that owns the flag plane cannot be assumed to hold every Def.
// What it does hold is what an OPERATOR set, which is the only thing the caller
// cannot know for itself.
//
// Before this op the caller asked its own process, whose flag registry is a package
// global that only the flags plugin ever fills. Every other binary read its own
// compiled default and reported it as the operator's setting.
type FlagValueIn struct {
	// Key is the flag's key.
	Key string `json:"key"`
	// Kind is how to parse a stored value: "bool", "int" or "string".
	Kind string `json:"kind"`
	// Default is what the caller falls back to when no operator has set this flag.
	// It travels because the fallback belongs to the caller, not to the store.
	Default string `json:"default"`
}

// FlagValue is the resolved value, already parsed by the app that owns the format.
//
// All three fields are always written, and a caller reads the one matching the Kind
// it asked for — so a single op serves every type without the caller re-parsing a
// string the flags engine has already parsed once.
type FlagValue struct {
	// Value is the resolved value as text.
	Value string `json:"value"`
	// N is Value as an integer, 0 when it is not one.
	N int `json:"n"`
	// On is Value as a boolean, false when it is not one.
	On bool `json:"on"`
	// Set reports whether an OPERATOR set this flag. False means Value is the
	// caller's own default, which is a different fact from the value being falsy.
	Set bool `json:"set"`
}

// Flag is the verdict: whether the caller holds it.
//
// One boolean, because the caller asks one question. A flag's variant and payload
// are a richer answer that /v1/flag already gives to a caller that wants one, and
// a refusal that read a variant would have to decide which variants mean yes.
type Flag struct {
	On bool `json:"on"`
}

// ---- entitlement.holds -----------------------------------------------------

// ProductIn names the product to test. The subject is the caller's org.
type ProductIn struct {
	Product string `json:"product"` // the product id; for a module it is the module name
}

// UploadFormIn is one object a browser may post, and what it may be.
type UploadFormIn struct {
	// Bucket is the bucket's friendly name in the caller's org.
	Bucket string `json:"bucket"`
	// Key is the object's key, fixed by the app.
	Key string `json:"key"`
	// ContentType is the one content type the upload may carry.
	ContentType string `json:"contentType"`
	// MaxBytes bounds the upload.
	MaxBytes int64 `json:"maxBytes"`
}

// UploadForm is a signed POST form: post Fields and the file to URL.
type UploadForm struct {
	// URL is where the form posts.
	URL string `json:"url"`
	// Fields go in the form ahead of the file, exactly as given, in order. A list
	// and not a map: a map cannot cross the plane.
	Fields []FormField `json:"fields"`
	// Key is the object the file becomes.
	Key string `json:"key"`
	// Expiry is how long the form is good for, in seconds.
	Expiry int64 `json:"expiry"`
}

// FormField is one field of a signed form.
type FormField struct {
	// Name is the field's name.
	Name string `json:"name"`
	// Value is its value.
	Value string `json:"value"`
}

// Held is whether the caller's org has this product turned on. Distinct from
// [Flag] despite the shape: sharing one type would let a caller ask the wrong
// subsystem and still type-check.
type Held struct {
	On bool `json:"on"`
}

// ---- iam.roles ----

// Roles is the caller's effective role names in their own org.
type Roles struct {
	Roles []string `json:"roles"`
	// Admin is whether the caller administers the org: an owner or admin
	// membership in it, or an admin at home — the grant the platform reads as org
	// admin, and the one that makes IAM name them "System Manager". A role a
	// tenant merely named "System Manager" does not set it.
	Admin bool `json:"admin,omitempty"`
	// Owner is whether the caller owns the org: an owner membership in it. It is
	// narrower than Admin, which an org's admins hold too.
	Owner bool `json:"owner,omitempty"`
	// Member is whether the caller may act in the org at all: IAM has not
	// forbidden or deleted them, and the org is in their membership set — their
	// home org, or one a membership row names them in (store.MemberOrgRefs). It
	// is the set the `orgs` claim carries and the gateway admits X-Org-Id by,
	// asked of a stored subject rather than read off a token.
	Member bool `json:"member,omitempty"`
}

// ProviderRunIn is one connector action to run with a credential its caller
// resolved. The org is the caller's, from the plane context.
type ProviderRunIn struct {
	Provider string `json:"provider" validate:"required"`
	Action   string `json:"action" validate:"required"`
	// Auth is the credential in the shape the provider's auth descriptor
	// declares, as JSON.
	Auth []byte `json:"auth,omitempty"`
	// Props is the action's input properties, as a JSON object.
	Props []byte `json:"props,omitempty"`
}

// ProviderRunOut is how the action went: ok with its output as JSON, or the
// provider's own failure. A failed action is an answer, not a transport error.
type ProviderRunOut struct {
	Ok     bool   `json:"ok"`
	Output []byte `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ---- iam.members / iam.grant ------------------------------------------------

// Scope narrows a membership question inside the caller's org. Every field is a
// filter and all are optional: give a Space for its roster, a User for
// everywhere that person may act, both for one grant. The ORG is the caller's and
// is not a field, so a question can never reach another tenant.
//
// A Space filter that is empty means the org level, not "any space" —
// the org's own grants are a real answer, and conflating them with every
// space's would make a roster read return the whole tenant.
type Scope struct {
	Space   string `json:"space,omitempty"`
	Project string `json:"project,omitempty"`
	User    string `json:"user,omitempty"`
	// Any drops the scope filters, for the caller asking where one person acts.
	Any bool `json:"any,omitempty"`
}

// Membership is one grant: who, where, and as what.
type Membership struct {
	User    string `json:"user"`
	Role    string `json:"role"`
	Name    string `json:"name"`
	Space   string `json:"space,omitempty"`
	Project string `json:"project,omitempty"`
}

// Memberships is every grant in one scope.
type Memberships struct {
	Memberships []Membership `json:"memberships"`
}

// Seats is what an org is billed for: distinct people, and how many are guests.
type Seats struct {
	Seats  int `json:"seats"`
	Guests int `json:"guests"`
}

// GrantIn records one membership. The org is the caller's and is never an
// argument.
type GrantIn struct {
	User    string `json:"user"`
	Space   string `json:"space,omitempty"`
	Project string `json:"project,omitempty"`
	Role    string `json:"role"`
}

// Product names the product whose platform configuration is wanted.
type Product struct {
	Product string `json:"product"`
}

// Configured carries the platform's non-secret configuration document for that
// product, verbatim as stored. Secret fields are excluded: their values live in
// KMS and a deployment knob is not one.
//
// An empty document means nothing has been configured, which every reader must
// treat as "use the default" — the same answer it gets when the settings store
// has no row at all, so an unconfigured product and an unreachable one do not
// need two behaviours.
type Configured struct {
	Config string `json:"config"`
}

// FleetConfig is the platform's non-secret configuration document for one
// product, to be stored verbatim in place of whatever is there. Config is a JSON
// object; secret fields never travel here — their values live in KMS.
type FleetConfig struct {
	Product string `json:"product"`
	Config  string `json:"config"`
}

// OrgSettingsSet is the caller organization's non-secret configuration document
// and KMS secret-key index for one product. Secret values never travel here —
// their values live exclusively in KMS under orgs/{org}/settings/{product}/{key}.
type OrgSettingsSet struct {
	// Product is the settings catalog slug (for example "vault").
	Product string `json:"product"`
	// Config is the non-secret JSON configuration object serialized as a string.
	Config string `json:"config"`
	// SecretKeys lists KMS secret key names already sealed under
	// orgs/{org}/settings/{product}/{key} to merge into the stored key index.
	SecretKeys []string `json:"secretKeys,omitempty"`
}

// PrefsIn asks for the caller's own preferences. It carries nothing: the person
// is the one on the call.
type PrefsIn struct{}

// Prefs is one person's preference document, verbatim as stored: a JSON object
// whose keys the surfaces own. `{}` when they have saved nothing.
type Prefs struct {
	Doc string `json:"doc"`
}

// Codebase names one codebase in the caller's org, for a run about to start on
// it. The org is the caller's own and is not a field.
type Codebase struct {
	// Repo is the repository's name in the caller's org.
	Repo string `json:"repo"`
	// Setup is a run whose work is to find the environment, so it carries the
	// secrets and none of the scripts.
	Setup bool `json:"setup,omitempty"`
}

// Prepared is what a run carries of its codebase's environment. A codebase with
// none answers it empty.
type Prepared struct {
	// Install runs in the fresh checkout before the agent starts.
	Install string `json:"install,omitempty"`
	// Start is left running in the background after the install.
	Start string `json:"start,omitempty"`
	// Secrets are the names the run exports, each with the KMS ref its value is
	// read from.
	Secrets []Carried `json:"secrets,omitempty"`
}

// Carried is one secret a codebase carries: the name the run exports it under,
// and the tenant ref KMS opens only to a call acting for that tenant.
type Carried struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// Suggestion is a setup run's answer, as the run read it out of its agent's
// final message. The environment app decides what of it holds.
type Suggestion struct {
	// Repo is the repository's name in the caller's org.
	Repo string `json:"repo"`
	// Session is the setup run that answered.
	Session string `json:"session"`
	Install string `json:"install"`
	Start   string `json:"start"`
	// Secrets are the environment variables the agent found the codebase reads.
	Secrets []string `json:"secrets"`
	// Note is what the agent found and checked, in its own words.
	Note string `json:"note"`
}

// Sent names the provider that carried the message, for the caller's audit trail.
// A delivery that FAILED is an error from the call, never a Sent with an empty
// provider — that would read as success.
type Sent struct {
	Provider string `json:"provider"`
}

// ---- iam.approval ----------------------------------------------------------

// Email is the address the identity store holds for the caller, and whether
// they have proved it.
//
// Verified is the whole point. An address a person merely TYPED is not evidence
// of anything: a self-serve signup can name any address, so a consumer keying
// an identity on one is keying it on a claim. Only a confirmed address says the
// person is reachable there — and therefore is who that address belongs to.
type Email struct {
	// Address is what the store holds, which is not necessarily what a token's
	// `email` claim says — a caller that changed it since the token was minted
	// would present the old one. Anything resolving an identity uses this.
	Address string `json:"address,omitempty"`
	// Verified is whether the person proved the address. False is a real answer
	// and callers must refuse on it, never treat it as "probably fine".
	Verified bool `json:"verified"`
	// Name is the display name the person gave. It names them — on a commit made
	// for them, beside a verified address — and identifies nobody.
	Name string `json:"name,omitempty"`
}

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
// The identity record's other columns — space, tags, metadata, the default
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

// MemberIn names the space and the person a membership question is about.
// The ORG is not here and cannot be: it is the tenancy key of every space
// row, so a caller able to pass it could read another tenant's roster.
type MemberIn struct {
	// Space is the space uuid, scoped to the caller's org on the read.
	Space string `json:"space"`
	// Subject is the IAM subject, NOT a team account id. team owns the join from
	// one to the other — it is the join that created the rows — so a peer that
	// computed its own would be a second derivation of the same address, which is
	// how two layers end up naming different accounts for one person.
	Subject string `json:"subject"`
}

// TokenIn is the credential a caller was handed, exactly as presented. It is
// the caller's own bearer travelling a socket only the host's uid can open; the
// key that verifies it is not here and never is.
type TokenIn struct {
	Bearer string `json:"bearer"`
}

// Token is the verdict on a presented team token and nothing more. Verified is
// false for every token the deployment will not stand behind: a bad signature, an
// expired or not-yet-valid one, a malformed one, and any token at all when no
// signing key is configured. The rest is empty exactly when Verified is false.
type Token struct {
	// Verified reports that the signature and temporal claims hold.
	Verified bool `json:"verified"`
	// Org is the signed tenant claim.
	Org string `json:"org"`
	// Account is the signed team AccountUuid.
	Account string `json:"account"`
	// Privileged reports that the signed role confers full capability on the
	// space's data (owner, admin, member); an absent or guest role does not.
	Privileged bool `json:"privileged"`
}

// BotsIn asks for the caller's own org, and carries nothing. The org is the
// tenancy key of every roster row and rides the CALLER, never an argument — a
// field here would let a peer name another tenant's bots.
type BotsIn struct{}

// BotMember is one bot as the space roster addresses it.
type BotMember struct {
	// ID is the agent id.
	ID string `json:"id"`
	// Name is the display name.
	Name string `json:"name"`
	// UserID is the derived member account uuid (personUuid).
	UserID string `json:"userId"`
	// PersonRef is the projected Person _id.
	PersonRef string `json:"personRef"`
	// Active is whether the agent projects as a LIVE space member, derived from
	// its registry status: empty, "active" and "ready" are live, anything else
	// (archived/retired) is not. An inactive bot drops out of the roster while its
	// past authorship survives.
	Active bool `json:"active"`
}

// BotRoster is every bot of the caller's org, projected as a space member.
type BotRoster struct {
	// Bots is one entry per bot, each carrying the member account uuid and the
	// Person reference the space roster addresses it by. Empty means the org has
	// no bots — not that the roster could not be read, which is an error.
	Bots []BotMember `json:"bots"`
}

// BotSync acknowledges a roster re-projection.
type BotSync struct {
	// Synced is true when the reconcile ran.
	Synced bool `json:"synced"`
	// Projected is how many roster entries the reconcile touched.
	Projected int `json:"projected"`
}

// Member is what the rows say. Role and Account are empty exactly when Member is
// false, so a caller cannot mistake "no row" for a role or an identity.
type Member struct {
	// Member reports whether the subject holds a row in that space.
	Member bool `json:"member"`
	// Role is the space role on that row (owner | admin | member | guest).
	Role string `json:"role"`
	// Account is the team AccountUuid the subject resolved to — the identity the
	// asking process attributes the person by, so it never derives one itself.
	Account string `json:"account"`
}

// FederatedIn names an external identity: the provider ("github") and the
// subject that provider issued (GitHub's numeric user id). The org is the
// caller's, from the plane context.
type FederatedIn struct {
	Provider string `json:"provider" validate:"required"`
	Subject  string `json:"subject" validate:"required"`
}

// Federated is the member it resolved to: the IAM user id, or "" for none.
type Federated struct {
	User string `json:"user"`
}

// LinkedIn names the provider whose subject the caller wants for themselves.
type LinkedIn struct {
	Provider string `json:"provider" validate:"required"`
}

// Linked is the caller's subject at that provider, or "" when they never signed
// in with it.
type Linked struct {
	Subject string `json:"subject"`
}

// FwRef names one DocType by its address ("kb.page") in the caller's org.
type FwRef struct {
	Doctype string `json:"doctype" validate:"required"`
}

// FwInstalled answers whether the org holds that DocType.
type FwInstalled struct {
	Installed bool `json:"installed"`
}

// FwFilter is one field-equality clause; the encoder carries no maps, so
// filters cross as pairs.
type FwFilter struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// FwDocsIn lists documents of one DocType, bounded by Limit (the server bounds
// it again). Filters are field equalities; Among is set membership, where a
// field named more than once must hold one of the values named for it.
type FwDocsIn struct {
	Doctype string     `json:"doctype" validate:"required"`
	Filters []FwFilter `json:"filters,omitempty"`
	Among   []FwFilter `json:"among,omitempty"`
	// Names asks which documents match and nothing more, so each answer carries
	// its name and no field data.
	Names bool `json:"names,omitempty"`
	Limit int  `json:"limit,omitempty"`
	// Cursor names the page after a previous page's `next`; empty is the first page.
	Cursor string `json:"cursor,omitempty"`
}

// FwDoc is a document as it crosses the plane: its name, and its field data as
// the JSON object it is — the DocType defines the fields at run time, so raw
// bytes are the honest carrier, exactly as IndexReconcileIn relays its corpus.
type FwDoc struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
	// Created is when the document was made, in unix seconds; 0 when unknown.
	Created int64 `json:"created,omitempty"`
}

// FwDocs is a page of documents.
type FwDocs struct {
	Docs []FwDoc `json:"docs"`
	// Next is the opaque keyset cursor for the next page; empty after the last.
	Next string `json:"next,omitempty"`
	// Total is the true number of matching documents, across every page.
	Total int64 `json:"total"`
}

// FwDocIn names one document.
type FwDocIn struct {
	Doctype string `json:"doctype" validate:"required"`
	Name    string `json:"name" validate:"required"`
}

// FwIngestIn creates a document from already-trusted field data, through the
// full validate + hook pipeline; Name requests a name, "" lets the DocType's
// naming rule decide. Data is the field object as JSON.
type FwIngestIn struct {
	Doctype string          `json:"doctype" validate:"required"`
	Data    json.RawMessage `json:"data" validate:"required"`
	Name    string          `json:"name,omitempty"`
}

// FwIngested is the created document's name.
type FwIngested struct {
	Name string `json:"name"`
}

// FwFindIn asks for the first document whose field equals value.
type FwFindIn struct {
	Doctype string `json:"doctype" validate:"required"`
	Field   string `json:"field" validate:"required"`
	Value   string `json:"value"`
}

// FwFound carries the found name, "" for none.
type FwFound struct {
	Name string `json:"name"`
}

// FwModuleIn names one content module ("cms", "patrol") in the caller's org.
type FwModuleIn struct {
	Module string `json:"module" validate:"required"`
}

// FwUpdateIn replaces one document's field data, through the same validate +
// hook pipeline an HTTP PUT runs. Data is the field object as JSON.
type FwUpdateIn struct {
	Doctype string          `json:"doctype" validate:"required"`
	Name    string          `json:"name" validate:"required"`
	Data    json.RawMessage `json:"data" validate:"required"`
}

// FwLeaseIn asks for the exclusive lease on one key in the caller's org. Hold is
// how long the lease lives before it lapses and Wait how long to keep trying
// while a live holder has it, both in milliseconds.
type FwLeaseIn struct {
	Key  string `json:"key" validate:"required"`
	Hold int64  `json:"hold"`
	Wait int64  `json:"wait"`
}

// FwLease says whether the lease was taken, and under which holder token. The
// token is what a release is scoped to, so the caller that took the lease is the
// only one that can free it.
type FwLease struct {
	Held   bool   `json:"held"`
	Holder string `json:"holder"`
}

// FwReleaseIn frees the lease on one key, scoped to the holder that took it.
type FwReleaseIn struct {
	Key    string `json:"key" validate:"required"`
	Holder string `json:"holder" validate:"required"`
}

// SkillFile is one SKILL.md as a repository holds it: the repo-relative Path,
// ".agents/skills/<name>/SKILL.md", whose directory is the skill's name, and
// the file's Content.
type SkillFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SkillsIn names the repository ("<project>/<name>", or "<name>" for an
// org-level repository) and carries every skill file its default branch holds.
// Empty Files means the repository holds none now, and removes what it held.
type SkillsIn struct {
	Source string      `json:"source" validate:"required"`
	Files  []SkillFile `json:"files"`
}

// Skills is what the repository contributes after the call, and the paths of
// files it could not take as skills — a name that is not one path segment, an
// empty body, or one over the size a skill may be.
type Skills struct {
	Count   int      `json:"count"`
	Refused []string `json:"refused,omitempty"`
}

// SkillCatalogIn asks for the brand's catalogue. It carries nothing: the brand
// is the serving deployment's, and the catalogue is the same for every org.
type SkillCatalogIn struct{}

// SkillEntry is one skill of the brand's catalogue: the id it is served under at
// /.well-known/agent-skills/<name>/SKILL.md, and its one-line description.
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SkillCatalog is the deployment brand's whole catalogue of agent skills.
type SkillCatalog struct {
	Brand  string       `json:"brand"`
	Skills []SkillEntry `json:"skills"`
}

// SkillDocsIn names skills, as their tool name (skill_<name>) or their bare name.
type SkillDocsIn struct {
	Names []string `json:"names"`
}

// SkillDoc is one skill's SKILL.md under its bare name.
type SkillDoc struct {
	// Name is the skill's bare name, which is also the directory its SKILL.md
	// is written into.
	Name string `json:"name"`
	// Content is the skill's SKILL.md, verbatim.
	Content string `json:"content"`
}

// SkillDocs is every named skill that could be answered, in the order asked; a
// name that is absent was not found, or not enabled.
type SkillDocs struct {
	Skills []SkillDoc `json:"skills"`
}

// KitIn asks for the org's kit. It carries nothing: the org is the caller's.
type KitIn struct{}

// Kit is what an agent run carries of its org's tool plane: only what an admin
// of the org put in place, since every colleague's run loads it.
type Kit struct {
	// Skills are the documents of the skills an admin activated, by name.
	Skills []SkillDoc `json:"skills"`
	// Omitted are activated skills left out because a run carries only so many.
	Omitted []string `json:"omitted,omitempty"`
	// Servers are the MCP servers an admin registered and activated tools of, at
	// most KitServers of them.
	Servers []Remote `json:"servers"`
	// Excess are servers left out, by id, because a run carries only so many.
	Excess []string `json:"excess,omitempty"`
	// Muted are the names the run's person took out of their own runs — a
	// skill, a server's tool, or a server by its id. None of them is above.
	Muted []string `json:"muted,omitempty"`
}

// KitServers is the most MCP servers one agent run carries. Every one is a
// connection its harness opens and a tool list it reads into the model's context
// before the task starts, so an org that registered a hundred does not start a
// hundred with every run. One number for the tool app that bounds the kit and the
// run that carries it, as AttachEvery is one number for two processes.
const KitServers = 16

// Remote is one MCP server an org registered, as an agent run carries it.
type Remote struct {
	// Name is the server's id in the org, which also prefixes its tools on the
	// tool plane, and names it to ToolsRelay.
	Name string `json:"name"`
	// Tools are the tools of it an admin activated, each by the server's own
	// name for it: all a run's harness may call of it.
	Tools []string `json:"tools"`
}

// RelayIn is one MCP request an agent run makes of one of its org's servers.
// The org is the caller's and is not a field.
type RelayIn struct {
	// Server is the server's id in the org, as the run's kit named it.
	Server string `json:"server"`
	// Method is tools/list or tools/call.
	Method string `json:"method"`
	// Params are the request's params, verbatim.
	Params json.RawMessage `json:"params,omitempty"`
	// Session is the coding run session id for per-run audit attribution.
	Session string `json:"session,omitempty"`
}

// Relayed is the server's answer to a relayed request: the JSON-RPC result,
// verbatim for a call and narrowed to the tools a run may call for a list.
type Relayed struct {
	Result json.RawMessage `json:"result"`
}

// SpacesIn names the person a space list is about. The ORG is absent for
// the same reason it is absent from MemberIn: it is the tenancy key of every
// space row, so a caller able to pass it could enumerate another tenant's.
type SpacesIn struct {
	// Subject is the IAM subject, NOT a team account id — team owns the join from
	// one to the other, exactly as in MemberIn.
	Subject string `json:"subject"`
}

// Space is one space a person holds a member row in.
//
// The ROLE is on it because the asking process decides with it: meet admits a
// privileged member and refuses a guest, and it applies that rule to the list it
// offers as well as to the join it grants, so a person is never shown a room
// they would then be refused.
type Space struct {
	// UUID is the space's stable id — and, in meet, the leading segment of
	// every room name bound to it.
	UUID string `json:"uuid"`
	// Name is the human label for a picker.
	Name string `json:"name"`
	// Role is the role on the caller's member row (owner | admin | member | guest).
	Role string `json:"role"`
}

// Spaces is what the rows say about one person: the account they resolved to and
// the spaces they are in.
//
// The invariant is ONE-WAY: a non-empty Items implies a non-empty Account, so
// every space offered has an identity to seat the person under. The converse
// does NOT hold and must not be assumed — a subject that resolves to an account
// while holding no current membership row answers with the account and an empty
// list, which is the honest "we know who you are, and you are in nothing".
//
// (This doc used to claim the biconditional — "Account is empty exactly when
// there are no spaces" — which the implementation never satisfied, because it
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
	// Items is every space the person is in, newest membership first. Empty is
	// a real answer, not an error.
	Items []Space `json:"items"`
}

// ---- git -------------------------------------------------------------------

// ImportIn asks git to create a repo and mirror an upstream into it. It exists
// because the app that decides to import (integrations, holding the provider
// credential) and the app that owns the git store are DIFFERENT PROCESSES, so
// the in-process importer client is nil across that boundary — the request has to
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

// LandedIn says a push landed on a repository's host: the host's address for
// it, the full ref, and the tips around the push. The org is the caller's.
type LandedIn struct {
	// CloneURL is the host's clone address for the repository.
	CloneURL string `json:"cloneUrl" validate:"required"`
	// Ref is the FULL ref the push moved, e.g. refs/heads/main.
	Ref string `json:"ref" validate:"required"`
	// Before is the ref's tip before the push.
	Before string `json:"before,omitempty"`
	// After is the ref's tip after the push.
	After string `json:"after" validate:"required"`
}

// Landed names the caller's repositories linked to that host address. None is
// an answer, not a failure: most pushes land on repositories no one linked.
type Landed struct {
	Repos []string `json:"repos"`
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

// Visibility is what one project's repository must BE, as the ONE derived
// answer — public, moderated and deleted resolved together — computed by the
// owner of that rule and never re-derived from parts here. Name and Description
// seed a repo the first time it is created and are never re-imposed, so an
// author who edits their own description keeps it.
//
// State has THREE values because the project row has three, and the third is
// load-bearing: a repository is created by NAME, so one left behind by a deleted
// project is one the next project of that name inherits — commits and all — and
// then publishes. A two-valued fact could say "closed" and could not say "gone",
// which left the deletion nowhere to go.
type Visibility struct {
	Slug        string `json:"slug" validate:"required"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	State       string `json:"state" validate:"required"`
}

// The three states a project's repository is asked to be in. They are the whole
// vocabulary of the visibility client, on both sides of it.
const (
	// Open: readable by anyone, including a reader with no credential.
	Open = "open"
	// Shut: it exists and is readable only by someone on it.
	Shut = "shut"
	// Gone: not there at all, with its contents. Deletion rather than closure,
	// because a closed repository is still there to be adopted and re-opened.
	Gone = "gone"
)

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

// Fired is one alert the platform's ruler holds active.
type Fired struct {
	// Name is the rule's alert name, e.g. HanzoServiceDown.
	Name string `json:"name"`
	// Severity is the rule's severity label: critical, warning.
	Severity string `json:"severity,omitempty"`
	// Summary is the summary annotation as the ruler rendered it.
	Summary string `json:"summary,omitempty"`
	// Labels name what fired (service, table, node), the ruler's bookkeeping
	// labels left out.
	Labels []Field `json:"labels,omitempty"`
	// Since is when the alert started firing, RFC 3339.
	Since string `json:"since"`
}

// Firing is the platform's active alerts, the longest-firing first.
type Firing struct {
	Alerts []Fired `json:"alerts"`
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
	// Users bounds rows by the user each was written with; absent reaches every
	// row. A caller asking on a person's behalf passes {"", theirs}: the rows
	// written for everyone, and that person's own.
	Users []string `json:"users,omitempty"`
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

// ---- ai.chat — one completion, bought for the caller ------------------------

// ChatIn is one request body as the model API reads it: a chat completion for
// AIChat, an embeddings request for AIEmbed, a rerank for AIRerank, a multipart
// recording for AITranscribe. It crosses verbatim because the wire belongs to the
// model API; a struct here would be a second copy of it. There is no org and no
// person in it: both ride the caller. ContentType is the body's own, "" for JSON.
type ChatIn struct {
	Request     []byte `json:"request" validate:"required"`
	ContentType string `json:"contentType,omitempty"`
}

// Chat is the model API's answer, relayed verbatim so a 402 stays a 402 and the
// caller reads a refusal exactly as it would over HTTP.
//
// Billed is what the call cost, in nano-USD, as the model API priced it: the amount
// the same answer debits any other caller (hanzoai/ai object.Bill). The caller's
// meter charges exactly this. Nil when nothing priced the call — a refusal, or an
// answer that filed no usage — which is a different fact from a call priced at
// nothing, and the pointer is what keeps the two apart on the wire.
type Chat struct {
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"`
	Billed *int64 `json:"billed,omitempty"`
}

// MediaIn is one media call as the model API reads it: the method and path of a
// media route (an image, speech or music, a video or 3D job and its reads) and the
// JSON body, crossing verbatim as ChatIn's does. No org and no person in it: both
// ride the caller.
type MediaIn struct {
	Method  string `json:"method" validate:"required"`
	Path    string `json:"path" validate:"required"`
	Request []byte `json:"request,omitempty"`
}

// IndexPutIn is one document written into one index, created on first use. The
// ORG is the caller's, as on every index op.
type IndexPutIn struct {
	// UID is the index within the org.
	UID string `json:"uid" validate:"required"`
	// PrimaryKey names the field the document is keyed by.
	PrimaryKey string `json:"primaryKey" validate:"required"`
	// Doc is the document, relayed verbatim as IndexReconcileIn.Docs is.
	Doc json.RawMessage `json:"doc" validate:"required"`
}

// IndexPutAllIn is a batch of documents written into one index in one
// transaction, the index created on first use. The ORG is the caller's.
type IndexPutAllIn struct {
	// UID is the index within the org.
	UID string `json:"uid" validate:"required"`
	// PrimaryKey names the field each document is keyed by.
	PrimaryKey string `json:"primaryKey" validate:"required"`
	// Docs are the documents, relayed verbatim as IndexReconcileIn.Docs is.
	Docs []json.RawMessage `json:"docs,omitempty"`
}

// IndexRemoveIn names documents to remove from one index by primary key. A key
// that is not there is not an error.
type IndexRemoveIn struct {
	// UID is the index within the org.
	UID string `json:"uid" validate:"required"`
	// Keys are the primary keys to remove.
	Keys []string `json:"keys"`
}

// ---- knowledge.index — a knowledge document's passages, from another process -

// KnowledgeIndexIn names one knowledge document whose retrieval legs are to
// match the framework store. The ORG is the caller's. It carries no fields: the
// op reads the document, so the text indexed is the text the store holds when
// the op runs, never an older save's.
type KnowledgeIndexIn struct {
	// DocType is the document's address, module.name: kb.page, kb.memory or
	// kb.source.
	DocType string `json:"doctype" validate:"required"`
	// Name is the document's name in the framework store.
	Name string `json:"name" validate:"required"`
	// Gone is a document being deleted: its passages and row go whatever the
	// store still holds, since the trash hook runs before the delete.
	Gone bool `json:"gone,omitempty"`
}

// ---- knowledge.files — the workspace files' passages, from another process ---

// FileQuery is one search of the caller org's files. The ORG is the caller's.
type FileQuery struct {
	// Query is the question or phrase.
	Query string `json:"query" validate:"required"`
	// Limit bounds the passages answered.
	Limit int `json:"limit,omitempty"`
}

// FileHit is one passage of one file.
type FileHit struct {
	// File is the file's id.
	File string `json:"file"`
	// Name is the file's name.
	Name string `json:"name"`
	// Path is the passage's section path in its document.
	Path string `json:"path"`
	// Cite is file › section › paragraph.
	Cite string `json:"cite"`
	// Text is the passage.
	Text string `json:"text"`
	// Score is the passage's fused rank score within this answer.
	Score float64 `json:"score"`
}

// FileHits is the passages that match, best first.
type FileHits struct {
	// Hits are the passages, best first.
	Hits []FileHit `json:"hits"`
	// Degraded is true when a leg failed and Hits are what the other found.
	Degraded bool `json:"degraded,omitempty"`
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

// EnqueueIn puts one render job on the caller's org's GPU lane. Idempotent on ID:
// enqueueing an ID that already exists answers that job unchanged, so a caller that
// retries after a lost reply never runs the work twice and never resets a claim.
type EnqueueIn struct {
	// Namespace is the lane, and only gpu-jobs is accepted.
	Namespace string `json:"namespace" validate:"required"`
	ID        string `json:"id" validate:"required"`
	// Type is the job type a worker dispatches on, and only studio.render is accepted.
	Type string `json:"type" validate:"required"`
	// Queue is the task queue within the lane a worker claims from: gpu-jobs, or
	// gpu:<identity> for one machine.
	Queue string `json:"queue" validate:"required"`
	// Input is the job's JSON input, at most 1 MiB, handed to the claimer verbatim.
	Input []byte `json:"input"`
	// Attempts is how many times the engine hands the job out, at least 1.
	Attempts int `json:"attempts"`
	// Heartbeat, Start and Close are durations in Go syntax ("1h0m0s"), each
	// positive and at most 30 days: the lease a claimer keeps by heartbeating, how
	// long the job may wait to be claimed, and how long one claim may run.
	Heartbeat string `json:"heartbeat"`
	Start     string `json:"start"`
	Close     string `json:"close"`
}

// ActivityIn names one job on the caller's org's GPU lane (gpu-jobs).
type ActivityIn struct {
	Namespace string `json:"namespace" validate:"required"`
	ID        string `json:"id" validate:"required"`
}

// Activity is one job as the engine holds it, without its input. Row is the
// engine's own JSON, relayed for the reason Activities.Rows is.
type Activity struct {
	Row []byte `json:"row"`
}

// CancelIn cancels one job on the caller's org's GPU lane (gpu-jobs). Reason is
// recorded on the job's history.
type CancelIn struct {
	Namespace string `json:"namespace" validate:"required"`
	ID        string `json:"id" validate:"required"`
	Reason    string `json:"reason,omitempty"`
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
	// Category is what a settlement of this resource pays for (the categories in
	// economic.go): a tool call is a service, a job is what its listing or offer
	// said. The rail records it on the settlement and states it on the economic
	// event, so it is the price table's to say, like the amount and the payee.
	Category string `json:"category,omitempty"`
	// PayTo is the payee address a payment agreed before it is made was signed
	// to: a job's, pinned when the rail checked it at funding. The wallet's keys
	// may be rotated since, and the payment still names the address it had. Empty
	// for a resource paid when it is priced, which pays the wallet's address now.
	PayTo string `json:"payTo,omitempty"`
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

// ConnectionIn names the provider being asked about. The ORG is the CALLER's,
// read from the plane context and never an argument — the same rule SlackSendIn
// follows, and for the same reason: an org a caller could name is an org a caller
// could read another tenant's connections from.
type ConnectionIn struct {
	Provider string `json:"provider" validate:"required"`
	// Account, when set, asks about that one account of the provider (a GitHub
	// login) instead of any: connected only if the org itself holds that account.
	Account string `json:"account,omitempty"`
}

// PieceRunIn names one connector action and its input. The ORG is the
// caller's, read from the plane context, never a field.
type PieceRunIn struct {
	Provider string `json:"provider" validate:"required"`
	Action   string `json:"action" validate:"required"`
	// Input is the action's props as a JSON object.
	Input []byte `json:"input,omitempty"`
}

// SocialPostIn is one post to one of the caller's org's social accounts.
type SocialPostIn struct {
	// To is the account's network: x, linkedin, facebook, instagram, threads,
	// tiktok, youtube, pinterest, reddit, bluesky, mastodon, telegram-bot or
	// discord-webhook.
	To string `json:"to" validate:"required"`
	// Text is the post, as it is published.
	Text string `json:"text" validate:"required"`
	// Media are the public addresses of the post's photos or its video, in
	// order. The network fetches them, or this process does and sends the file.
	Media []string `json:"media,omitempty"`
	// Account is the platform id of the connected target. Empty posts the org's
	// only connection of that network. Several connections and no account is a
	// refusal: a post does not guess the first row.
	Account string `json:"account,omitempty"`
}

// SocialTargetIn is one account a consent produced, recorded where the org
// manages its publish targets. The token stays with the provider.
type SocialTargetIn struct {
	// Provider is the network: x, facebook, linkedin, and the rest of the set.
	Provider string `json:"provider"`
	// ExternalID is the platform id the token is sealed under.
	ExternalID string `json:"externalId"`
	// Handle is the name a person recognizes.
	Handle string `json:"handle"`
}

// SocialTargetOut is the publish-target id the account was stored as.
type SocialTargetOut struct {
	// ID is the social account id.
	ID string `json:"id"`
}

// SocialPostOut is where a post went, or why it did not: an answer either way,
// never a transport error, so the run that asked keeps the reason.
type SocialPostOut struct {
	// URL opens the post; empty when it did not go.
	URL string `json:"url,omitempty"`
	// Error is the platform's reason it did not go, in words a person acts on.
	Error string `json:"error,omitempty"`
}

// GoogleSendIn is one email sent from the caller's own Gmail, to one recipient.
// The ORG and the PERSON are the caller's, read from the plane context and never
// fields: a person a caller could name is a mailbox a caller could send from.
type GoogleSendIn struct {
	// To is the one recipient, an address ("ada@example.com" or "Ada <ada@example.com>").
	To string `json:"to" validate:"required"`
	// Subject is the subject line, at most 200 characters, on one line.
	Subject string `json:"subject" validate:"required"`
	// Text is the plain-text body, at most 64 KiB.
	Text string `json:"text" validate:"required"`
	// HTML is the HTML body, sent beside Text as its alternative. Optional; at most 256 KiB.
	HTML string `json:"html,omitempty"`
	// Unsubscribe is the https or mailto address the message's List-Unsubscribe
	// header names; an https one is also offered as one-click. Optional.
	Unsubscribe string `json:"unsubscribe,omitempty"`
}

// GoogleSendOut is whether the message went, or why it did not: an answer either
// way, never a transport error, so the caller keeps the reason. Reason is one of
// not_connected (the person has not connected Google, or Google dropped the
// grant), not_allowed (the grant does not include sending), limited (Gmail's
// sending limit), rejected (Gmail refused the message or the recipient) and
// unavailable (Google did not answer; try again later).
type GoogleSendOut struct {
	// ID is Gmail's id for the sent message; empty when it did not go.
	ID string `json:"id,omitempty"`
	// From is the address it was sent from: the person's own.
	From string `json:"from,omitempty"`
	// Reason is why it did not go, as a code; empty when it went.
	Reason string `json:"reason,omitempty"`
	// Error is the reason in words a person acts on; empty when it went.
	Error string `json:"error,omitempty"`
}

// PieceRunOut is what the action answered: its output as JSON, or why it
// failed. A failed action is an answer, not a transport error, so a caller
// records the reason.
type PieceRunOut struct {
	Output []byte `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Connection is what an app needs to know about a connection, and nothing more.
// No token, no secret, no credential of any kind: an app that must ACT on a
// connection asks the owner to act (IntegrationsSlackSend), rather than fetching
// the material and acting itself.
type Connection struct {
	// Connected is the whole answer to "may I offer this". False leaves the rest
	// empty.
	Connected bool `json:"connected"`
	// Account is the id-shaped fact — the provider-side external id (a Slack team
	// id, a Discord guild id). AccountLabel is the human name. Never swapped.
	Account      string   `json:"account,omitempty"`
	AccountLabel string   `json:"accountLabel,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// GitHubGrantIn names the one repository a token may reach. The ORG is the
// CALLER's, never a field: it decides which connected accounts the token can be
// minted on at all.
type GitHubGrantIn struct {
	Owner string `json:"owner" validate:"required"`
	Repo  string `json:"repo" validate:"required"`
	// Read asks for a token that can only read the repository's contents — a
	// plan run's — rather than write it and open a pull request.
	Read bool `json:"read,omitempty"`
}

// GitHubGrant is the token and the repository it reaches. The token is
// write-only — never logged, never in an event — and the caller revokes it when
// its run ends; GitHub expires it within the hour if nothing does.
//
// Owner and Repo are where the repository lives NOW, which a transfer makes
// different from what was asked. The caller works there: GitHub redirects the
// old name, but a redirected POST arrives as a GET.
type GitHubGrant struct {
	Token string `json:"token"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// GitHubAccountIn names the GitHub account whose granted repositories are asked
// for. The org rides the caller: it decides which installations exist to ask.
type GitHubAccountIn struct {
	Account string `json:"account" validate:"required"`
}

// GitHubAccount is every repository the account's installation grants.
type GitHubAccount struct {
	Repos []GitHubAccountRepo `json:"repos"`
}

// GitHubAccountRepo is one granted repository: its name and the https address
// it is cloned from.
type GitHubAccountRepo struct {
	Name     string `json:"name"`
	CloneURL string `json:"cloneUrl"`
}

// GitHubRunnersIn is the cluster's side of a reconcile of GitHub's queue: how
// many runners may still start, and every runner Job platform holds for GitHub.
type GitHubRunnersIn struct {
	Free int         `json:"free"`
	Jobs []RunnerJob `json:"jobs"`
}

// RunnerJob is one runner Job: its name, which is its runner's registration
// name too, whether it finished, and when it was created, in Unix seconds.
type RunnerJob struct {
	Name    string `json:"name"`
	Done    bool   `json:"done,omitempty"`
	Created int64  `json:"created"`
}

// GitHubRunnersOut is what a reconcile did: how many runners it started, and
// the finished Jobs holding the name a queued job's runner needs, which platform
// deletes so the next reconcile can start it.
type GitHubRunnersOut struct {
	Started int      `json:"started"`
	Reclaim []string `json:"reclaim"`
}

// GitHubRunIn names the GitHub repository a coding run asks to work on. The org,
// the person and whether they administer the org all ride the CALLER; none is a
// field, because each decides what may be minted.
type GitHubRunIn struct {
	Owner string `json:"owner" validate:"required"`
	Repo  string `json:"repo" validate:"required"`
	// Read asks for a token that can only read the repository's contents — a
	// plan run's. Admission is the same either way: a plan is for someone who
	// could build.
	Read bool `json:"read,omitempty"`
}

// GitHubRepoIn names a GitHub repository to look up in the caller's own view.
type GitHubRepoIn struct {
	Owner string `json:"owner" validate:"required"`
	Repo  string `json:"repo" validate:"required"`
}

// GitHubRepo is a repository the caller may see, as GitHub names it now.
type GitHubRepo struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	// Base is the repository's default branch.
	Base    string `json:"base"`
	Private bool   `json:"private"`
	// Installation is the Hanzo Platform App installation that reaches it.
	Installation int64 `json:"installation"`
	// Personal is whether the caller's own GitHub answered, rather than the
	// org's installations on an admin's behalf.
	Personal bool `json:"personal"`
}

// GitHubRefIn names one ref of a GitHub repository: a branch or tag name, a
// full refs/heads or refs/tags ref, or a full commit sha.
type GitHubRefIn struct {
	Owner string `json:"owner" validate:"required"`
	Repo  string `json:"repo" validate:"required"`
	Ref   string `json:"ref" validate:"required"`
}

// GitHubRef is whether a ref is one the repository publishes from, and why.
type GitHubRef struct {
	// Trusted is true for the default branch, a protected branch, a tag, or a
	// full commit one of those branches contains.
	Trusted bool `json:"trusted"`
	// Kind names which of those it is: "default", "protected", "tag" or
	// "commit". Empty when it is none of them.
	Kind string `json:"kind,omitempty"`
}

// GitHubIssueIn is one write to one issue. Number 0 opens a new issue from Title
// and Body; otherwise every field left empty is left alone. The org is the
// caller's, from the plane context.
type GitHubIssueIn struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	// State is open or closed.
	State string `json:"state,omitempty"`
	// Assignee hands the issue to one GitHub login; "me" is the caller, by the
	// GitHub account IAM linked to them. Nil leaves the holder alone and "" takes
	// the issue off whoever holds it.
	Assignee *string `json:"assignee,omitempty"`
	// Labels are added to the issue's own; none is removed.
	Labels []string `json:"labels,omitempty"`
	// Comment is posted on the issue after every other change.
	Comment string `json:"comment,omitempty"`
	// Channel binds the issue to the conversation the write was asked for in
	// (Reply.Address), when it is bound to none yet.
	Channel string `json:"channel,omitempty"`
}

// GitHubIssue is the issue as GitHub holds it after the write.
type GitHubIssue struct {
	Owner    string   `json:"owner"`
	Repo     string   `json:"repo"`
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	State    string   `json:"state"`
	URL      string   `json:"url"`
	Assignee string   `json:"assignee,omitempty"`
	Labels   []string `json:"labels"`
	// Channel is the conversation the issue is bound to, "" for none.
	Channel string `json:"channel,omitempty"`
}

// GitHubRun is a run's admission to a GitHub repository: the token that reaches
// it, where it lives now, its default branch, and whose access admitted it.
//
// Token is write-only — never logged, never in an event — and the caller revokes
// it when its run ends; GitHub expires it within the hour if nothing does.
type GitHubRun struct {
	Token string `json:"token"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	// Base is the repository's default branch.
	Base string `json:"base"`
	// Login is the GitHub account whose own access admitted the run. Empty when
	// an org admin was admitted on an installation the org holds.
	Login string `json:"login"`
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
	// Controls are the buttons the message carries, for a run still going.
	Controls Controls `json:"controls,omitempty"`
	// Join, when the app is not a member of Channel, joins it (a public channel,
	// with channels:join) and posts once more. For a sender that must not fail on
	// membership: an alert page to the ops channel. APPENDED, as Update was.
	Join bool `json:"join,omitempty"`
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
//
// Cash says a payment the customer made funded the credit; Ref is then the payment's
// own reference. Unset, the credit is minted and Notes says who minted it and why.
type CreditIn struct {
	Subject string `json:"subject" validate:"required"`
	Amount  Money  `json:"amount"`
	Ref     string `json:"ref" validate:"required"`
	Notes   string `json:"notes,omitempty"`
	Tags    string `json:"tags,omitempty"`
	Cash    bool   `json:"cash,omitempty"`
}

// Credited reports what the credit wrote.
type Credited struct {
	Amount Money `json:"amount"`
	// ID is the ledger entry the credit created — the handle a reconciliation
	// names, and the one fact about a completed credit that cannot be derived
	// from the request that asked for it.
	//
	// The op always had it and threw it away, so a caller with no ledger of its
	// own could move money and then had nothing to cite for it: the admin grant
	// receipt's transactionId echoed the idempotency key it had just sent, which
	// reads like an id and identifies nothing in commerce's books.
	//
	// APPENDED at the end, because the wire is field ORDER — a field inserted
	// anywhere else changes what every existing peer reads.
	ID string `json:"id,omitempty"`
}

// ReverseIn names one deposit in the caller org's books to undo. The org rides the
// caller, as it does for a credit, so no input can name another tenant's books.
type ReverseIn struct {
	Entry  string `json:"entry" validate:"required"`
	Reason string `json:"reason,omitempty"`
}

// WireDeposit is one deposit in the caller org's books whose ref names a bank wire
// ("wire:…"), as the books hold it.
type WireDeposit struct {
	ID        string `json:"id"`        // the ledger entry
	Ref       string `json:"ref"`       // the deposit's ref, the wire's key
	Wallet    string `json:"wallet"`    // the account it credited: cash[:<name>], or wallet[:<name>] before cash was kept apart
	Amount    Money  `json:"amount"`    // what it credited
	CreatedAt int64  `json:"createdAt"` // unix seconds
	Reversed  bool   `json:"reversed"`  // a reversal undid it
}

// WireDeposits is every wire deposit in the caller org's books, oldest first.
type WireDeposits struct {
	Rows []WireDeposit `json:"rows"`
}

// SourceIn asks which of the caller org's earlier deposits were payments. Paid names
// the ones an operator read off a record of the payment; the ledger's owner adds every
// deposit its own processor receipts show settled. Apply false answers the table and
// moves nothing. The org rides the caller.
type SourceIn struct {
	Paid  []SourcePaid `json:"paid,omitempty"`
	Apply bool         `json:"apply,omitempty"`
}

// SourcePaid is one deposit named as a payment, and the record it was read off.
type SourcePaid struct {
	Entry string `json:"entry" validate:"required"`
	Basis string `json:"basis" validate:"required"`
}

// Sourcing is the table: every deposit in the org's books and whether it was a
// payment, and per wallet what the source entry moves (or moved).
type Sourcing struct {
	Deposits []SourceDeposit `json:"deposits"`
	Wallets  []SourceWallet  `json:"wallets"`
}

// SourceDeposit is one deposit: the bucket it funded, its ref and note, and whether
// it is cash and on what basis.
type SourceDeposit struct {
	Entry     string `json:"entry"`
	Wallet    string `json:"wallet"`
	Ref       string `json:"ref"`
	Memo      string `json:"memo,omitempty"`
	Amount    Money  `json:"amount"`
	CreatedAt int64  `json:"createdAt"`
	Reversed  bool   `json:"reversed,omitempty"`
	Cash      bool   `json:"cash"`
	Basis     string `json:"basis,omitempty"`
}

// SourceWallet is one wallet's source entry: the payments it holds (net of
// reversals), what of them is unspent replayed cash first, and the entry that
// records it — Done when it was recorded before.
type SourceWallet struct {
	Wallet  string   `json:"wallet"`
	Paid    Money    `json:"paid"`
	Cash    Money    `json:"cash"`
	Balance Money    `json:"balance"`
	Entries []string `json:"entries"`
	Holds   int      `json:"holds,omitempty"`
	Posted  string   `json:"posted,omitempty"`
	Done    bool     `json:"done,omitempty"`
}

// OrgCash is one org's customer money: Paid is what payments brought in, Owed the
// cash still unspent (Hanzo's liability to its customers), Spent what cash paid for,
// Credit the minted money still unspent. Books is false for an org with no ledger.
type OrgCash struct {
	Books  bool  `json:"books"`
	Paid   Money `json:"paid"`
	Owed   Money `json:"owed"`
	Spent  Money `json:"spent"`
	Credit Money `json:"credit"`
}

// Line is an org's own account and its credit line. Cash is what it paid and has
// not spent, Credit what was granted and not spent, Owed what it owes (usage the
// line paid for, the amount due), Limit the line and Left what of it is still to
// draw. Total is what the org may spend now — cash, the line left and credit —
// computed by finance.Funds.Total, the figure the balance read answers as Amount.
// Books is false for an org with no ledger, and every figure is then zero.
//
// Account is an org on account ([OnAccount]): its line has no limit and Left is
// always the same large figure. Since is when the account last went from owing
// nothing to owing, in unix seconds, 0 when it owes nothing or the debt began before
// the books kept the moment. Used is the metered usage the account paid for since
// Period, the start of the current UTC month in unix seconds.
type Line struct {
	Books   bool  `json:"books"`
	Cash    Money `json:"cash"`
	Credit  Money `json:"credit"`
	Owed    Money `json:"owed"`
	Limit   Money `json:"limit"`
	Left    Money `json:"left"`
	Total   Money `json:"total"`
	Account bool  `json:"account"`
	Since   int64 `json:"since,omitempty"`
	Used    Money `json:"used"`
	Period  int64 `json:"period"`
}

// LimitIn sets the caller org's credit limit. Reason is recorded on the trail.
type LimitIn struct {
	Limit  Money  `json:"limit"`
	Reason string `json:"reason,omitempty"`
}

// WaiveIn forgives what the caller org's own account currently owes. Reason is recorded on the trail.
type WaiveIn struct {
	Reason string `json:"reason,omitempty"`
}

// Reversed is the reversal entry, the deposit it undoes, the wallet the money left,
// and the amount.
type Reversed struct {
	ID     string `json:"id"`
	Of     string `json:"of"`
	Wallet string `json:"wallet"`
	Amount Money  `json:"amount"`
}

// ---- risk.decide — the scorer, from the process that holds the model --------

// Signal is one fact the asking gate observed, as a LIST element rather than a
// map entry: a map cannot cross this plane at all (zapenc refuses it at encode),
// and a signal map would have failed inside zip.Call before it reached the socket.
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
	// endpoint answerable: a payment is scored against the payer's own payment
	// history rather than against a spend distribution it has nothing to do with.
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
// It does not carry the client's Privileged bit either. Whether silence must deny
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
	// a counter, a timestamp or anything a caller of the settling endpoint chose. A
	// key the payer can predict is a key the payer can claim first, after which the
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
// plane POST /v1/event fills, /v1/event/insights reads and every product lens groups
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
	// name/value pair — because a map cannot cross this plane at all. They land in
	// the row's attributes map, so a new fact is a new value and never a schema
	// change.
	Attributes []Signal `json:"attributes,omitempty"`
	// Error, when set, states the occurrence as a FAILURE rather than an act: it
	// lands on the error signal, is grouped into an issue at ingest and is filed by
	// Sentinel — the same road a browser's error takes through /v1/event.
	Error *EventError `json:"error,omitempty"`
	// ID is the emitter's own id for this occurrence. Stating the same one twice is
	// one row: the warehouse keeps one row per (org, time, id), so an emitter that
	// states an occurrence again after a failure, with its id and its time, does
	// not count it twice. Empty, the plane mints one. It is the LAST field: the
	// plane's encoding is positional, so a field is only ever added at the end.
	ID string `json:"id,omitempty"`
}

// EventError is a failure an app states about itself. The text is scrubbed at
// ingest like every other exception, so a credential in a message never lands.
type EventError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
	// Path is the route the failure answered on.
	Path    string `json:"path,omitempty"`
	Handled bool   `json:"handled,omitempty"`
	// Fingerprint is the key the peer gives its own failure (sdk/fault): sixteen hex
	// digits over its code, its route or run and its site. When set the plane groups
	// the failure by it instead of by the message. Always the LAST field: the plane's
	// encoding is positional.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// EventCaptured is the honest receipt: what landed and what did not. A peer that
// states an occurrence the plane cannot route is TOLD so, rather than being given
// a 200 for a row that was never written — the exact silence that let an 88% loss
// run unnoticed on the HTTP endpoint.
type EventCaptured struct {
	// Accepted is how many occurrences were admitted and published.
	Accepted int `json:"accepted"`
	// Dropped is how many were not, because nothing about them named a landable
	// row.
	Dropped int `json:"dropped"`
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
	// KeepLocal is the project's choice to keep events from a developer's own machine.
	KeepLocal bool `json:"keepLocal"`
	// InternalNetworks are the project's own addresses (CIDRs); a visit from one is
	// the site's team.
	InternalNetworks []string `json:"internalNetworks,omitempty"`
}

// Site is a published site's serving facts. Found is explicit: a site that does
// not exist is a clean answer, not an error, and the edge must be able to tell
// "no such site" (honest 404) from "the owner could not be reached" (503) —
// collapsing them is how a transient failure would start serving 404s for real
// customers' live sites.
type Site struct {
	Found                bool   `json:"found"`
	Org                  string `json:"org"`
	Namespace            string `json:"namespace"`
	Slug                 string `json:"slug"`
	Bucket               string `json:"bucket"`
	Prefix               string `json:"prefix"`
	Status               string `json:"status"`
	CrossOriginIsolation bool   `json:"crossOriginIsolation"`
}

// NameIn is a name in the public namespace and what holds it for the CALLER's
// org: an app (its holder), never a site, which holds its name as its own slug.
// The org is the caller's plane identity and never a field.
type NameIn struct {
	Name   string `json:"name"`
	Holder string `json:"holder"`
}

// NameOut answers a claim: Taken when anyone else holds the name — another org,
// another holder in this one, a site, or the platform (a reserved label).
type NameOut struct {
	Taken bool `json:"taken"`
}

// EdgePurgeIn names what to clear from the edge cache: URL prefixes, host then
// path with no scheme (api.hanzo.ai/v1/pricing), each covering every URL under it.
type EdgePurgeIn struct {
	Prefixes []string `json:"prefixes"`
}

// EdgePurgeOut answers a purge. Purged is how many prefixes were handed to the
// edge; a purge the edge could not make is logged there and is not an error, since
// every cached answer also lapses by its own TTL.
type EdgePurgeOut struct {
	Purged int64 `json:"purged"`
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
	// SessionID narrows to one session. It never widens: the session must still
	// be the subject's own, so naming someone else's stops nothing.
	SessionID string `json:"sessionId,omitempty"`
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

// ---- todo ---------------------------------------------------------------

// IssueIn is one external work item to mirror into the native todo, keyed
// idempotently by ExtRef so a webhook redelivery updates the same row. No Org:
// the tenant is the caller's, resolved from the signed installation at the edge.
type IssueIn struct {
	// Project is the IAM project scope; empty means the org's default store.
	Project string `json:"project,omitempty"`
	// Key is the todo team the item files under, e.g. "GH"; ensured on first use.
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
	// State is the upstream open/closed state; the todo maps it to a column.
	State    string   `json:"state,omitempty"`
	Assignee string   `json:"assignee,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	// Channel binds the item to the conversation it was asked for in (Reply.Address).
	// It is written once: a row already bound keeps its channel, and an upsert that
	// names none leaves it alone.
	Channel string `json:"channel,omitempty"`
}

// IssueUpserted reports what the upsert did, so a feeder can count precisely.
type IssueUpserted struct {
	// Created distinguishes a new row from an update.
	Created bool `json:"created"`
	// Number is the todo's own item number.
	Number int `json:"number"`
	// Identifier is KEY-<number>.
	Identifier string `json:"identifier,omitempty"`
	// Channel is the conversation the item is bound to, "" for none — where what
	// happens to it next is said.
	Channel string `json:"channel,omitempty"`
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
// Builds is how many builds the push actually LAUNCHED, and it is here because
// something finally reads it: the forge's push receiver answers its delivery with
// this number, and without it "accepted" and "built" are the same answer. Most
// pushes track no application, so zero is ordinary rather than an error — but it
// is a different fact from one, and collapsing the two is what let a green
// delivery page sit on top of a fleet that had built nothing.
//
// Appended at the END, which is what this type's own note said the day would
// look like.
type Built struct {
	Repo   string `json:"repo"`
	Builds int    `json:"builds"`
}

// JobIn is one workflow job a provider has queued, with what the runner that
// takes it registers with. The forge hands a registration token and the labels
// the job asked for; GitHub hands a just-in-time configuration that already
// carries both. No Org: the tenant is the caller's.
type JobIn struct {
	Provider string   `json:"provider"` // forge | github
	Repo     string   `json:"repo"`     // owner/name
	ID       int64    `json:"id"`       // the provider's job id
	Labels   []string `json:"labels,omitempty"`
	URL      string   `json:"url,omitempty"`   // the forge to register with
	Token    string   `json:"token,omitempty"` // the registration token, or the JIT configuration
	// Try is 0 for a delivery's runner, and n for the nth the queue loop starts
	// for the same job; it names the Job (cloud.RunnerName).
	Try int `json:"try,omitempty"`
}

// Launched names the Job that will take the workflow job. A failure is an
// error, never this shape.
type Launched struct {
	Job string `json:"job"`
}

// BuildIn is one image to build from a repository at a commit — what a native
// pipeline (`.hanzo/workflows/*.yml` images) asks platform for, one call per
// image. No Org: the tenant is the caller's, stated through cloud.For by the
// git app that saw the push land, because an ask that could name an org could
// build into another tenant's registry.
type BuildIn struct {
	Repo         string  `json:"repo"`
	SHA          string  `json:"sha"`
	Image        string  `json:"image"`
	Branch       string  `json:"branch,omitempty"`
	Ref          string  `json:"ref,omitempty"`
	Dockerfile   string  `json:"dockerfile,omitempty"`
	Context      string  `json:"context,omitempty"`
	DockerTarget string  `json:"dockerTarget,omitempty"`
	OS           string  `json:"os,omitempty"`
	Arch         string  `json:"arch,omitempty"`
	Args         []Field `json:"args,omitempty"`
	// Platforms are the `<os>/<arch>` pairs the image is built for. Empty builds
	// one image for the default architecture; two publishes one manifest index
	// over an image built on a node of each.
	Platforms []string `json:"platforms,omitempty"`
}

// Queued is a build platform accepted.
type Queued struct {
	BuildJobID string `json:"buildJobId"`
	Status     string `json:"status"`
	RunnerPool string `json:"runnerPool"`
	Image      string `json:"image,omitempty"`
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

// ProjectIn asks for one new project in the CALLER's org. The org is not here
// for the reason it is nowhere in this file.
//
// Slug is taken exactly as given and never renumbered: a slug the org already
// holds is a 409, so the caller that chose it is the one that picks the next.
type ProjectIn struct {
	// Name is the display name.
	Name string `json:"name"`
	// Slug is the org-unique handle, under POST /v1/project's rules.
	Slug string `json:"slug"`
	// Repo is the clone address of the source the project builds from. Empty
	// links none.
	Repo string `json:"repo,omitempty"`
}

// LinkIn names a repository the CALLER's org linked. The org is not here for
// the reason it is nowhere in this file.
type LinkIn struct {
	// Repo is the repository's https clone address.
	Repo string `json:"repo"`
}

// ProjectOut is the project ProjectsCreate or ProjectsLink answered.
type ProjectOut struct {
	// ID is the project's internal identifier.
	ID string `json:"id"`
	// Slug is the handle the project answers to, the one it was made under.
	Slug string `json:"slug"`
}

// WorkspaceIn asks for the CALLER's code workspace on the forge. The org is not
// here for the reason it is nowhere in this file.
type WorkspaceIn struct {
	// Make is whether a workspace the org does not have yet is made now. False
	// only reads, and an org with none is answered with an empty Owner.
	Make bool `json:"make,omitempty"`
}

// Workspace is where the caller's org keeps its code on the forge.
type Workspace struct {
	// Owner is the forge namespace, empty when the org has none.
	Owner string `json:"owner,omitempty"`
	// Made says the platform made the namespace for the org, so the platform acts
	// there and nobody in it has a forge account. False is the estate's own.
	Made bool `json:"made,omitempty"`
}

// SiteOpenIn opens a deployment of one of the CALLER's projects, built from the
// project's own repository. The org is not here for the reason it is nowhere in
// this file.
type SiteOpenIn struct {
	// Slug is the project.
	Slug string `json:"slug"`
	// Owner and Repo name the repository the build is of, when the caller knows
	// it. They must name the project's own repository; empty takes it.
	Owner string `json:"owner,omitempty"`
	Repo  string `json:"repo,omitempty"`
}

// SiteOpened is a deployment ProjectsSiteOpen opened, and what building it needs.
type SiteOpened struct {
	// ID is the deployment, the handle ProjectsSiteClose ends.
	ID string `json:"id"`
	// Owner and Repo are the project's repository, in the org's own workspace.
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	// Branch is the branch the project builds from when no other is asked for.
	Branch string `json:"branch"`
	// Upload is the grant the built site is written with.
	Upload SiteUpload `json:"upload"`
}

// SiteUpload is a short-lived permission to write one site's objects straight
// to object storage: a presigned POST policy the store itself confines to
// Prefix. Fields carry verbatim on every POST beside `key`, `Content-Type` and
// `file`, and the signature covers them.
type SiteUpload struct {
	URL       string  `json:"url"`
	Fields    []Field `json:"fields"`
	Prefix    string  `json:"prefix"`
	MaxBytes  int64   `json:"maxBytes"`
	ExpiresAt int64   `json:"expiresAt"`
}

// Field is one name and its value. A set of them crosses the plane as a list,
// because zapenc refuses a map at encode.
type Field struct {
	// Name is the field's name.
	Name string `json:"name"`
	// Value is the field's value, verbatim.
	Value string `json:"value"`
}

// Fields is m as a list, in name order, so one map always crosses as the same
// bytes.
func Fields(m map[string]string) []Field {
	if len(m) == 0 {
		return nil
	}
	out := make([]Field, 0, len(m))
	for k, v := range m {
		out = append(out, Field{Name: k, Value: v})
	}
	slices.SortFunc(out, func(a, b Field) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Named is fs as a map; a name given twice keeps its last value.
func Named(fs []Field) map[string]string {
	if len(fs) == 0 {
		return nil
	}
	out := make(map[string]string, len(fs))
	for _, f := range fs {
		out[f.Name] = f.Value
	}
	return out
}

// SiteCloseIn ends a deployment ProjectsSiteOpen opened.
type SiteCloseIn struct {
	// Slug and ID name the project and the deployment.
	Slug string `json:"slug"`
	ID   string `json:"id"`
	// Live says the build uploaded; false ends it failed.
	Live bool `json:"live,omitempty"`
	// Commit is the commit that was built.
	Commit string `json:"commit,omitempty"`
	// Message says why a failed build failed.
	Message string `json:"message,omitempty"`
	// Files and Bytes count what was uploaded.
	Files int   `json:"files,omitempty"`
	Bytes int64 `json:"bytes,omitempty"`
	// Keys are the uploaded objects, relative to the grant's prefix. The site's
	// prefix is reconciled against them, so a page the build no longer makes stops
	// being served.
	Keys []string `json:"keys,omitempty"`
}

// SiteClosed is how a deployment ended.
type SiteClosed struct {
	// Status is live or error.
	Status string `json:"status"`
	// LiveURL is the address the site serves at, empty when it has none.
	LiveURL string `json:"liveUrl,omitempty"`
	// Message is why it failed, or why a live site has no address.
	Message string `json:"message,omitempty"`
}

// SiteBuildsIn lists the CALLER's sandbox-built deployments.
type SiteBuildsIn struct {
	// Limit caps the list; anything not positive is 50.
	Limit int `json:"limit,omitempty"`
}

// SiteBuilds is the caller's sandbox-built deployments, newest first.
type SiteBuilds struct {
	Builds []SiteBuild `json:"builds"`
}

// SiteBuild is one sandbox-built deployment.
type SiteBuild struct {
	// ID is the deployment.
	ID string `json:"id"`
	// Project is the slug of the project it deploys.
	Project string `json:"project"`
	// Repo is the project's clone address.
	Repo string `json:"repo"`
	// Commit is the commit that was built, empty until it is known.
	Commit string `json:"commit,omitempty"`
	// Status is building, live or error.
	Status string `json:"status"`
	// Message is why it failed.
	Message string `json:"message,omitempty"`
	// CreatedAt and UpdatedAt are Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// SiteAppsIn names one of the CALLER's sites, or none for all of them.
type SiteAppsIn struct {
	// Slug is the site to read with its history; empty lists every site that has
	// served something, without history.
	Slug string `json:"slug,omitempty"`
}

// SiteApps is the caller's sites as deployed applications.
type SiteApps struct {
	Sites []SiteApp `json:"sites"`
}

// SiteApp is one site as a deployed application: what it serves now, and what
// it can be rolled back to.
type SiteApp struct {
	// Slug is the site.
	Slug string `json:"slug"`
	// Status is live, building, error or empty.
	Status string `json:"status"`
	// LiveURL is where it serves, empty when it has no address.
	LiveURL string `json:"liveUrl,omitempty"`
	// Revision is the release the site serves: the one a rollback pointed it at,
	// else the one its live deployment was kept as. Empty when neither is known,
	// as for a site whose files are not kept.
	Revision string `json:"revision,omitempty"`
	// Version and Commit are the deployment that made Revision live, when known.
	Version int    `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	// UpdatedAt is when the site last changed, Unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
	// History is the site's live deployments whose kept release retention still
	// holds, newest first: what it can be rolled back to. Only a one-site read
	// answers it.
	History []SiteRevision `json:"history,omitempty"`
}

// SiteRevision is one live deployment of a site and the release it was kept as.
type SiteRevision struct {
	// Release is the kept release, the revision a rollback names.
	Release string `json:"release"`
	// Deploy, Version and Commit are the deployment that went live with it.
	Deploy  string `json:"deploy"`
	Version int    `json:"version"`
	Commit  string `json:"commit,omitempty"`
	// At is when that deployment went live, Unix seconds.
	At int64 `json:"at"`
	// Objects and Bytes are the release's size.
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

// SiteRollbackIn names a site and the kept release it should serve again.
type SiteRollbackIn struct {
	// Slug is the site.
	Slug string `json:"slug"`
	// Release is one of the site's kept releases (SiteApp.History), not the one
	// it serves.
	Release string `json:"release"`
}

// PublishIn asks for one of the CALLER's projects to be built from its
// repository in a sandbox and published to its site.
type PublishIn struct {
	// Project is the project's slug.
	Project string `json:"project"`
	// Owner and Repo name the repository, when the caller knows it; they must be
	// the project's own. Empty takes it.
	Owner string `json:"owner,omitempty"`
	Repo  string `json:"repo,omitempty"`
	// Ref is the branch to build; empty is the project's own.
	Ref string `json:"ref,omitempty"`
}

// Publishing is a publish that was admitted. It builds in the background; the
// deployment is how it is followed.
type Publishing struct {
	// Deployment is the deployment the build ends, on the project's history.
	Deployment string `json:"deployment"`
	// Project is the project's slug.
	Project string `json:"project"`
	// Ref is the branch being built.
	Ref string `json:"ref"`
	// Status is building.
	Status string `json:"status"`
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
// TransportIngestIn is one authenticated inbound chat event crossing to transport.
// Org is resolved by the adapter via OrgForExternalID on a signature-VERIFIED
// payload — never a client-supplied field — and is the isolation root: a
// workspace's events reach only the org that connected that workspace.
// ChatSendIn is one outbound message. Provider picks the transport on the
// ANSWERING side, where the org's token for it lives; nothing here names a
// credential.
type ChatSendIn struct {
	// Org is the tenant this message is sent AS, and it is REQUIRED: it resolves
	// the credential the send spends, so a send that omits it names no custody to
	// check. The answering side refuses an empty one rather than letting each
	// transport discover it separately.
	Org      string `json:"org"`
	Provider string `json:"provider"`
	Channel  string `json:"channel"`  // channel / conversation / chat id
	ReplyTo  string `json:"reply_to"` // thread or message to reply under, "" when unthreaded
	Root     string `json:"root"`     // transport-verified reply root (Teams' serviceURL), "" elsewhere
	Text     string `json:"text"`
	// User is who the message is FOR when Private is set — a sign-in prompt
	// carries a URL bound to a nonce for one person, and a channel must never see it.
	User string `json:"user"`
	// Private asks the transport for a reply only User can see. Not every
	// transport has one; the answering side refuses rather than falling back to
	// the channel, because posting a link meant for one person to everyone is the
	// failure this exists to prevent.
	Private bool `json:"private"`
	// Edit is a message this send REPLACES, by the id a previous send returned.
	// It is how one turn keeps one message: progress, then the answer, in the
	// same place. A transport that cannot edit refuses rather than posting a
	// second message the caller did not ask for.
	Edit string `json:"edit,omitempty"`
	// Controls are the buttons the message carries, for a run still going.
	Controls Controls `json:"controls,omitempty"`
	// Question is the question the message asks, for a run waiting on its answer.
	Question Question `json:"question,omitempty"`
	// Group asks WhatsApp to address Channel as a group id (recipient_type
	// "group") rather than a person's number. Every other transport ignores it.
	// Appended: an older peer stops before this field.
	Group bool `json:"group,omitempty"`
}

// Question is a question a run asks its person, on the message that asks it. A
// transport with buttons draws one per option (Slack, as Block Kit); one without
// shows the message's text, which lists the options.
type Question struct {
	// ID is the question a press answers. Empty is no question.
	ID string `json:"id,omitempty"`
	// Text is the question itself.
	Text    string   `json:"text,omitempty"`
	Options []Option `json:"options,omitempty"`
	// Owner is the platform's id for the person the question is for, named to
	// anyone else who presses.
	Owner string `json:"owner,omitempty"`
}

// Option is one answer a question offers.
type Option struct {
	// Label is the answer, as it is shown and as it is returned: 1 to 75
	// characters, which is what one Slack button holds.
	Label string `json:"label"`
	// Description says what choosing it means, up to 150 characters.
	Description string `json:"description,omitempty"`
}

// AskIn is ask_user's argument: one question and the answers it offers. It
// names no conversation and no person — a run asks where it is already being
// read, and only the person it runs for can answer.
type AskIn struct {
	// Question is what to ask, up to 500 characters.
	Question string `json:"question"`
	// Options are the 2 to 8 answers to offer. The person may also reply in their
	// own words, and that reply is the answer.
	Options []Option `json:"options"`
}

// AnswerIn is an answer to a question from a surface acting for the person it
// names. The org is the caller's, from the plane context; the question must be
// Subject's own.
type AnswerIn struct {
	// Subject is the person the surface resolved, unqualified.
	Subject string `json:"subject"`
	// Question names the question. Empty answers the one Subject has open in
	// Reply's conversation, which is how a reply in the thread reaches it.
	Question string `json:"question,omitempty"`
	Reply    Reply  `json:"reply,omitempty"`
	// Text is the answer: a pressed option's label, or what was said.
	Text string `json:"text"`
	// Chosen says Text is an option that was pressed, which must then be one the
	// question offered.
	Chosen bool `json:"chosen,omitempty"`
	// By is the platform's id for the person who answered, named on the question.
	By string `json:"by,omitempty"`
}

// Answered is the question an answer closed, and the answer it closed it with.
type Answered struct {
	Question string `json:"question"`
	Text     string `json:"text"`
}

// Controls are the buttons a run's message carries, so the person whose run it
// is can stop, pause or continue it from where they are reading it. The sender
// decides which from the run's state; a transport with buttons draws them (Slack
// as Block Kit) and one without shows the text alone.
type Controls struct {
	// Session is the run the buttons act on. Empty is no buttons: the run is over.
	Session string `json:"session,omitempty"`
	// Kind is what the session runs — ControlsTurn or ControlsCoding — which is
	// what Continue has to start.
	Kind string `json:"kind,omitempty"`
	// Paused shows Continue and Stop; otherwise the run is going, and it is Stop
	// and Pause.
	Paused bool `json:"paused,omitempty"`
	// Owner is the platform's id for the person whose run it is, named to anyone
	// else who presses.
	Owner string `json:"owner,omitempty"`
}

// The two kinds of run a message's buttons steer.
const (
	ControlsTurn   = "turn"
	ControlsCoding = "coding"
)

// ChatSendOut carries the platform's id for the message, when it gives one.
type ChatSendOut struct {
	MessageID string `json:"message_id"`
}

// ChatHistoryIn names one chat conversation to read from the platform itself.
// The org is the isolation root resolved by the adapter from a signed payload.
type ChatHistoryIn struct {
	Org        string `json:"org"`
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id,omitempty"`
	Channel    string `json:"channel"`
	ThreadID   string `json:"thread_id,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	Text       string `json:"text,omitempty"`
}

// ChatHistoryOut carries the platform's recent turns, oldest first, excluding
// the triggering message itself.
type ChatHistoryOut struct {
	Turns []Turn `json:"turns"`
}

// ChatIdentityIn names one chat user at one workspace. The org is the isolation
// root the adapter already resolved from a signature-verified payload.
type ChatIdentityIn struct {
	Org        string `json:"org"`
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
	User       string `json:"user"`
}

// ChatIdentityOut is the linked account, or the sentence to say instead of
// running anything. Subject is meaningful only when Say is empty; Ephemeral
// marks a reply that carries a sign-in URL and must reach only the asker.
//
// It carries NO token. A turn needs to know who it runs as, not how to prove it.
type ChatIdentityOut struct {
	Subject   string `json:"subject"`
	Model     string `json:"model"`
	Say       string `json:"say"`
	Ephemeral bool   `json:"ephemeral"`
}

type TransportIngestIn struct {
	Org        string `json:"org"`
	Provider   string `json:"provider"`    // "slack","teams","discord","telegram"
	ExternalID string `json:"external_id"` // workspace/tenant/guild/chat id
	User       string `json:"user"`        // platform-verified user id
	Channel    string `json:"channel"`     // reply target
	ThreadID   string `json:"thread_id"`   // the thread this was said in, "" at the top level
	Text       string `json:"text"`        // the prompt, mention stripped
	DedupeKey  string `json:"dedupe_key"`  // event id, "" when non-dedupable
	// MessageID is the platform's id for THIS message — on Slack its ts — and it
	// is the address a NEW thread is rooted at.
	//
	// It is a third fact, distinct from both of its neighbours: ThreadID is the
	// thread the message is already IN, and DedupeKey identifies the DELIVERY
	// rather than the message. One field standing in for two of them is what made
	// every reply a thread — an adapter reported "the thread, or my own ts when I
	// am not in one", so the answering side could no longer tell a message typed
	// in a channel from one typed in a thread, and answered both the same way.
	MessageID string `json:"message_id"`
	// ReplyRoot is a transport-verified reply root: Teams' JWT-verified
	// serviceURL, "" everywhere else.
	ReplyRoot string `json:"reply_root"`
	// Installer is the provider-side user who completed this workspace's install,
	// from the connection the adapter already resolved. It rides the event because
	// the connection lives in the adapter's process and the gate lives in another.
	// Whoever finished the OAuth was already an admin of this org.
	Installer string `json:"installer"`
	// Public says everyone in the workspace can read the channel — on Slack a message
	// event's channel_type "channel". False where the event does not say.
	Public bool `json:"public,omitempty"`
	// Group classifies Channel as a multi-person channel rather than a direct
	// message. WhatsApp sets it when the delivery names a group_id. Appended:
	// an older peer stops before this field.
	Group bool `json:"group,omitempty"`
	// Attachments are media the adapter already fetched for this message.
	// Appended, after Group.
	Attachments []IngestAttachment `json:"attachments,omitempty"`
}

// IngestAttachment is one piece of media an adapter fetched before the event
// crossed. Kind is the envelope's closed set: image, audio, video or file.
// URL is a handle for those bytes, never a credentialed download address.
// MIME was sniffed from the bytes. Name is the filename when the platform
// gave one.
type IngestAttachment struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	MIME string `json:"mime,omitempty"`
	Name string `json:"name,omitempty"`
}

// TransportIngestOut reports whether the event was taken. A consumer that is not
// mounted, or an event for a transport it does not carry, answers false — which
// is a fact worth having rather than a silent drop.
type TransportIngestOut struct {
	Taken bool `json:"taken"`
}

// Reply is WHERE A CONVERSATION IS HAPPENING: the one address that work started
// by a turn can talk back into.
//
// A chat turn answers in its own reply, and a CODING RUN it starts is followed
// by that turn into the same message (Turn, below; apps/agents/follow.go). A run
// with no turn following it — one a slash command started, or a follow-up of one
// whose turn is over — is admitted in milliseconds and runs for minutes, and
// this is the only way back for its progress.
//
// It travels because only the BRIDGE holds it — the channel is a fact about the
// transport, the same reason [Turn] history rides a turn — and because a model
// cannot know a channel id and must never be asked to invent one.
//
// It is an ADDRESS AND NOT A TOKEN. It says where to put text; the process that
// owns the workspace's bot credential is the one that posts. So a run reports
// into a thread without the engine ever holding the token that could post
// anywhere else in that workspace.
type Reply struct {
	// Provider is the transport the conversation is on — slack, discord,
	// telegram, teams — and it is what decides who can carry the text at all. An
	// address without it names a channel in no particular workspace.
	Provider string `json:"provider,omitempty"`
	// Channel is the conversation inside that workspace, and it is what decides whether
	// there is an address at all. Empty is the ordinary case: a caller of
	// /v1/agent/coding holds a session id and reads the stream instead.
	Channel string `json:"channel,omitempty"`
	// Thread narrows the address to one thread inside the channel — on Slack the
	// parent message's ts, the same value a reply carries as thread_ts. Empty
	// puts the text at the top level of the channel.
	Thread string `json:"thread,omitempty"`
	// User is the platform's id for the person the conversation is with — whose
	// run it is. A run's buttons name them to anyone else who presses one; it is
	// never what decides who may (that is the chat identity).
	User string `json:"user,omitempty"`
	// Turn is the turn that renders a run started here: the session whose one
	// message the run's progress and its outcome are shown in. A run with a Turn
	// posts nothing of its own — one question, one message. The dispatcher writes
	// it (apps/agents/replyto.go); a model never does.
	Turn string `json:"turn,omitempty"`
}

// Address is the reply as a work item's channel (IssueIn.Channel): the transport, the
// channel and the thread, so an issue filed in a Slack thread names that thread
// and what happens to it is said there. "" for an address with no channel.
func (r Reply) Address() string {
	if strings.TrimSpace(r.Provider) == "" || strings.TrimSpace(r.Channel) == "" {
		return ""
	}
	return r.Provider + ":" + r.Channel + "/" + r.Thread
}

// ReplyTo is the reply an address names, when it names a transport conversation —
// the inverse of Address. An address that is not one (a team channel is
// "<space>_<channel>") answers false.
func ReplyTo(address string) (Reply, bool) {
	provider, rest, ok := strings.Cut(address, ":")
	if !ok || provider == "" {
		return Reply{}, false
	}
	channel, thread, _ := strings.Cut(rest, "/")
	if channel == "" {
		return Reply{}, false
	}
	return Reply{Provider: provider, Channel: channel, Thread: thread}, true
}

// Origin is the message a turn answers, beyond the channel its Reply names: what a
// fact filed during the turn cites, and whether it may be filed at all. Only the
// bridge that received the message knows any of it.
type Origin struct {
	// Account is the workspace the channel is in — on Slack the team id — lowercased,
	// as the channels envelope holds it.
	Account string `json:"account,omitempty"`
	// Message is the message the turn answers — on Slack its ts. Empty where the
	// turn did not start from a message, as a slash command does not.
	Message string `json:"message,omitempty"`
	// Public says everyone in the workspace can read the channel, as a public Slack
	// channel. The graph is read by the whole organization, so a fact is filed
	// only from such a channel. False for a DM, a private channel, and a channel the
	// bridge cannot tell.
	Public bool `json:"public,omitempty"`
}

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
	// History is the conversation this Input arrived in, oldest first and NOT
	// including Input itself. Empty is a first message, which is a real answer and
	// not a missing one.
	//
	// It rides the turn because the answering side has no way to obtain it: the
	// channel is a fact about the transport the bridge is on, and only the bridge knows
	// it. Without this the agent saw one message with nothing around it, which is
	// how "try again" became "what would you like me to help you with".
	History []Turn `json:"history,omitempty"`
	// Reply is WHERE A RUN THIS TURN STARTS SHOULD NARRATE, and it rides the turn
	// for exactly the reason History does: the channel is a fact about the transport
	// the bridge is on, and only the bridge knows it.
	//
	// A coding run is a tool the brain calls, and the brain cannot know a channel
	// id — so the model never supplies this and must not be asked to. The
	// dispatcher writes it into the coding call after the model has chosen the
	// tool (apps/agents/replyto.go). Empty means the surface has nowhere for a run
	// to talk, which is the app surface's case: it holds a session id and reads
	// the stream instead.
	Reply Reply `json:"reply,omitempty"`
	// Origin is the message this turn answers, which a fact filed in the turn cites.
	Origin Origin `json:"origin,omitempty"`
	// Resume asks to CONTINUE rather than begin: when the Subject's last turn in
	// this Reply's conversation was stopped or paused, that turn runs again — the
	// same session, and the same sandbox, with every file it had written — with
	// Input as what it is told next. Anything else is an ordinary new turn, so a
	// bridge sets this on a "continue" without first asking whether there is one.
	Resume bool `json:"resume,omitempty"`
}

// RecentIn asks for the last turns of one channel. The ORG is the caller's, from
// the plane context and never an argument — an org a caller could name is an org
// whose conversations a caller could read.
// AgentForIn names a channel; the org is the caller's, from the plane context.
type AgentForIn struct {
	Transport string `json:"transport" validate:"required"`
	Channel   string `json:"channel"`
}

// AgentFor is the agent ref that answers there.
type AgentFor struct {
	Ref string `json:"ref"`
}

type RecentIn struct {
	// Transport is the network (slack, discord, telegram) and Channel the id within
	// it. Both are required: a channel id is only unique inside its transport.
	Transport string `json:"transport" validate:"required"`
	Channel   string `json:"channel" validate:"required"`
	// Account is the bot the channel is read under — a DM's channel id is the human's
	// own id on Telegram and WhatsApp, the same under every bot of the org, so the
	// history is scoped to one bot. Empty reads only the rows recorded with no bot.
	Account string `json:"account,omitempty"`
	// Limit caps how many turns come back, newest last. Zero takes a sane default;
	// the owner clamps it, because an unbounded history is a prompt that costs more
	// than the answer.
	Limit int `json:"limit,omitempty"`
}

// Turn is one thing somebody said, in the order it was said.
type Turn struct {
	// Sender is the platform user id, and Self marks the assistant's own turns so a
	// reader can tell a question from its answer without parsing either.
	Sender string `json:"sender,omitempty"`
	Self   bool   `json:"self,omitempty"`
	Text   string `json:"text"`
	At     int64  `json:"at,omitempty"`
}

// Recent is the conversation, oldest first — the order a reader needs and not the
// order the store returns.
type Recent struct {
	Turns []Turn `json:"turns"`
}

// RosterIn asks agents for the caller's own agents. It carries NOTHING, and the
// emptiness is the design: the tenant is the caller's, stated on the wire before
// the hop, because an org a caller could name is an org whose agents a caller
// could enumerate. The one field this type will never grow is Org.
type RosterIn struct{}

// AgentRoster is an org's agents, as much of each as a membership projection
// needs and no more. It is deliberately NOT the agents.Agent row: a roster
// reader wants to name an agent and say whether it is live, and shipping the
// instructions, model and tool grants across the plane would put an agent's
// whole configuration on a wire that only needs its name.
type AgentRoster struct {
	Agents []iam.AgentBrief `json:"agents"`
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
	// Why a non-"ok" run failed. A run that EXECUTED and whose model failed comes
	// back with a nil error and a status, so without this the cause was reachable
	// only by finding the run row by hand — which is how a broken inference wire
	// survived a day of looking while the bridge posted a generic sentence.
	Error string `json:"error,omitempty"`
}

// TransportStopIn names a channel and the person asking to stop. The org is the
// caller's, from the plane context. The subject is resolved by transport from
// the chat identity, so a caller cannot name someone else's.
type TransportStopIn struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Channel  string `json:"channel"`
	User     string `json:"user"` // the platform's id for the person who asked
}

// SessionControlIn is one steering command for one session, from a surface
// acting for the person it names. The org is the caller's, from the plane
// context; the session must be Subject's own, and anyone else's answers 403.
type SessionControlIn struct {
	// Subject is the person the surface resolved, unqualified. The answering side
	// qualifies it with the caller's org and requires the session to be theirs.
	Subject   string `json:"subject"`
	SessionID string `json:"sessionId"`
	// Command is stop, pause, resume or message (SessionStop …).
	Command string `json:"command"`
	// Message is what a message says to the run, and what a resume tells it next.
	Message string `json:"message,omitempty"`
	// Reply and Origin are where a resumed turn is being read, for the run it
	// starts to narrate into and cite, as RunOnBehalfIn carries them.
	Reply  Reply  `json:"reply,omitempty"`
	Origin Origin `json:"origin,omitempty"`
}

// SessionControlled is what a command found and what it started.
type SessionControlled struct {
	// Was is the session's status when the command reached it. A surface that
	// stopped a paused run learns from it that no live run is left to say so.
	Was string `json:"was"`
	// Run is the run a resume started; empty for every other command.
	Run string `json:"run,omitempty"`
}

// The four steering commands, as SessionControlIn.Command spells them.
const (
	SessionStop    = "stop"
	SessionPause   = "pause"
	SessionResume  = "resume"
	SessionMessage = "message"
	// SessionAnswer answers a question the session's run asked.
	SessionAnswer = "answer"
)

// TransportActIn is one press of a run's button. The org is the caller's, from
// the plane context; who pressed is resolved from User by transport, never taken
// as a subject.
type TransportActIn struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Channel  string `json:"channel"`
	// Thread is the thread the pressed message is in, "" at the top of a channel.
	Thread string `json:"thread,omitempty"`
	// Message is the pressed message, which the run's state is shown in.
	Message string `json:"message"`
	User    string `json:"user"` // the platform's id for the person who pressed
	// Command is stop, pause or resume (SessionStop …), or answer (SessionAnswer)
	// for a press on a question's option.
	Command string `json:"command"`
	// Controls are the buttons as they were drawn: which run, of what kind, whose.
	Controls Controls `json:"controls"`
	// Question and Answer are the question an option was pressed on and the
	// option's label, for an answer.
	Question string `json:"question,omitempty"`
	Answer   string `json:"answer,omitempty"`
}

// TurnStarted names the session a turn runs as, and the run it executes.
type TurnStarted struct {
	SessionID string `json:"sessionId"`
	RunID     string `json:"runId"`
}

// TurnEventsIn asks what a turn has said after sequence number After. The org
// is the caller's, from the plane context.
type TurnEventsIn struct {
	SessionID string `json:"sessionId"`
	After     int64  `json:"after"`
}

// TurnEvents is what a turn said after After, and how it ended once it has.
type TurnEvents struct {
	Events []TurnEvent `json:"events"`
	// Next is the After to ask with next time.
	Next int64 `json:"next"`
	// Outcome is completed, failed, stopped or paused once the turn has stopped
	// working, and empty while it runs. A stopped or paused turn can be resumed.
	Outcome string `json:"outcome,omitempty"`
	// Error is why a failed turn failed.
	Error string `json:"error,omitempty"`
	// Tools is a concise one-line-per-call summary of tools the turn invoked
	// (capped at ~1.5 KB), stored with the reply for conversation history.
	Tools string `json:"tools,omitempty"`
	// Park is set while a coding run waits for a refusal to clear and goes on by itself:
	// the turn has NOT ended (Outcome is empty), so a caller keeps waiting, for as long
	// as it is prepared to, and may say why the run stands still.
	Park *TurnPark `json:"park,omitempty"`
}

// TurnPark is why a coding run waits, and when it goes on by itself.
type TurnPark struct {
	// Reason is limit (a usage limit), payment (a refusal by the payer) or fault (the model
	// API failed the run's calls).
	Reason string `json:"reason"`
	// ResumeAt is when the authority that refused the run says it may be asked again,
	// unix seconds, and 0 when none names a time: the run then goes on when credits are
	// added, or when a person presses Continue.
	ResumeAt int64 `json:"resumeAt,omitempty"`
}

// TurnEvent is one line a person may be shown: what the turn is doing
// (progress) or what it concluded (answer). Never a tool's arguments or output.
type TurnEvent struct {
	Seq  int64  `json:"seq"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Turn outcomes, and the event kinds a surface renders.
const (
	TurnCompleted = "completed"
	TurnFailed    = "failed"
	TurnStopped   = "stopped"
	TurnPaused    = "paused"

	TurnProgress = "progress"
	TurnAnswer   = "answer"
	// TurnRun names the coding run the turn is following, by its session (Text):
	// the turn's message shows that run's steps, and its buttons act on that run.
	TurnRun = "run"
	// TurnRunPaused says the run the turn followed is paused (Text): the turn ends,
	// and its message keeps that run's Continue and Stop.
	TurnRunPaused = "run-paused"
)

// ---- sandbox ---------------------------------------------------------------

// LeaseIn asks for a sandbox. ID names one to RESUME: a lease that is still
// running comes back as it is, a PARKED one is given a pod again with its disk as
// it was left, and one that has ended is replaced by a fresh sandbox rather than
// refused — the caller is resuming a conversation whose lease expired while they
// were reading, and a working sandbox with a new id is the honest answer to that.
type LeaseIn struct {
	// ID names a sandbox to RESUME, and is the id an earlier lease answered with.
	// Empty asks for a new one. A caller that holds an id and omits it does not get
	// a second view of the same computer, it gets a second computer.
	ID string `json:"id,omitempty"`
	// Class is what KIND of computer to lease, and the set is closed:
	//
	//	exec     a throwaway one that keeps nothing. Seconds to minutes.
	//	dev      a coding one, with the project's own disk attached. Hours.
	//	desktop  a dev one that also has a screen.
	//	browser  a desktop with Chromium open full screen, for a page to watch or drive.
	//	android  a desktop with a phone running on that screen.
	//
	// Empty leases an `exec`, which is the right answer for running a program and
	// the wrong one for working on a repository, because it keeps nothing.
	//
	// An `android` needs a node that can virtualise a CPU, so it is the one class
	// a deployment may not be able to place. Where the fleet has none, the lease
	// succeeds and the pod stays Pending naming the device it is waiting for —
	// which is the honest answer, because the alternative is an emulator running
	// on an interpreted CPU and never finishing its boot.
	Class string `json:"class,omitempty"`
	// Project names the disk to attach, and is REQUIRED for every class but `exec`.
	//
	// One live sandbox per project: the disk attaches to one computer at a time, so
	// a second lease over a project that already has one is refused by name rather
	// than handed a silently empty disk.
	Project string `json:"project,omitempty"`
	// Runtime is the isolation boundary asked for: `gvisor` shares a filesystem
	// and holds a project volume, `kata-fc` is a microVM that boots slower and
	// reads files faster but has no shared filesystem at all. Empty asks for the
	// fleet's default, which is the right answer unless you are measuring.
	//
	// It is a REQUEST. The owner decides, and refuses a combination it cannot
	// honour — a volume under a runtime with no shared filesystem would write
	// into a tmpfs and lose the bytes at exit. Read Leased.Runtime for what the
	// sandbox actually got.
	Runtime string `json:"runtime,omitempty"`
	// TTLSec bounds the lease in seconds. Unset takes the class default. Nothing
	// runs forever, because a sandbox is somebody else's code on our nodes.
	TTLSec int `json:"ttlSec,omitempty"`
	// Cluster names one of the org's ATTACHED clusters to run the lease on —
	// the fleet-local name a BYO cluster was registered under. Empty runs where
	// leases have always run, the home cluster. The named cluster must carry
	// the sandbox namespace and the gvisor runtime class; a name the org has
	// not attached is refused 404.
	Cluster string `json:"cluster,omitempty"`
	// Disk gives a sandbox with no Project a disk of its OWN, so what it writes
	// survives a park: small — the size its working directory always had — billed
	// as storage for as long as it exists, and deleted with the lease. Without it a
	// project-less sandbox keeps its files in the pod and loses them with it. A
	// project sandbox has the project's disk and ignores this.
	Disk bool `json:"disk,omitempty"`
	// Bearer is the person's OWN token, relayed by the sibling that received it, so
	// the sandbox signs in as them: it is exchanged for a token of the same subject
	// that lives no longer than the lease, exactly as the token on POST
	// /v1/sandboxes is. Empty leases a sandbox that holds no identity.
	//
	// Set it ONLY from a token cloud already validated on this person's own call:
	// cloud.CallerBearer on an HTTP request, or the verified cap of a ZAP stream.
	// Never a credential of ours and never an API key. IAM mints only for the
	// subject the token names, so whoever relays it lands that person and nobody
	// else; the sandbox signs a pod in only for the caller who leased it.
	//
	// It is on the wire because the plane carries this struct between processes,
	// the way TokenIn carries a bearer. It is never logged, never stored on the
	// sandbox row, and never echoed back: Leased has no field for it.
	Bearer string `json:"bearer,omitempty"`
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
	// ID names this computer for every later call — run, read, write, stop and end
	// all take it, and a LeaseIn carrying it resumes THIS sandbox instead of leasing
	// a second one. Minted here; a caller cannot choose it, and a resumed lease that
	// had expired comes back under a new one.
	ID string `json:"id"`
	// Class is what was actually leased, from the closed set LeaseIn.Class names:
	// exec | dev | desktop | browser | android. A request that named none leased an `exec`,
	// so this is where a caller learns which kind of computer it is holding, and it
	// is what Workdir below follows from.
	Class string `json:"class"`
	// Runtime is the boundary this sandbox GOT, which need not be the one asked
	// for — carried for the same reason Workdir is, that it is a fact only the
	// owner knows and a caller assuming it would be holding a second copy. Empty
	// is the node's default runtime, and a real answer.
	Runtime string `json:"runtime,omitempty"`
	// Status is where the pod stands, from the store's three: pending | running |
	// error. A lease that ANSWERS has already waited for the pod, so this reads
	// `running` — a start that failed is a 503 and no sandbox at all. Read it
	// anyway: exec refuses a sandbox that is not running, so anything else here is
	// the reason the next call will not work.
	Status string `json:"status"`
	// Workdir is the absolute directory this sandbox keeps files in, and what a
	// relative path in a later read, write or run resolves against — /work for dev,
	// desktop and android (the project volume's mount point), /mnt/data for exec
	// (the artifact directory the code tool tells the model to write to). A path
	// that climbs above it is refused rather than rewritten.
	Workdir string `json:"workdir"`
	// Cluster is the attached cluster this sandbox runs on, when one was named.
	// Empty is the home cluster.
	Cluster string `json:"cluster,omitempty"`
	// ExpiresAt is when the lease ends, Unix seconds. A resumed sandbox keeps the
	// expiry it had, so this is not always a fresh lease's.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// Carried says the lease is extended for as long as it works and its wallet
	// pays for the next hour. A lease that is not carried ends at ExpiresAt
	// whatever it is doing: a free one.
	Carried bool `json:"carried,omitempty"`
	// EndsAt is when the sandbox is ended whatever it is doing and however it is paid, Unix
	// seconds: the longest a sandbox may live (a day by default), counted from when it was
	// created and not from this lease, so a resumed sandbox has less than a fresh one. A caller
	// that works in it past this loses the work in it. Absent for a sandbox with no such
	// ceiling (a desktop).
	EndsAt int64 `json:"endsAt,omitempty"`
}

// RunIn runs one command in a sandbox. Argv is the honest form; Command is the
// convenience for a caller holding a shell line.
type RunIn struct {
	// ID is the sandbox to run in, from an earlier lease.
	ID string `json:"id"`
	// Argv is the program and its arguments, already split — the form no shell can
	// misread. Give this or Command, not both.
	Argv []string `json:"argv,omitempty"`
	// Command is a shell line, run by `sh -c`. Use it when a pipeline or a
	// redirection is the point, and Argv when it is not.
	Command string `json:"command,omitempty"`
	// Stdin is fed to the program on standard input. This is how bytes reach a file
	// without being quoted into a shell line: `cat > path` with the contents here
	// writes them exactly.
	Stdin string `json:"stdin,omitempty"`
	// Dir runs the command somewhere other than the sandbox's working directory,
	// which Leased.Workdir names.
	Dir string `json:"dir,omitempty"`
	// TimeoutSec bounds this ONE command, so a wedged program holds the caller for
	// its own timeout rather than for the whole lease.
	TimeoutSec int `json:"timeoutSec,omitempty"`
	// Blind is the set of secrets this command must never publish.
	//
	// It exists because output is redacted where it is PRODUCED or not at all. A
	// caller that scrubbed the returned result would still have streamed the
	// unredacted bytes into the session as they were written — to a durable event
	// store, an SSE feed and a chat thread — because the narration leaves the
	// sandbox by a different path from the result. Nothing downstream can take a
	// secret back out of a message that has already been delivered.
	//
	// The sandbox holds these only for the life of the one command, applies them
	// to every stream leaving it, and never logs or stores them.
	Blind []string `json:"blind,omitempty"`
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
	// Events says the command writes JSON lines to stdout, one event each, as an
	// agent harness's --json does: each is appended to Session as one `event`, in
	// the shape the command wrote it, and stderr is narrated as output.
	Events bool `json:"events,omitempty"`
}

// Ran is what a command produced. A non-zero ExitCode is DATA, not an error: the
// call succeeded and the program failed, and a caller has to be able to tell those
// apart.
type Ran struct {
	// ExitCode is the PROGRAM's own status — 0 succeeded, anything else is what it
	// returned, and a Command runs under `sh -c` so its shell's conventions apply.
	// A command that never reached an exit does not arrive here at all: a timeout
	// or a stop cancels the channel, and that is an error on the call rather than a
	// code of ours invented to fill this field.
	ExitCode int `json:"exitCode"`
	// Stdout is what the program wrote to standard output, collected whole rather
	// than streamed — to watch it arrive instead, name a RunIn.Session and read that
	// session's feed. Capped at 1 MiB, past which it ends in "[truncated at 1MiB]".
	// Every string named in RunIn.Blind is replaced by "[redacted]" before it gets
	// here, and before it reaches the session.
	Stdout string `json:"stdout"`
	// Stderr is standard error, kept apart from Stdout so a caller reading a
	// program's OUTPUT is not reading its diagnostics as data. Same 1 MiB cap, same
	// redaction. A program that failed usually says why here and nowhere else.
	Stderr string `json:"stderr"`
}

// PathIn names one path inside a sandbox. An empty Path means the sandbox's own
// working directory.
type PathIn struct {
	// ID is the sandbox to read from, from an earlier lease.
	ID string `json:"id"`
	// Path is read relative to the sandbox's working directory unless it is
	// absolute, and a path that climbs out of it is refused rather than rewritten.
	// Empty names the working directory itself, which lists it.
	Path string `json:"path,omitempty"`
}

// Blob is what a path IS: a file's bytes, or a directory's entries. One answer for
// both, because one command answers both and a caller that had to stat first would
// pay two round trips to learn what the first one already knew.
type Blob struct {
	// Path is the RESOLVED absolute path that was read — the caller's relative path
	// joined onto the sandbox's working directory (Leased.Workdir), so it names the
	// same file for a reader who does not know the class.
	Path string `json:"path"`
	// Dir says which of the two answers this is: true and the path is a directory,
	// so read Entries; false and it is a file, so read Data. Nothing else
	// distinguishes them — an empty file and an empty directory look alike here.
	Dir bool `json:"dir,omitempty"`
	// Data is the file's bytes, verbatim, base64 on the wire. Empty for a directory
	// and for an empty file alike; Dir is what tells those apart.
	Data []byte `json:"data,omitempty"`
	// Entries is a directory's contents as bare NAMES, not paths — one level, no
	// recursion, dotfiles included, "." and ".." excluded (`ls -1A`). Empty for a
	// file, and for an empty directory.
	Entries []string `json:"entries,omitempty"`
}

// WriteIn puts bytes at one path inside a sandbox, creating parents.
type WriteIn struct {
	// ID is the sandbox to write into, from an earlier lease.
	ID string `json:"id"`
	// Path is confined the same way PathIn.Path is. Missing parent directories are
	// created.
	Path string `json:"path"`
	// Data is the file's bytes, and replaces whatever was there.
	Data []byte `json:"data,omitempty"`
}

// Wrote reports the RESOLVED path and how much landed there. The path is resolved
// because a caller's path is relative far more often than not, and echoing back
// what it asked for would tell it nothing it did not already know.
type Wrote struct {
	// Path is where the bytes actually landed: the caller's path resolved against
	// the sandbox's working directory (Leased.Workdir), which is what a later read
	// or a shell line inside the sandbox has to name.
	Path string `json:"path"`
	// Bytes is how many bytes the file now holds. A write REPLACES the file, so this
	// is its whole length and not an amount appended, and 0 is a legitimate answer:
	// a WriteIn with no Data truncates the file to nothing.
	Bytes int `json:"bytes"`
}

// StopIn interrupts what a sandbox is running. It names the SANDBOX and not a
// command, because whoever is watching a run holds the sandbox's id and never the
// process id of whatever is inside it.
type StopIn struct {
	// ID is the sandbox to interrupt, from an earlier lease. Every command running
	// in it stops; the lease itself survives, so the checkout and the half-written
	// files are still there to read. Use EndIn to give the computer back.
	ID string `json:"id"`
}

// Stopped is how many commands the stop interrupted.
//
// Zero is an ANSWER and not a failure: a command that finished a moment ago is
// one there was nothing left to stop. The distinction a caller actually needs is
// "already over" versus "not yours", and the second is a 404 from the ordinary
// org lookup rather than a zero here.
type Stopped struct {
	// Stopped counts the commands that were still running and were interrupted.
	// Zero says the sandbox was idle, not that the stop failed — see above.
	Stopped int `json:"stopped"`
}

// ComputerIn performs one GUI or shell action inside a sandbox with a screen
// (desktop, browser, android) or runs `shell` on any sandbox.
type ComputerIn struct {
	// ID is the sandbox to act on, from an earlier lease. When omitted inside an
	// agent run, the agent runtime fills in the bot's own desktop computer.
	ID string `json:"id,omitempty"`
	// Action selects the computer-use step: screenshot, click, move, drag, scroll,
	// type, key, navigate, autofill, save_credential, wait, or shell.
	Action string `json:"action"`
	// Credential selects the Unified Vault login or secret by id, name, envVar, or
	// domain (e.g. "github.com") when Action is autofill, or provides an optional
	// display name override when Action is save_credential. Secret values never
	// travel in ComputerIn; autofill unseals from KMS and save_credential reads and
	// unlinks /dev/shm/.hanzo-credential.json inside the sandbox and seals directly
	// into KMS.
	Credential string `json:"credential,omitempty"`
	// Field selects which part of Credential to type when Action is autofill:
	// login (default: username, Tab, password), login_totp (username, Tab, password,
	// Tab, 6-digit TOTP), username, password, totp, or secret.
	Field string `json:"field,omitempty"`
	// Submit presses Return after autofilling the credential when Action is autofill.
	Submit bool `json:"submit,omitempty"`
	// X is the horizontal pixel coordinate on the screen (0..20000), used by click,
	// move, drag and scroll.
	X *int `json:"x,omitempty"`
	// Y is the vertical pixel coordinate on the screen (0..20000), used by click,
	// move, drag and scroll.
	Y *int `json:"y,omitempty"`
	// ToX is the destination horizontal pixel coordinate on the screen (0..20000)
	// when Action is drag.
	ToX *int `json:"toX,omitempty"`
	// ToY is the destination vertical pixel coordinate on the screen (0..20000)
	// when Action is drag.
	ToY *int `json:"toY,omitempty"`
	// DY is the scroll delta in notches (-50..50, non-zero): positive scrolls down,
	// negative scrolls up.
	DY *int `json:"dy,omitempty"`
	// Button names the mouse button for click: left (default), right or middle.
	Button string `json:"button,omitempty"`
	// Double sends a double-click instead of a single click when Action is click.
	Double bool `json:"double,omitempty"`
	// Text is the literal string typed into whichever window has keyboard focus
	// when Action is type (up to 4000 characters).
	Text string `json:"text,omitempty"`
	// Keys is one to ten space- or comma-separated key chords as xdotool names them
	// (Return, Tab, Escape, ctrl+l, alt+Left, ctrl+shift+t) when Action is key.
	Keys string `json:"keys,omitempty"`
	// URL is the http or https address opened in the desktop browser when Action is
	// navigate.
	URL string `json:"url,omitempty"`
	// Command is the shell command line run in the sandbox terminal when Action is
	// shell (up to 8000 characters).
	Command string `json:"command,omitempty"`
	// Seconds is how long to wait (0..10) for the screen to settle when Action is
	// wait.
	Seconds float64 `json:"seconds,omitempty"`
}

// ComputerOut is what one computer-use action answered.
type ComputerOut struct {
	// OK reports whether the GUI or shell action succeeded.
	OK bool `json:"ok"`
	// Text describes what happened or carries the shell command's exit code and
	// output.
	Text string `json:"text"`
	// Image holds the captured screen as a data:image/jpeg;base64,... URL when
	// Action was screenshot and succeeded; empty otherwise.
	Image string `json:"image,omitempty"`
}

// ParkIn parks a sandbox: its pod stops, and its disk and its record stay, so a
// later lease naming it resumes it with every file where it was. A parked sandbox
// costs its disk and no compute, and refuses run, read and write until then.
type ParkIn struct {
	// ID is the sandbox to park, from an earlier lease.
	ID string `json:"id"`
}

// EndIn ends a lease. Purge drops the project VOLUME as well and is opt-in,
// because ending a lease is reversible and deleting someone's uncommitted work is
// not.
type EndIn struct {
	// ID is the sandbox whose lease ends, from an earlier lease.
	ID string `json:"id"`
	// Purge deletes the project's DISK as well. It is opt-in because the disk holds
	// the only copy of the checkout: ending a lease is cheap and reversible,
	// deleting someone's uncommitted work is neither.
	Purge bool `json:"purge,omitempty"`
}

// AttachIn names the PROJECT a watcher has open, not a sandbox.
//
// The project is what the watcher knows: a browser holds a session, a session
// carries the project it is building, and the sandbox for that project is a pod
// whose id has already been replaced twice while the tab stayed open. Keying
// presence on the sandbox id would go stale at exactly the moment it matters —
// the reap — because the id changes and the person does not.
type AttachIn struct {
	Project string `json:"project"`
}

// StreamIn names the unix socket in a sandbox to open a stream to.
type StreamIn struct {
	// ID is the sandbox, from an earlier lease.
	ID string `json:"id"`
	// Path is the absolute path of the unix socket inside the sandbox whose
	// listener is the stream's far end. Nothing in a cell listens on the pod
	// network, so a stream has no port.
	Path string `json:"path"`
}

// Streamed is where the stream is.
type Streamed struct {
	// Socket is a unix socket path. Connect once, within ExpiresIn seconds; what
	// is written to it reaches the port and what the port writes is read from it,
	// untouched. It closes when either end does, or when the sandbox's lease ends.
	Socket string `json:"socket"`
	// ExpiresIn is how long the socket waits for its one connection, in seconds.
	ExpiresIn int `json:"expiresIn"`
}

// Attached is the sandbox the presence landed on, or an empty id when the project
// holds none.
//
// An empty id is a SUCCESSFUL answer. Watching a project with no live sandbox is
// the ordinary case — the user opened the page before the agent leased anything —
// and reporting it as an error would make every stream log a failure for doing
// nothing wrong.
type Attached struct {
	ID string `json:"id,omitempty"`
}

// PortsIn names the sandbox whose listening ports are asked for.
type PortsIn struct {
	// ID is the sandbox, from an earlier lease.
	ID string `json:"id"`
}

// Ports are the TCP ports something listens on inside a sandbox, each with the
// preview host that serves it (POST /v1/sandbox/{id}/preview opens one).
type Ports struct {
	// Ports are the listening ports, ascending. Never null.
	Ports []Port `json:"ports"`
}

// Port is one listening port.
type Port struct {
	// Port is the TCP port.
	Port int `json:"port"`
	// Host is the preview host that serves it, the same for as long as the
	// sandbox lives, parked or running.
	Host string `json:"host"`
}

// AttachEvery is how often a watcher must say it is still there, and it is
// declared HERE because it is the contract between two processes rather than a
// detail of either.
//
// It is the SSE stream's own heartbeat: agents already writes a `: ping` comment
// on this cadence to hold the connection open through proxies, and that ping is
// the moment the stream proves it still has a client — a dead client fails the
// flush. So presence rides the beat that already exists instead of a second timer
// invented beside it, and apps/sandbox derives how stale a stamp may be from this
// one number rather than keeping its own copy of agents' interval.
const AttachEvery = 25 * time.Second

// ---- coding: every client one autonomous coding run reaches across ------------
//
// A coding run (`@hanzo code: <repo> <task>`) is the most cross-app act in the
// estate: it opens an agents SESSION, resolves and gates an agents TARGET, reads
// git's CLONE URL, verifies in git that the pushed branch LANDED, and files the
// PR row in todo. Five apps, and since the fused monolith went away, five
// PROCESSES.
//
// It was written as five direct Go calls. Each of those reads a package global
// (`mounted`) that belongs to the OTHER process, so each answered the zero value
// in the one deployment that exists: CloneURL returned "" ("git is not
// available"), OpenSession returned "not mounted", VerifyRef reported the branch
// absent and the run failed CLOSED with no PR. The same shape that killed every
// @hanzo chat turn until the on-behalf-of run moved onto this plane.
//
// So the clients travel. The orchestration itself (apps/coding) is unchanged and
// stays ONE library — it always spoke to its collaborators through injected
// clients, which is exactly what made this a re-binding rather than a rewrite.
const (
	// AgentsSessionOpen / Event / Close are the live agent session a coding run
	// streams into: the durable record mission-control renders and the stream the
	// operator watches. Target, when set, tags the session with the MACHINE the
	// run was routed to (agents.OpenSessionOn), so a routed run shows up where it
	// is actually executing.
	AgentsSessionOpen  = "agents_session_open"
	AgentsSessionEvent = "agents_session_event"
	AgentsSessionClose = "agents_session_close"

	// AgentsSessionTag records what a session is the view of: the kind of run,
	// the repository and branches it works on, where it executes, and the pull
	// request it proposed. A run learns these at different moments — the branch
	// is named after the session, the pull request exists only at the end — so
	// the session is told each as it becomes true, and a field left empty is left
	// as it was.
	AgentsSessionTag = "agents_session_tag"

	// AgentsCodingSteer gives a coding run a new instruction from its person, or
	// continues a paused one. A run is one `dev exec` and cannot be told anything
	// mid-flight, so the run is stopped, its work so far is pushed to its own
	// branch, and a follow-up run starts from that branch with the task and the
	// instruction. It answers once the run has been told; the follow-up narrates
	// in the run's message.
	AgentsCodingSteer = "agents_coding_steer"

	// AgentsCodingStart starts a coding run as the CALLER, over the plane: the
	// same handler as POST /v1/agent/coding, for a surface that is not a request
	// — an issue handed to the agent from the board, or labelled on GitHub.
	AgentsCodingStart = "agents_coding_start"

	// AgentsCodingPublish builds one of the CALLER's projects from its repository
	// in a sandbox and puts what it built on the project's site. It is the agent
	// app's because the sandbox runner is.
	AgentsCodingPublish = "agents_coding_publish"

	// AgentsResolveTarget turns a human's `on <machine>` reference — a target id
	// or the friendly label the CLI registered — into a target id, org-scoped and
	// fail-closed. The Slack adapter parses the word; agents owns the registry
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

	// TodoAgentPR opens the native PR work item for a finished run.
	TodoAgentPR = "todo_agent_pr"

	// A coding run has no plane op, and that absence is a decision.
	//
	// There was a `coding_start`, so that the process holding the Slack adapter
	// could reach the engine in the agents process. Its only caller was the
	// `code:` prefix a human typed, and the whole point of deleting that prefix is
	// that a chat surface reaches the engine the way every other capability is
	// reached: the brain calls the TOOL. That path already runs through this
	// plane — AgentsRunOnBehalf carries the turn, the run's tool loop asks the
	// fleet's own MCP server, and the server routes the call to the agents
	// process — so a second op would be a second way in for exactly one caller,
	// and a caller with a private way in is a caller whose model never has to
	// choose.
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
	// Conversation is the app conversation the run is part of: the session is born
	// in its tree, its person's alone. It must be Actor's own; empty is a root.
	Conversation string `json:"conversation,omitempty"`
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

// SessionTagIn records run facts on a live session. Every field but the two
// that address the session is optional, and an empty one changes nothing.
type SessionTagIn struct {
	Org       string `json:"org"`
	SessionID string `json:"sessionId"`
	// Kind is what sort of run the session is the view of: "coding" for a coding
	// run.
	Kind string `json:"kind,omitempty"`
	// Repo is the repository the run works on: owner/name on GitHub, or the
	// forge's owner/name for a repository the forge holds.
	Repo string `json:"repo,omitempty"`
	// Base is the branch the run started from.
	Base string `json:"base,omitempty"`
	// Branch is the branch the run pushes its work to.
	Branch string `json:"branch,omitempty"`
	// Environment is where the run executes: "sandbox" for a Hanzo sandbox, or
	// the id of the org's machine it was routed to.
	Environment string `json:"environment,omitempty"`
	// PR is the address of the pull request the run proposed.
	PR string `json:"pr,omitempty"`
	// Mode is what the run may do: "build" writes its branch, "plan" and "setup"
	// only read.
	Mode string `json:"mode,omitempty"`
	// Sandbox is the id of the sandbox the run leased, the handle its terminal and
	// its screen open with.
	Sandbox string `json:"sandbox,omitempty"`
	// Project is the project the run was started on.
	Project string `json:"project,omitempty"`
}

// SessionCloseIn transitions a session out of running. Status is "done" or
// "error" — the two terminals a coding run has.
type SessionCloseIn struct {
	Org       string `json:"org"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	// Error explains why the session was closed error, when it was.
	Error string `json:"error,omitempty"`
}

// CodingAck is the answer of a coding client that either worked or returned an
// error. It carries a field because a void op answers 204 and a caller cannot
// tell 204 from "the op is not there".
type CodingAck struct {
	// OK means the operation was accepted; it does not imply that an associated coding run has finished.
	OK bool `json:"ok"`
	// Queued says the instruction was kept and the run has not taken it yet: it is in the
	// run's inbox, and is delivered when the run's owner reads it. Absent, the run has it.
	Queued bool `json:"queued,omitempty"`
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
	// Dir is the folder the run will work in there; when set it must be inside a
	// folder the machine's owner granted.
	Dir string `json:"dir,omitempty"`
	// ReadOnly asks whether the machine advertises a harness that can honour a
	// read-only run. A true this machine cannot honour is refused 422
	// read_only_unsupported before a session opens.
	ReadOnly bool `json:"readOnly,omitempty"`
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
	// Request makes a retry of this call the same call: a row already filed under it, in
	// the same project, is answered and no second is filed. Empty files a new row each time.
	Request string `json:"request,omitempty"`
}

// AgentPROut is the created row's stable handle.
type AgentPROut struct {
	Identifier string `json:"identifier"`
	ProjectKey string `json:"projectKey,omitempty"`
	Number     int    `json:"number,omitempty"`
}

// RouteRunIn is the NON-SECRET spec of a run to be executed on a registered
// machine. It carries no credential, so the engine can persist it: a run that
// needs one is minted it when the run is offered (Made).
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
	// ForgeActor is the forge login the run acts as. It is CLOUD-SIDE, like Actor:
	// the completion opens the pull request as that person and the executing
	// machine never sees it.
	ForgeActor string `json:"forgeActor,omitempty"`
	// Home is owner/name on GitHub for a run on a repository the forge does not
	// hold, and empty otherwise. The completion verifies and proposes there, on a
	// token it mints again as the run's person — admitted on their GitHub access
	// and IAM as they stand then, so nothing about their authority is carried.
	// Cloud-side, like Actor.
	Home string `json:"home,omitempty"`
	// Model and Effort are what the person asked the run to think with, handed to
	// the machine with the run; empty is the machine's own default.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Instructions are the person's own instructions for their runs, handed to
	// the machine for its harness to read as its user's; empty when they set none.
	Instructions string `json:"instructions,omitempty"`
	// Pull is a run that was asked to open a pull request for what it pushed. Unset,
	// its branch is pushed and verified and the completion proposes nothing.
	// Cloud-side, like Actor.
	Pull bool `json:"pull,omitempty"`
	// Made is a run in a workspace the platform made, whose repository the machine
	// holds no credential for: the run is given a deploy key of its own, a read-only
	// one when Read is set, when it is offered to the machine.
	Made bool `json:"made,omitempty"`
	Read bool `json:"read,omitempty"`
	// Dir is the granted folder the run works in on the machine; it crosses to
	// the machine. Empty is the machine's own choice.
	Dir string `json:"dir,omitempty"`
	// ReadOnly is a run whose harness may not write to the granted folder or checkout,
	// commit, push or open a pull request: it answers as the run's terminal status and
	// reports changed:false. It needs a machine advertising readOnlyRuns; one that does
	// not is refused before a run is enqueued, never silently run writable. This
	// contains one harness run; it is NOT a security boundary against the machine's
	// owner. It is NOT Read: Read marks a read-only DEPLOY KEY on a platform-made
	// workspace, where the run may still push with the machine's own identity.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// CodingStartIn asks the engine to begin one coding run.
//
// IT SAYS WHAT TO DO AND NEVER WHO TO BE, and that absence is the whole shape of
// the type. There is no org on it, no subject and no credential, so the two
// questions worth forging — whose balance, whose name — have no field to answer
// them with. The handler reads both off the caller it already proved
// (plugin/agents/coding.go), which is what makes the boundary structural: there
// is nothing to validate away, because there is nothing there.
//
// Each of the three left for the same reason one step later than the last.
//
// The org's agent git secret used to be read by the Slack surface and handed
// down through the request, so a token with write access to every repo in the
// org existed in the chat process, crossed a socket, and sat in a struct that
// three packages touched. None of them needed it; the ENGINE needs it, at the
// one moment it dispatches a sandbox, and reads it from KMS itself now.
//
// The subject — the person a run is ATTRIBUTED to, its session's actor and its
// PR's assignee — used to be filled in by whichever adapter had proved the
// account link. That was sound while every caller was an adapter we wrote. It
// stopped being sound the moment this op became an MCP tool, because the thing
// filling in a tool's arguments is a MODEL: a subject field is a model deciding
// whose run this is, and it could hand the work, the session and the assignment
// to a colleague who never asked for any of it. A field a model writes is not an
// identity, so there is no longer a field.
//
// What remains is entirely facts about the WORK, which is exactly what a model
// is entitled to decide.
type CodingStartIn struct {
	// Repo is what to work on: a repository's name, `owner/name` in the caller's
	// own org, or the site a repository serves (`hanzo.ai`), as the person said
	// it. The engine resolves it against the org's own repositories — a name that
	// matches none is refused with the nearest ones — and finds the clone URL and
	// the push credential from the org itself, so this says WHICH repository and
	// never how to reach it.
	//
	// A repository on GitHub is named by GitHub's address, `github.com/owner/name`
	// — or `owner/name` when the owner is not the org's own — and runs there
	// directly: it is admitted when the caller's own connected GitHub may push to
	// it, or when the caller administers the org and the org's installation holds
	// it, and refused otherwise.
	//
	// A repository the caller can read and not push to — a public repository, or
	// one their GitHub reads — is cloned read-only: the run works, commits in its
	// workspace, and pushes nothing; its change is kept as the run's artifacts.
	//
	// Empty runs in an empty workspace: a fresh git repository in the sandbox,
	// with nothing cloned and nothing pushed.
	Repo string `json:"repo"`
	// Prompt is the task, in the words you would use with a colleague who has the
	// checkout open. It is the whole instruction: there is no second field for
	// context, and a prompt that names files and the outcome it wants gets a run
	// that does not have to guess either.
	Prompt string `json:"prompt"`
	// Project scopes the run to one board's work when the org keeps more than one.
	// Empty is the org's default.
	Project string `json:"project,omitempty"`
	// Base is the branch to start from. Empty takes the repository's default. The
	// run never writes here — it writes the agent branch it answers with.
	Base string `json:"base,omitempty"`
	// After names a previous run's session, and starts this one from where that
	// one stopped instead of from the repository's default. It is how a follow-up
	// instruction — "now add tests for it" — builds on work already done rather
	// than beginning again on a fresh clone: the follow-up resumes the earlier
	// run's sandbox and works in its tree, files, installs and all, while that
	// sandbox is kept; one reaped by then is cloned again from the earlier branch.
	//
	// The follow-up writes the SAME branch the earlier run did, on top of its
	// commits, so one piece of work is one branch and one pull request however
	// many instructions it takes. It pushes without force, so it only ever adds to
	// that branch. When the earlier run was on another repository, or kept its work
	// local, there is no branch to carry on and the follow-up writes a new one.
	// Branch names the branch instead.
	//
	// A caller who already knows the branch may pass Base directly. Base wins if
	// both are given.
	After string `json:"after,omitempty"`
	// Conversation is the Dev conversation this run is part of (POST
	// /v1/agent/turns): one of the caller's own. The run is held in its tree, so it
	// is read, steered and written as the conversation is — by its person alone.
	// The app writes it; a run that carries on one in a conversation (After) is in
	// that conversation without naming it. Empty is a run of its own.
	Conversation string `json:"conversation,omitempty"`
	// Branch names the branch the run writes, as `agent/<name>`: pass the name
	// alone, or with the prefix. Runs write only under `agent/`, never to Base or
	// to the default branch, so the name is letters, digits, `.`, `_` and `-`.
	// Empty takes `agent/` and the first part of the run's session id, or the
	// earlier run's branch when After names one.
	//
	// A branch that already exists on the repository is carried on, not replaced:
	// the run starts from it and adds commits, and its pull request is the one it
	// already has.
	Branch string `json:"branch,omitempty"`
	// Pull is whether the run opens a pull request for what it pushed. Unset, it
	// pushes the branch and opens none: a pull request is the step after the work,
	// taken when it is ready, and a run resumed with `after` adds to the same branch.
	// True opens one when the run changed something.
	Pull bool `json:"pull,omitempty"`
	// Draft opens the pull request Pull asks for as a draft.
	Draft bool `json:"draft,omitempty"`
	// Assignees are the GitHub logins the pull request Pull asks for is assigned
	// to. GitHub repositories only; a forge repository ignores them.
	Assignees []string `json:"assignees,omitempty"`
	// Reviewers are the GitHub logins asked to review the pull request Pull asks
	// for. GitHub repositories only; a forge repository ignores them.
	Reviewers []string `json:"reviewers,omitempty"`
	// AgentRef names a configured agent to run as, which is how an org pins a
	// harness, a model and a prompt to a name. Empty runs the default agent.
	AgentRef string `json:"agentRef,omitempty"`
	// TargetID routes the run to a registered machine the org has claimed instead
	// of to a sandbox in our cluster. Empty runs it here, which is the usual case.
	TargetID string `json:"targetId,omitempty"`
	// Dir is the folder on that machine the run works in: an absolute path inside
	// one of the folders the machine's owner has let the web use (the target's
	// `folders`). A folder outside every grant is refused 403
	// `folder_not_granted`, and the machine checks the real path again before it
	// runs. It needs TargetID and is a run on no repository: a run works in a
	// repository or in a folder of the machine, not both. Empty is the machine's
	// own choice.
	Dir string `json:"dir,omitempty"`
	// TimeoutSeconds bounds the whole run. Unset takes the longest a run may take,
	// which is the longest a session may live (a day by default); a longer value
	// is that long. A run that hits the bound is stopped and reports what it had
	// done by then. A dev run's model credential is renewed for as long as it
	// lasts; a claude or codex run reads its credential once, so it ends a little
	// short of the four hours that credential is good for, saying so, with its
	// work kept on its branch.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Tool is which harness runs the prompt — dev | claude | codex | python |
	// node — and Desktop is whether the run needs a screen. Both are empty by
	// default, which is `dev` with no screen, and that default is what every
	// caller gets until it says otherwise.
	//
	// They are two fields because they are two questions. The harness decides
	// what argv starts; the screen decides which image carries an X server. A
	// caller may want claude WITH a browser it can see, and a single enum would
	// have made that combination unsayable.
	Tool string `json:"tool,omitempty"`
	// Desktop asks for a run with a SCREEN — an image carrying an X server — for a
	// task that has to drive a browser or another windowed program. False, the
	// default, is a headless checkout, which is what writing code needs.
	Desktop bool `json:"desktop,omitempty"`
	// Reply is WHERE THE RUN NARRATES ITSELF, when the surface that started it has
	// somewhere for it to talk. Empty means nobody is listening and the run simply
	// does not narrate — which is the app surface's case, because /v1/agent/coding
	// hands back a session id and the session stream is a better progress feed than
	// any message could be.
	//
	// IT IS NOT A FIELD A MODEL FILLS IN. A run asked for in a conversation is
	// dispatched with the address that conversation is happening at, written over
	// whatever arrived here (apps/agents/replyto.go). A model naming a channel is
	// a model naming a channel it was not spoken to in, so the value it chose is
	// discarded rather than argued with — which is the same rule that left the org
	// and the subject off this type entirely.
	Reply Reply `json:"reply,omitempty"`
	// Task is Prompt under the name a caller asking for work tends to reach for.
	// The two are one field: Prompt wins when both are given.
	Task string `json:"task,omitempty"`
	// Mode is what the run may do to the repository: `build` edits, commits and
	// pushes its branch; `plan` reads the repository and answers with a plan,
	// writing nothing — its sandbox is handed a credential that can only read and
	// no push step runs, and the plan arrives as the run's final status. `setup`
	// is a plan put to one question: its agent explores the checkout, installs and
	// checks what it finds, and ends with the codebase's environment, which is kept
	// as the proposal at /v1/environment/{repo} until someone saves it. Empty is
	// `build`. A plan or a setup runs in our sandbox only: a claimed machine clones
	// and pushes with its own credential, so one routed to a machine is refused.
	Mode string `json:"mode,omitempty"`
	// Model is the model the run's agent thinks with, by the id GET /v1/models
	// lists. Empty takes the platform's coding model. The dev and codex harnesses
	// take it; the others do not think with a model we name, so naming one for
	// them is refused.
	Model string `json:"model,omitempty"`
	// Effort is how hard that model reasons: low, medium or high. Empty takes the
	// model's own default. Taken by the dev and codex harnesses, like Model.
	Effort string `json:"effort,omitempty"`
	// Issue is the issue this run resolves: its number on Repo (`464` or `#464`),
	// or `owner/name#464` for an issue in another repository. Its pull request
	// says `Closes <issue>`, so merging the work closes the issue. Empty, or 0, is
	// a run no issue asked for.
	Issue Issue `json:"issue,omitempty"`
	// RequestID makes a retry of this request the same request: one run for this
	// caller whichever pod takes the retry and whatever restarts between, and the
	// retry answers what the first answered. The Idempotency-Key header is the same
	// value, and wins when both are sent. Optional.
	RequestID string `json:"requestId,omitempty" header:"Idempotency-Key"`
	// Instructions carries per-run developer instructions (such as active skills
	// and memories) separate from Prompt so the user's message stays clean;
	// optional.
	Instructions string `json:"instructions,omitempty"`
	// DeveloperInstructions is the explicit alias for Instructions passed to the
	// coding harness as developer_instructions; optional.
	DeveloperInstructions string `json:"developerInstructions,omitempty"`
	// ReadOnly asks for a routed run whose harness may not write to the granted folder
	// or checkout, commit, push or open a pull request. It needs a targetId: our own
	// sandbox already hands plan runs a read-only credential, so use `mode: plan` for
	// those. A target whose machine does not advertise readOnlyRuns is refused with
	// 422 read_only_unsupported before a session is opened. Absent is today's writable
	// run. This contains one harness run; it is NOT a promise about the machine itself.
	ReadOnly bool `json:"readOnly,omitempty"`
	// Steer redirects or queues this prompt into the session named by After when
	// that session is still running or paused, instead of failing 409. When After
	// has already finished, a normal follow-up run starts in its sandbox/branch.
	Steer bool `json:"steer,omitempty"`
	// Parallel forces a fresh isolated sandbox and branch even when After names an
	// active session or a session whose sandbox is occupied by another run.
	Parallel bool `json:"parallel,omitempty"`
}

// Issue names the issue a coding run resolves, as a person writes it: a number
// on the run's own repository, or owner/name#number for one in another. A JSON
// number is read as the first, so a caller that sends the bare number names the
// same issue it always did.
type Issue string

// UnmarshalJSON reads an issue from a JSON string or a JSON number.
func (i *Issue) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*i = Issue(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return errors.New("issue: a number, or owner/name#number")
	}
	*i = Issue(n.String())
	return nil
}

// Work is the coding run an issue asks for: its own words are the prompt, the
// run's proposal closes it, and the run narrates where the issue is discussed.
func Work(repo string, issue int, title, body string, to Reply) CodingStartIn {
	prompt := strings.TrimSpace(title)
	if b := strings.TrimSpace(body); b != "" {
		prompt += "\n\n" + b
	}
	in := CodingStartIn{Repo: repo, Prompt: prompt, Reply: to}
	if issue > 0 {
		in.Issue = Issue(strconv.Itoa(issue))
	}
	return in
}

// CodingSteerIn is a new instruction for a coding run, from a surface acting
// for the person it names. The org is the caller's, from the plane context, and
// the run must be Subject's own.
type CodingSteerIn struct {
	// Subject is the person the surface resolved, unqualified.
	Subject string `json:"subject"`
	// SessionID names the run. Empty finds the one Subject has RUNNING in Reply's
	// conversation, which is how a message said in a run's thread reaches it.
	SessionID string `json:"sessionId,omitempty"`
	// Reply is the conversation the instruction was said in.
	Reply Reply `json:"reply,omitempty"`
	// Text is the new instruction. Empty continues a paused run as it was asked.
	Text string `json:"text,omitempty"`
	// Update is the message the follow-up narrates into, by the id the platform
	// gave it: the one a button was pressed on, so the run carries on in place.
	// Empty keeps the stopped run's own message.
	Update string `json:"update,omitempty"`
	// RequestID makes a retry of this instruction the same instruction: said once
	// to the run, whichever pod takes the retry and whatever restarts between.
	// Optional.
	RequestID string `json:"requestId,omitempty"`
}

func (in *CodingSteerIn) UnmarshalJSON(b []byte) error {
	type plain CodingSteerIn
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*in = CodingSteerIn(p)
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
		return ""
	}
	if in.SessionID == "" {
		in.SessionID = pick("sessionId", "session", "session_id", "id")
	}
	if in.Text == "" {
		in.Text = pick("text", "message", "prompt", "instruction", "input", "content")
	}
	if in.RequestID == "" {
		in.RequestID = pick("requestId", "request_id")
	}
	return nil
}

// CodingStarted is the ACCEPTED run's handle. A coding run takes minutes, so the
// op answers when the run is admitted, not when it is finished: the session id is
// the handle every later question about the run is asked with, and the branch is
// the ref the run is permitted to write (nothing else — see the forge's ref
// policy). Progress streams at /v1/agent/sessions/stream?root=<sessionId> for
// every surface equally, which is why neither surface grew a progress endpoint of
// its own.
type CodingStarted struct {
	// SessionID is the run's handle: its durable record, and the root its live
	// progress streams under at /v1/agent/sessions/stream?root=<sessionId>. Every
	// later question about this run is asked with it.
	SessionID string `json:"sessionId"`
	// Branch is the ref the run will push its work to, and the ONLY ref it is
	// permitted to write. It exists before the work does, so it is safe to tell
	// somebody where to look while the run is still going.
	Branch string `json:"branch"`
	// Repo is the repository the run was admitted against, echoed back as the
	// engine resolved it.
	Repo string `json:"repo"`
	// Routed says the run went to one of the org's own registered machines rather
	// than to a sandbox in our cluster. False is the ordinary case.
	Routed bool `json:"routed,omitempty"`
	// TargetID names that machine when Routed is true, and is empty otherwise.
	TargetID string `json:"targetId,omitempty"`
	// Pull is the CLI command or instruction to pull this run locally, e.g. `hanzo pull <sessionId>`.
	Pull string `json:"pull,omitempty"`
	// Carried is how many of the instructions queued on the run `after` names went into
	// this run's task. Zero for a run that follows nothing, or one with nothing queued.
	Carried int `json:"carried,omitempty"`
	// Left is how many of them did not fit this run's task and are still queued on the
	// run it follows.
	Left int `json:"left,omitempty"`
	// Dropped is how many of them fit no task at all (more than the prompt limit) and were
	// set aside, not left queued.
	Dropped int `json:"dropped,omitempty"`
	// Note says what became of the queue in words, when there was one.
	Note string `json:"note,omitempty"`
	// Steered is true when `steer: true` redirected or queued this prompt into the
	// already-active session named by `after` rather than starting a second run.
	Steered bool `json:"steered,omitempty"`
	// Queued is true when a steered instruction was queued for the active session
	// rather than delivered immediately into an open turn.
	Queued bool `json:"queued,omitempty"`
}

// ---- a coding run's work, read from the forge --------------------------------

// CodingRunIn names a coding run by its session, for a read of what it did.
type CodingRunIn struct {
	// Session is the run's handle — the sessionId POST /v1/agent/coding answered
	// with — from the path.
	Session string `json:"session"`
}

// CodingMergeIn is the merge of a coding run's pull request: the run's session from
// the path, and the id that makes a retry the same merge.
type CodingMergeIn struct {
	// Session is the run's handle, from the path.
	Session string `json:"session"`
	// RequestID makes a retry of this merge the same merge, answered as the first
	// was. The Idempotency-Key header is the same value, and wins when both are sent.
	// Optional.
	RequestID string `json:"requestId,omitempty" header:"Idempotency-Key"`
}

// CodingPullIn is the pull request to open for a coding run: the run's session
// from the path, and what the caller says about the request. Every field but the
// session is optional.
type CodingPullIn struct {
	// Session is the run's handle, from the path.
	Session string `json:"session"`
	// Title is the pull request's title. Empty takes one from the run's task.
	Title string `json:"title,omitempty"`
	// Body is its description. Empty says who it is for, the task, the branch and
	// the run it came from.
	Body string `json:"body,omitempty"`
	// Base is the branch to propose into. Empty is the branch the run started from.
	Base string `json:"base,omitempty"`
	// Draft opens it as a draft, which a reviewer cannot merge until it is ready.
	Draft bool `json:"draft,omitempty"`
	// Assignees are the GitHub logins it is assigned to. GitHub repositories only;
	// a forge repository ignores them.
	Assignees []string `json:"assignees,omitempty"`
	// Reviewers are the GitHub logins asked to review it. GitHub repositories
	// only; a forge repository ignores them.
	Reviewers []string `json:"reviewers,omitempty"`
	// RequestID makes a retry of this request the same request, answered as the
	// first was. The Idempotency-Key header is the same value, and wins when both
	// are sent. Optional.
	RequestID string `json:"requestId,omitempty" header:"Idempotency-Key"`
}

// CodingPulled is the pull request a run's branch has, after it is opened.
type CodingPulled struct {
	// Number is the pull request's number in its repository.
	Number int64 `json:"number"`
	// URL is where it is read.
	URL string `json:"url"`
	// Base is the branch it proposes into.
	Base string `json:"base"`
	// Warning says what was asked and not done: the pull request is open, but an
	// assignee or reviewer GitHub refused (not a collaborator, say) was not added.
	Warning string `json:"warning,omitempty"`
}

// CodingExtendIn is how much longer a running coding run may work.
type CodingExtendIn struct {
	// Session is the run's handle, from the path.
	Session string `json:"session"`
	// Seconds is how much to add to the time the run is held to. It is added to
	// what the run has left, up to the longest a run may be given, so asking for
	// more than that is answered with what the run got.
	Seconds int `json:"seconds"`
	// RequestID makes a retry of this request the same request: the time is added
	// once, and the retry answers what the first answered. The Idempotency-Key
	// header is the same value, and wins when both are sent. Optional.
	RequestID string `json:"requestId,omitempty" header:"Idempotency-Key"`
}

// CodingExtended is a run's time after it was extended.
type CodingExtended struct {
	// Until is when the run is ended for time now, as an RFC 3339 time.
	Until string `json:"until"`
	// Seconds is how long the run has left.
	Seconds int `json:"seconds"`
}

// CodingPathIn names one path in a coding run's repository.
type CodingPathIn struct {
	// Session is the run's handle, from the path.
	Session string `json:"session"`
	// Path is repo-relative, from the query. Empty is the repository's root.
	Path string `json:"path"`
}

// CodingChanges is what a coding run changed: the commits on its branch, the net
// change of that branch against its base file by file, and its pull request. A
// branch the forge does not hold yet — a run still working, or one that changed
// nothing — answers with no commits, no files and no pull request.
type CodingChanges struct {
	// Repo is the run's repository on the forge, owner/name.
	Repo string `json:"repo"`
	// Base is the branch the run started from: the one it was given, or the
	// repository's default.
	Base string `json:"base"`
	// Head is the run's own branch. Empty for a plan or a setup, which write none.
	Head string `json:"head"`
	// Commits are the commits on head that base does not have, newest first.
	Commits []CodingCommit `json:"commits"`
	// Files is the net change head makes against its merge base with base, one
	// entry per file.
	Files []CodingFile `json:"files"`
	// Pull is the run's pull request, or null while it has none. The forge's own
	// is read whole; one proposed somewhere else — a repository the forge only
	// mirrors from GitHub — is the address and number the run recorded, with its
	// state, title and reviews empty.
	Pull *CodingPull `json:"pull"`
	// MoreCommits says the branch carries more commits than the newest 250 that
	// Commits lists.
	MoreCommits bool `json:"moreCommits,omitempty"`
	// MoreFiles says the change is larger than one read answers — past 8 MiB of
	// diff or 3000 files — so Files lists the files before that, and the last of
	// them is marked truncated with the counts of the part read.
	MoreFiles bool `json:"moreFiles,omitempty"`
	// Steers are the instructions said to the run while no pod held it, oldest
	// first, each queued until the run took it and delivered after. An instruction
	// that reached a run directly is not here: its answer was the delivery.
	Steers []CodingSteered `json:"steers,omitempty"`
}

// CodingSteered is one instruction said to a coding run that no pod held at the
// time, and whether the run has taken it yet.
type CodingSteered struct {
	// Text is what was said.
	Text string `json:"text"`
	// State is queued while the run has not taken the instruction, and delivered
	// once it has. An instruction queued for a run that ended stays queued: it was
	// never delivered, and a run started after it carries it. Dropped is one that fit
	// no task, which that start set aside.
	State string `json:"state"`
	// Said is when it was said, an RFC 3339 time in UTC.
	Said string `json:"said"`
	// Delivered is when the run took it, empty while it is queued.
	Delivered string `json:"delivered,omitempty"`
	// RequestID is the id the instruction was sent with, when it had one.
	RequestID string `json:"requestId,omitempty"`
}

// CodingArtifacts is what a coding run left: the files it changed and its patch,
// saved beside the run and kept after its sandbox is gone; the ports it served;
// and where its work went.
type CodingArtifacts struct {
	// Session is the run's handle.
	Session string `json:"session"`
	// Saved is when the artifacts were saved, RFC 3339 in UTC; empty while the run
	// has saved none.
	Saved string `json:"saved,omitempty"`
	// Artifacts are the run's artifacts, stored files first. Never null.
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one thing a coding run left.
type Artifact struct {
	// Name is the artifact's handle: a changed file's path in the workspace, or
	// `changes.patch` for the whole change; for a link, its kind and what it names.
	Name string `json:"name"`
	// Kind is file, patch, preview, pull or deploy. A file or a patch is stored,
	// and its bytes are read at GET /v1/agent/coding/{session}/artifacts/{name}.
	Kind string `json:"kind"`
	// Size is a stored artifact's length in bytes.
	Size int64 `json:"size,omitempty"`
	// SHA256 is a stored artifact's digest, hex.
	SHA256 string `json:"sha256,omitempty"`
	// URL is where a link points: a preview's host, a pull request, a deployment.
	URL string `json:"url,omitempty"`
	// Port is a preview's port in the run's sandbox, which
	// POST /v1/sandbox/{id}/preview opens while the sandbox is kept.
	Port int `json:"port,omitempty"`
	// Sandbox is the sandbox a preview is served from.
	Sandbox string `json:"sandbox,omitempty"`
}

// CodingArtifactIn names one stored artifact of a run.
type CodingArtifactIn struct {
	// Session is the run's handle, from the path.
	Session string `json:"session"`
	// Name is the artifact's name, from the path.
	Name string `json:"name"`
}

// CodingCommit is one commit of a run's branch.
type CodingCommit struct {
	// SHA is the full commit hash.
	SHA string `json:"sha"`
	// Message is the commit's subject — its first line only.
	Message string `json:"message"`
	// Author is the commit author's name as git recorded it; a run's own commits
	// are authored by its harness.
	Author string `json:"author"`
	// Date is the author date, RFC 3339 in UTC.
	Date string `json:"date"`
}

// CodingFile is one file a run's branch changes.
type CodingFile struct {
	// Path is where the file is on the branch — for a deleted file, where it was.
	Path string `json:"path"`
	// From is where a renamed file was. Omitted for every other status.
	From string `json:"from,omitempty"`
	// Status is added, modified, deleted or renamed.
	Status string `json:"status"`
	// Additions counts the lines the change adds, over the whole file whether or
	// not all of its patch is carried.
	Additions int `json:"additions"`
	// Deletions counts the lines the change removes, the same way.
	Deletions int `json:"deletions"`
	// Patch is the file's unified-diff hunks, from its first "@@". Empty for a
	// binary file and for a rename that moved no line.
	Patch string `json:"patch"`
	// Truncated marks a patch cut short: at 64 KiB for one file, or past 1 MiB of
	// patch across the change, when later files carry none. The counts are whole.
	Truncated bool `json:"truncated,omitempty"`
}

// CodingPull is a run's pull request, as the forge holds it.
type CodingPull struct {
	// Number is the pull request's number in its repository.
	Number int64 `json:"number"`
	// URL is where a person reads and merges it.
	URL string `json:"url"`
	// Title is its title.
	Title string `json:"title"`
	// State is open, closed or merged.
	State string `json:"state"`
	// Mergeable is the forge's verdict that the base takes the branch without
	// conflict.
	Mergeable bool `json:"mergeable"`
	// Reviews are its reviews, oldest first: the first 50.
	Reviews []CodingReview `json:"reviews"`
	// MoreReviews says it has more reviews than the first 50 that Reviews lists.
	MoreReviews bool `json:"moreReviews,omitempty"`
}

// CodingReview is one review of a run's pull request.
type CodingReview struct {
	// Author is the reviewer's forge login.
	Author string `json:"author"`
	// State is the verdict: approved, request_changes, comment, pending or
	// request_review.
	State string `json:"state"`
	// Body is what the reviewer wrote; empty for a bare verdict. At most 16 KiB
	// of it, and 256 KiB across the reviews listed.
	Body string `json:"body"`
	// Truncated marks a Body cut at either bound — or left empty past the second,
	// where the review keeps its author, verdict and time.
	Truncated bool `json:"truncated,omitempty"`
	// At is when it was submitted, RFC 3339 in UTC; empty for one that has not
	// been.
	At string `json:"at"`
}

// CodingTree is one directory of a run's repository.
type CodingTree struct {
	// Ref is the branch this was read at: the run's own once the forge holds it,
	// its base until then.
	Ref string `json:"ref"`
	// Entries are the directory's immediate children, directories first.
	Entries []CodingEntry `json:"entries"`
}

// CodingEntry is one child of a directory.
type CodingEntry struct {
	// Name is the entry's own name, no directory part.
	Name string `json:"name"`
	// Path is the entry's full repo-relative path.
	Path string `json:"path"`
	// Type is "tree" for a directory and "blob" for anything else a reader opens.
	Type string `json:"type"`
	// Size is a file's byte length; 0 for a directory.
	Size int64 `json:"size"`
}

// CodingBlob is one file of a run's repository, in the shape GET
// /v1/git/repos/{name}/blob answers, so one reader shows both.
type CodingBlob struct {
	// Path is the file's repo-relative path.
	Path string `json:"path"`
	// Size is the file's byte length in the repository, whatever was returned below.
	Size int64 `json:"size"`
	// Encoding is how Content is carried: "utf8" verbatim, or "base64".
	Encoding string `json:"encoding"`
	// Content is the file's bytes, empty when Truncated.
	Content string `json:"content"`
	// Binary marks content that is not text; it comes back base64.
	Binary bool `json:"binary"`
	// Truncated marks a file past the 1 MiB view cap. No content is sent.
	Truncated bool `json:"truncated"`
}

// CodingMerged is a coding run's pull request after a merge.
type CodingMerged struct {
	// Number is the pull request's number in its repository.
	Number int64 `json:"number"`
	// URL is where it is read.
	URL string `json:"url"`
	// State is merged.
	State string `json:"state"`
	// Base is the branch it was merged into.
	Base string `json:"base"`
}

// ---- code: fold a pushed tree into the org's index ---------------------------

// CodeIndex folds a repo's text files into the org's code-intelligence index.
//
// It is an OP rather than an injected function because the two planes are two
// PROCESSES. apps/git carried an `Indexer` seam for the composition root to fill
// with apps/code's client, and the fleet runs one binary per app — plugin/git
// links git, plugin/code links code, and nothing links both — so the seam could
// never be filled and push-indexing was inert wherever it shipped. That is the
// same failure charge_peer.go records for the tools charger, and it has the same
// answer: ask the process that owns the thing.
const CodeIndex = "code_index"

// IndexFile is one file of the pushed tree. Content is TEXT — the git side drops
// binaries and over-cap blobs before it sends, and the code side caps again,
// because a bound stated once is a bound the other end has to trust.
type IndexFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// IndexIn is a full-tree reconcile for one repo: everything that should be in
// the index after this push, so the receiver can prune what is no longer there.
//
// Payer rides beside Org because indexing EMBEDS, and an embedding is paid
// inference. Org is the tenant whose index this is; Payer is the account that pays
// for the work, written the way IAM writes the billing_account claim
// (`<kind>:<subject>`, account.Account.String) so the receiver parses it with
// account.Parse and can never mistake an org for a person.
type IndexIn struct {
	Org     string      `json:"org"`
	Payer   string      `json:"payer"`
	Project string      `json:"project"`
	Repo    string      `json:"repo"`
	Files   []IndexFile `json:"files"`
}

// Indexed reports what the reconcile did. Skipped counts files the receiver
// declined — a binary that slipped through, or one over its own cap — so a
// caller can tell "indexed nothing because there was nothing" from "indexed
// nothing because everything was refused".
type Indexed struct {
	Files   int `json:"files"`
	Chunks  int `json:"chunks"`
	Pruned  int `json:"pruned"`
	Skipped int `json:"skipped"`
}

// CodeSearch is the code leg of /v1/search: the org-scoped hybrid search over
// every repo the org has indexed, answered in the process that holds the index.
// An empty query asks only whether the leg is here.
const CodeSearch = "code_search"

// CodeSearchIn is one search of the caller org's code. The ORG is the caller's.
// Payer pays for the query's embedding, written as IndexIn.Payer is.
type CodeSearchIn struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
	Payer string `json:"payer,omitempty"`
}

// CodeSpan is one matching span of code. Score is not carried: the fused
// surface ranks by position.
type CodeSpan struct {
	Repo    string `json:"repo"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Kind    string `json:"kind,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
	Snippet string `json:"snippet"`
}

// CodeSpans is the spans that match, best first. Ready is false when the
// answering process mounts no code index. Capped is true when the semantic tier
// stopped at its scan cap with vectors left unread.
type CodeSpans struct {
	Ready  bool       `json:"ready"`
	Capped bool       `json:"capped,omitempty"`
	Spans  []CodeSpan `json:"spans"`
}

// SemanticIn is one vector search of the caller org's knowledge. The ORG is the
// caller's. Subject is the asking person as the validated principal names them,
// or "" for the org itself.
type SemanticIn struct {
	Query    string   `json:"query,omitempty"`
	Project  string   `json:"project,omitempty"`
	DocTypes []string `json:"docTypes,omitempty"`
	Limit    int      `json:"limit,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	Payer    string   `json:"payer,omitempty"`
}

// SemanticHit is one matching document. Text is the passage that matched, for a
// reranker to score.
type SemanticHit struct {
	DocType string  `json:"doctype"`
	Name    string  `json:"name"`
	Title   string  `json:"title"`
	Project string  `json:"project,omitempty"`
	URL     string  `json:"url,omitempty"`
	Text    string  `json:"text,omitempty"`
	Score   float64 `json:"score"`
}

// SemanticHits is the documents that match, best first. Ready is false when the
// answering process has no vector leg.
type SemanticHits struct {
	Ready bool          `json:"ready"`
	Hits  []SemanticHit `json:"hits"`
}

// CodeGraph files what a pushed tree states about its code into the org's
// graph: which module depends on which, which file defines which symbol, who
// owns which path, and what each decision record decides.
//
// It is the code plane's op because the parser is the code plane's. The tenant
// and project ride the caller.
const CodeGraph = "code_graph"

// Tree is one commit's text files and the two facts about the commit a graph
// assertion carries: when it was made and where it lives.
type Tree struct {
	Repo string `json:"repo"`
	// At is when the default branch took the commit, RFC 3339: the push, not
	// the commit's own clock, so a branch reset to an older commit states the
	// older tree from the reset on. Everything the tree states holds from it.
	At string `json:"at"`
	// Source is the commit's URL, the source every assertion names.
	Source string      `json:"source"`
	Files  []IndexFile `json:"files"`
}

// ---- graph: a source restating the whole of a scope -------------------------

// GraphRestate files everything one source now states about a scope, as of one
// instant. What it states and the graph does not hold begins then; what the
// graph holds and it no longer states ends then. Restating the same thing twice
// files nothing.
const GraphRestate = "graph_restate"

// Stated is one statement of a restatement. The instant, the source and the
// confidence are the restatement's; the evidence is the statement's own.
type Stated struct {
	Entity   string `json:"entity"`
	Relation string `json:"relation"`
	// Value is at most 2048 bytes, or 512 when it names an entity.
	Value    string `json:"value"`
	Names    bool   `json:"names,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// RestateMax is the most statements one restatement files. A source that
// states more is refused whole, since filing part of a whole would end the rest.
const RestateMax = 20_000

// Restate is the whole of what a source states about a scope.
//
// Scope is a key prefix and Relations the relations the source speaks for: the
// restatement is whole about every assertion under one of those relations whose
// entity, or whose edge's value, starts with Scope. An assertion under any other
// relation is never ended by it.
type Restate struct {
	Scope     string   `json:"scope"`
	Relations []string `json:"relations"`
	// At is the instant the statements hold from, RFC 3339.
	At     string   `json:"at"`
	Source string   `json:"source"`
	Stated []Stated `json:"stated"`
}

// Restated reports what a restatement filed, and why each statement it could
// not take was refused. Stale says a later restatement of the scope is already
// held, and nothing was filed: an older one arriving late cannot rewrite what a
// newer one said.
type Restated struct {
	Begun   int      `json:"begun"`
	Ended   int      `json:"ended"`
	Refused []string `json:"refused,omitempty"`
	Stale   bool     `json:"stale,omitempty"`
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

// AuditRecord is one record of the audit trail as it crosses the plane: what a
// process hands to the trail, and what the trail answers with once it is sealed.
//
// Org is the tenant the recorded action was taken IN — a fact the record states,
// not a tenant the call acts for. An append acts for no tenant; a read takes its
// tenant from the caller, and AuditQueryIn has no org.
type AuditRecord struct {
	// Seq is the chain position the trail sealed the record at. An append ignores it.
	Seq uint64 `json:"seq"`
	// Time is when the action happened, RFC3339Nano in UTC. Empty on an append is now.
	Time string `json:"time"`
	// Org is the tenant the action was taken in; empty on an unauthenticated request.
	Org string `json:"org"`
	// Sub is the acting user; empty for a machine principal or an anonymous request.
	Sub string `json:"sub"`
	// Email is the actor's validated address.
	Email string `json:"email,omitempty"`
	// Home is the org a platform admin acted FROM, present only on a cross-org action.
	Home string `json:"home,omitempty"`
	// Action is the event's name, e.g. "ai.limits.credits".
	Action string `json:"action"`
	// Resource is the kind of thing acted on.
	Resource string `json:"resource"`
	// ResourceID is the instance acted on.
	ResourceID string `json:"resourceId,omitempty"`
	// AuthMethod is the credential the actor presented.
	AuthMethod string `json:"authMethod,omitempty"`
	// IsAdmin is the validated platform-SuperAdmin bit at decision time.
	IsAdmin bool `json:"isAdmin"`
	// Result is "success", "deny" or "error".
	Result string `json:"result"`
	// Status is the HTTP status the caller received.
	Status int `json:"status"`
	// Reason explains a deny or an error.
	Reason string `json:"reason,omitempty"`
	// SourceIP is the client address the edge resolved.
	SourceIP string `json:"sourceIp,omitempty"`
	// UserAgent is the client the request announced itself as.
	UserAgent string `json:"userAgent,omitempty"`
	// RequestID ties the record to the request log.
	RequestID string `json:"requestId,omitempty"`
	// Method is the HTTP verb of a request-sourced record.
	Method string `json:"method,omitempty"`
	// Path is the request's route, credential-shaped segments already replaced.
	Path string `json:"path,omitempty"`
	// Before is the changed thing before the action, redacted JSON.
	Before json.RawMessage `json:"before,omitempty"`
	// After is the changed thing after the action, redacted JSON.
	After json.RawMessage `json:"after,omitempty"`
	// PrevHash is the hash of the record before this one. An append ignores it.
	PrevHash string `json:"prevHash"`
	// Hash is this record's hash. An append ignores it.
	Hash string `json:"hash"`
	// ActSub is the IAM subject of the platform operator behind a support session
	// (an IAM impersonation); empty for an actor acting as themselves. It and the
	// two below are appended last: the wire is ZAP, and a field is its offset.
	ActSub string `json:"actSub,omitempty"`
	// ActOwner is the org of that operator's IAM row: "admin" for a SuperAdmin.
	ActOwner string `json:"actOwner,omitempty"`
	// ActName is that operator's IAM username.
	ActName string `json:"actName,omitempty"`
	// Agent is who acted for the actor through a delegated token — the deployment
	// that ran their work in a sandbox; empty for an actor acting as themselves.
	// Appended last for the reason the three above are.
	Agent string `json:"agent,omitempty"`
}

// AuditQueryIn narrows a read of the trail within the caller's org. Every field
// is optional, and a blank one is not applied.
type AuditQueryIn struct {
	// Sub is one actor.
	Sub string `json:"sub,omitempty"`
	// Home is the org a platform admin acted from.
	Home string `json:"home,omitempty"`
	// Impersonated keeps only cross-org actions.
	Impersonated bool `json:"impersonated,omitempty"`
	// Action is one action name.
	Action string `json:"action,omitempty"`
	// Actions is any one of several action names.
	Actions []string `json:"actions,omitempty"`
	// Resource is one resource kind.
	Resource string `json:"resource,omitempty"`
	// ResourceID is one resource instance.
	ResourceID string `json:"resourceId,omitempty"`
	// Result is "success", "deny" or "error".
	Result string `json:"result,omitempty"`
	// Since is the inclusive lower time bound, RFC3339Nano.
	Since string `json:"since,omitempty"`
	// Until is the inclusive upper time bound, RFC3339Nano.
	Until string `json:"until,omitempty"`
	// Limit is rows per page: 100 by default, 1000 at most.
	Limit int `json:"limit,omitempty"`
	// Offset skips that many matching rows.
	Offset int `json:"offset,omitempty"`
	// Q matches action, actor, resource and org as a case-insensitive substring.
	Q string `json:"q,omitempty"`
	// Before starts the page at the records older than this chain position.
	Before uint64 `json:"before,omitempty"`
}

// AuditQueryOut is one page of the trail, newest first, and how many records
// match across every page.
type AuditQueryOut struct {
	// Records is the page. Empty, never absent, when nothing matches.
	Records []AuditRecord `json:"records"`
	// Total counts every matching record, ignoring the page.
	Total int `json:"total"`
}

// ---- ai.rates — what an Enso model costs ------------------------------------

// RatesIn asks what each Enso model costs. Models names ids to price beside the
// tiers. Admin asks for what names upstreams — how each price was set, each
// model's route and the day's ledger — and is set only for a SuperAdmin.
type RatesIn struct {
	// Models are ids to price beside the tiers: a router id among them (enso,
	// enso-auto, hanzo/enso) is quoted at the price of the tier a request that states
	// no depth is served at, as /v1/models lists it, and any other id at the row the
	// model API bills it from. An id no row prices is not quoted.
	Models []string `json:"models,omitempty"`
	// Admin asks for how each price was set, each route and the ledger: a
	// SuperAdmin's read alone.
	Admin bool `json:"admin,omitempty"`
}

// Price is a price in US dollars a million tokens, by class, each an exact
// decimal. A cache class a price does not state is "".
type Price struct {
	// Input is a fresh prompt token's price, dollars a million.
	Input string `json:"input"`
	// Output is a completion token's price, dollars a million.
	Output string `json:"output"`
	// CacheRead is a prompt token read from the provider's cache, dollars a
	// million; "" where the price states none.
	CacheRead string `json:"cacheRead,omitempty"`
	// CacheWrite is a prompt token written to the provider's cache, dollars a
	// million; "" where the price states none.
	CacheWrite string `json:"cacheWrite,omitempty"`
}

// RateArm is one rung of a model's route: its upstream, the provider whose
// account runs it, the provider's own price, and the token classes in which it
// costs us more than the model bills.
type RateArm struct {
	// Upstream is the model the arm runs.
	Upstream string `json:"upstream"`
	// Provider is the account the arm runs on, as the family's catalog names it.
	Provider string `json:"provider"`
	// Kind is how the provider is reached: direct (its own API, on a key Hanzo or
	// the org holds), family (a Hanzo family service), do (DigitalOcean's
	// inference) or openrouter (the fallback), asked in that order.
	Kind string `json:"kind"`
	// Fee is a reseller's fee on the provider's price, percent: part of the cost.
	Fee string `json:"fee"`
	// List is the provider's own price.
	List Price `json:"list"`
	// Long is the provider's price for a prompt past Above tokens, where it charges
	// one.
	Long *Price `json:"long,omitempty"`
	// Above is the prompt length Long applies past, tokens.
	Above int `json:"above,omitempty"`
	// Over names the token classes (input, output, cache read, cache write) in which
	// the arm costs us more than the model bills; empty when it fits under the price.
	Over []string `json:"over,omitempty"`
}

// Quote is one Enso model's price: one fixed price a million tokens, the same at
// every reasoning depth and whichever arm answers, which the model lists, bills
// and holds at.
type Quote struct {
	// Model is the model priced.
	Model string `json:"model"`
	// Price is what a million of its tokens bill, class by class.
	Price Price `json:"price"`
	// Tier is, for a router id, the tier whose Price it is quoted at: the one a
	// request that states no depth is served at. A router bills the tier that
	// answers. "" for a tier.
	Tier string `json:"tier,omitempty"`
	// Typical is the upstream the model typically routes to, List that upstream's
	// list price and Multiple the multiple of it Price is: how the price was set.
	// They, Arms and Refused name upstreams, so a SuperAdmin's read alone carries
	// them.
	Typical  string `json:"typical,omitempty"`
	List     *Price `json:"list,omitempty"`
	Multiple string `json:"multiple,omitempty"`
	// Arms are the model's route as last read, each held to Price.
	Arms []RateArm `json:"arms,omitempty"`
	// Refused is why the model is not served — its route cannot be read, holds no
	// arm, or holds an arm that costs more than the model bills — "" while it is.
	Refused string `json:"refused,omitempty"`
}

// RateRoute is one model's calls on one arm through one provider's account over
// the day, nano-dollars.
type RateRoute struct {
	// Model is the model asked.
	Model string `json:"model"`
	// Served is the upstream that answered.
	Served string `json:"served"`
	// Vendor is the provider whose account ran it.
	Vendor string `json:"vendor"`
	// Requests is how many answers the arm gave.
	Requests int `json:"requests"`
	// Failovers is how many times the arm failed first.
	Failovers int `json:"failovers"`
	// Uncosted is how many answers stated no cost, which Margin leaves out.
	Uncosted int `json:"uncosted"`
	// Cost is what the answers cost us, the reseller's fee included.
	Cost int64 `json:"costNano"`
	// Billed is what they billed.
	Billed int64 `json:"billedNano"`
	// Margin is what the answers whose cost is known billed past their cost.
	Margin int64 `json:"marginNano"`
	// Loss is what they cost past what they billed.
	Loss int64 `json:"lossNano"`
}

// RateLedger is what the family's calls did over the last day, in all and by
// model and arm, nano-dollars: a report of the margin the fixed prices made.
type RateLedger struct {
	// Since is when the day began, unix seconds.
	Since int64 `json:"since"`
	// Until is when it ended, unix seconds.
	Until int64 `json:"until"`
	// Requests is how many answers.
	Requests int `json:"requests"`
	// Failovers is how many arms failed before an answer.
	Failovers int `json:"failovers"`
	// Fallbacks is how many answers a reseller's credit bought (OpenRouter, the
	// path a call takes when no provider key of Hanzo's or the org's serves it).
	Fallbacks int `json:"fallbacks"`
	// FallbackShare is Fallbacks over Requests.
	FallbackShare float64 `json:"fallbackShare"`
	// Uncosted is how many answers stated no cost, which Margin leaves out.
	Uncosted int `json:"uncosted"`
	// Losses is how many answers cost us more than they billed.
	Losses int `json:"losses"`
	// Loss is what those cost past what they billed.
	Loss int64 `json:"lossNano"`
	// Cost is what the answers cost us, the reseller's fee included.
	Cost int64 `json:"costNano"`
	// Billed is what the answers billed.
	Billed int64 `json:"billedNano"`
	// Margin is what the answers whose cost is known billed past their cost.
	Margin int64 `json:"marginNano"`
	// Routes is the same by model, arm and vendor.
	Routes []RateRoute `json:"routes"`
}

// Rates is every Enso model's price and the models served free.
type Rates struct {
	// Quotes is each priced model's one price.
	Quotes []Quote `json:"quotes"`
	// Free names the family's models served at no charge.
	Free []string `json:"free,omitempty"`
	// Ledger is the last day's, set for a SuperAdmin alone.
	Ledger *RateLedger `json:"ledger,omitempty"`
	// Error is why the ledger could not be read, "" when it was.
	Error string `json:"error,omitempty"`
}

// RoutingIn names the family whose routing is read: "enso" or "zen".
type RoutingIn struct {
	Family string `json:"family"`
}

// Routing is a family's routing catalog as the family serves it now.
type Routing struct {
	// Family is the family read.
	Family string `json:"family"`
	// Catalog is the family's admin catalog (its models, each one's route of
	// upstreams, providers and costs), JSON; empty when the family could not be read.
	Catalog []byte `json:"catalog,omitempty"`
	// Version is the newest applied version's id, 0 before the first.
	Version int64 `json:"version"`
	// Drift says the family serves something other than its newest version.
	Drift bool `json:"drift"`
	// Redeployed says the family serves a deployment the newest version was not
	// made over (a new release of its file), which is left standing.
	Redeployed bool `json:"redeployed"`
	// Error is why the family could not be read, "" when it was.
	Error string `json:"error,omitempty"`
}

// RoutingApplyIn is an edited catalog for a family, made from a version.
type RoutingApplyIn struct {
	// Family is the family edited.
	Family string `json:"family"`
	// Catalog is the whole edited admin catalog, JSON.
	Catalog []byte `json:"catalog"`
	// Base is the version the edit was made from, 0 before the first; an apply
	// made from any but the newest is refused.
	Base int64 `json:"base"`
	// Note is why, kept with the version.
	Note string `json:"note"`
	// Dry proposes the edit: its diff and the guard's verdict, nothing applied.
	Dry bool `json:"dry,omitempty"`
}

// RoutingApplied is what an edit did, or, dry, would do.
type RoutingApplied struct {
	// Version is the version recorded, 0 for a proposal.
	Version int64 `json:"version"`
	// Base is the version the edit was made from (a proposal: the newest).
	Base int64 `json:"base"`
	// Diff is the line diff against what the family served.
	Diff string `json:"diff"`
	// Refused is why the family's price guard would refuse a model the catalog
	// serves, "" when every priced model fits. A proposal only: an apply the guard
	// would refuse is refused.
	Refused string `json:"refused,omitempty"`
}

// LibraryPutIn is one thing a person made, to keep in the Library.
type LibraryPutIn struct {
	// Kind is image, video, audio, document, slides, type, model, code or file;
	// empty derives it from Mime.
	Kind string `json:"kind"`
	// Source is the product that made it: chat, dev, studio or workflow.
	Source string `json:"source" validate:"required"`
	// Name is what it is called, a file name.
	Name string `json:"name" validate:"required"`
	// Mime is its media type.
	Mime string `json:"mime" validate:"required"`
	// Data is the bytes, at most 10 MiB. Empty on a reservation.
	Data []byte `json:"data,omitempty"`
	// Bytes is the size a reservation will upload.
	Bytes int64 `json:"bytes,omitempty"`
	// Preview is a still that shows it (a video's poster), at most 2 MiB.
	Preview []byte `json:"preview,omitempty"`
	// Ref is what made it: a conversation, a Dev run, a Studio run.
	Ref string `json:"ref,omitempty"`
	// Origin is the app path back to what made it: /chat?thread=…, /dev/<run>.
	Origin string `json:"origin,omitempty"`
	// Project is a project slug of the org, or empty.
	Project string `json:"project,omitempty"`
	// Key makes the write idempotent: the same owner, source and key answer the
	// item already made.
	Key string `json:"key,omitempty"`
	// Width and Height are pixels; Seconds is a duration; Pages a page count.
	Width   int64   `json:"width,omitempty"`
	Height  int64   `json:"height,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Pages   int64   `json:"pages,omitempty"`
}

// LibraryItem is one thing in the Library, as a reader sees it.
type LibraryItem struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Source   string  `json:"source"`
	Name     string  `json:"name"`
	Mime     string  `json:"mime"`
	Bytes    int64   `json:"bytes"`
	Width    int64   `json:"width"`
	Height   int64   `json:"height"`
	Seconds  float64 `json:"seconds"`
	Pages    int64   `json:"pages"`
	Owner    string  `json:"owner"`
	Mine     bool    `json:"mine"`
	Shared   bool    `json:"shared"`
	Project  string  `json:"project"`
	Ref      string  `json:"ref"`
	Origin   string  `json:"origin"`
	URL      string  `json:"url"`
	Download string  `json:"download"`
	Preview  string  `json:"preview"`
	Expires  string  `json:"expires"`
	Created  string  `json:"created"`
	Updated  string  `json:"updated"`
	// By is the owner as a reader names them: the name, else the address, the
	// identity boundary carried when the item was made.
	By string `json:"by"`
	// Upload is where a reserved item's bytes go, on the reservation response
	// before it is committed.
	Upload *LibraryUpload `json:"upload,omitempty"`
}

// LibraryUpload is where a reservation's bytes go: one presigned PUT.
type LibraryUpload struct {
	URL string `json:"url"`
	// Headers are the ones the PUT must carry.
	Headers []Field `json:"headers"`
	Expires string  `json:"expires"`
}

// LibraryReserved is a reserved item and where its bytes go.
type LibraryReserved struct {
	Item   LibraryItem   `json:"item"`
	Upload LibraryUpload `json:"upload"`
}

// LibraryRef names one item.
type LibraryRef struct {
	ID string `json:"id" validate:"required"`
}

// JobTrackIn is one run of a product's background work, as it stands now.
type JobTrackIn struct {
	// Product is chat, dev, studio or workflow.
	Product string `json:"product" validate:"required"`
	// Kind is the product's word for the work: render, video, run.
	Kind string `json:"kind"`
	// Title is what a person sees.
	Title string `json:"title"`
	// Ref is the product's own id for the run; with Product it names the job.
	Ref string `json:"ref" validate:"required"`
	// Origin is the app path back to it.
	Origin string `json:"origin,omitempty"`
	// State is scheduled, running, succeeded, failed or cancelled.
	State string `json:"state" validate:"required"`
	// Progress is 0 to 1, or nil for unknown.
	Progress *float64 `json:"progress,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Error    string   `json:"error,omitempty"`
	// Outputs are the Library items it made.
	Outputs []string `json:"outputs,omitempty"`
	// Cost is what it cost, in cents, or nil while unknown.
	Cost *int64 `json:"cost,omitempty"`
}

// Job is one run of anything that runs without its person watching: an
// automation's run, a workflow's, or a product's tracked background run.
type Job struct {
	ID         string   `json:"id"`
	Product    string   `json:"product"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	State      string   `json:"state"`
	Status     string   `json:"status"`
	Progress   *float64 `json:"progress"`
	Automation *string  `json:"automation"`
	Session    *string  `json:"session"`
	Ref        string   `json:"ref"`
	Origin     string   `json:"origin"`
	Summary    string   `json:"summary"`
	Error      string   `json:"error"`
	Outputs    []string `json:"outputs"`
	Cost       *int64   `json:"cost"`
	Started    string   `json:"started"`
	Finished   *string  `json:"finished"`
	Updated    string   `json:"updated"`
}

// BaseImportIn names what to grow the caller's org Base by: a module Base ships
// ("crm") or an import document of collections, exactly one of the two. The org
// is the caller's; nothing here can name another.
type BaseImportIn struct {
	// Module is a module Base ships, by name.
	Module string `json:"module,omitempty"`
	// Collections is a JSON array of collections in the shape Base's own import
	// takes. A relation may name its collection by name. Only ordinary base
	// collections are accepted: no auth or view collection, nothing named with a
	// leading underscore.
	Collections json.RawMessage `json:"collections,omitempty"`
}

// BaseGrowth is what a grow changed in the caller's org Base.
type BaseGrowth struct {
	// Created names the collections that did not exist and now do.
	Created []string `json:"created"`
	// Grown lists, per collection, what was added to one that existed: fields,
	// select values ("field:value") and indexes ("index:name").
	Grown []BaseNames `json:"grown"`
	// Kept lists, per collection, the fields the import names with a different
	// type than they have, which were left exactly as they are.
	Kept []BaseNames `json:"kept"`
}

// BaseNames is one collection and some names in it.
type BaseNames struct {
	// Collection is the collection, by name.
	Collection string `json:"collection"`
	// Names are the fields, values or indexes.
	Names []string `json:"names"`
}

// BaseUpsertIn is a batch of records for one collection of the caller's org
// Base.
type BaseUpsertIn struct {
	// Collection is the collection, by name.
	Collection string `json:"collection" validate:"required"`
	// Key is the field a record without an id is matched on, such as an
	// activity's source_id, so writing the same record twice updates it rather
	// than adding a second. Empty means only an id matches.
	Key string `json:"key,omitempty"`
	// Source is where the writes come from, as an actor names it: import, email,
	// calendar, webhook, workflow, agent or application (the default). The
	// records' actor fields and their history record it with the caller.
	Source string `json:"source,omitempty"`
	// Records are the records, each a JSON object of field values. A field a
	// record does not name keeps its value on an update.
	Records []json.RawMessage `json:"records" validate:"required"`
}

// BaseUpserted says what became of each record, in the order they were sent.
type BaseUpserted struct {
	// Records are the written records, one per record sent.
	Records []BaseWritten `json:"records"`
}

// BaseWritten is one written record.
type BaseWritten struct {
	// ID is the record's id.
	ID string `json:"id"`
	// Created is true for a record the write created, false for one it updated.
	Created bool `json:"created"`
}

// BaseDeleteIn names records of one collection of the caller's org Base.
type BaseDeleteIn struct {
	// Collection is the collection, by name.
	Collection string `json:"collection" validate:"required"`
	// IDs are the records' ids, at most 500.
	IDs []string `json:"ids" validate:"required"`
	// Source is who the delete is recorded as having been made by, as an upsert's is.
	Source string `json:"source,omitempty"`
}

// BaseDeleted is what a delete removed.
type BaseDeleted struct {
	// Deleted counts the records removed; an id that named none is not counted.
	Deleted int `json:"deleted"`
}

// BaseGetIn names one record of the caller's org Base.
type BaseGetIn struct {
	// Collection is the collection, by name.
	Collection string `json:"collection" validate:"required"`
	// ID is the record's id.
	ID string `json:"id" validate:"required"`
	// Expand names relations to expand, as Base's expand parameter does.
	Expand string `json:"expand,omitempty"`
}

// BaseRecord is one record, as a JSON object of its fields.
type BaseRecord struct {
	// Record is the record's fields, hidden ones included.
	Record json.RawMessage `json:"record"`
}

// BaseListIn asks for a page of one collection of the caller's org Base.
type BaseListIn struct {
	// Collection is the collection, by name.
	Collection string `json:"collection" validate:"required"`
	// Filter is a Base filter expression.
	Filter string `json:"filter,omitempty"`
	// Where is a structured filter, ANDed with Filter.
	Where json.RawMessage `json:"where,omitempty"`
	// Sort is Base's sort expression ("-created,name").
	Sort string `json:"sort,omitempty"`
	// Expand names relations to expand.
	Expand string `json:"expand,omitempty"`
	// Page is 1-based; PerPage is at most 1000 (30 when zero).
	Page    int `json:"page,omitempty"`
	PerPage int `json:"perPage,omitempty"`
	// SkipTotal leaves TotalItems and TotalPages at -1 and saves the count.
	SkipTotal bool `json:"skipTotal,omitempty"`
}

// BaseRecords is a page of records.
type BaseRecords struct {
	// Items are the page's records, each a JSON object of its fields.
	Items []json.RawMessage `json:"items"`
	// Page and PerPage say which page this is.
	Page    int `json:"page"`
	PerPage int `json:"perPage"`
	// TotalItems and TotalPages count the whole filtered collection, or are -1
	// when the caller skipped the count.
	TotalItems int `json:"totalItems"`
	TotalPages int `json:"totalPages"`
}

// IntegrationsConnectIn asks the provider process to start an OAuth flow or
// verify and seal a credential for one connector in the caller's org.
type IntegrationsConnectIn struct {
	// Provider is the connector id ("x", "linkedin", "youtube", "bluesky", ...).
	Provider string `json:"provider"`
	// Credential selects the credential/apikey verification path when non-nil.
	Credential *string `json:"credential,omitempty"`
	// AccountID is the optional provider account hint for credential verification.
	AccountID string `json:"accountId,omitempty"`
	// Fields are named credential form inputs for multi-field connectors.
	Fields []Field `json:"fields,omitempty"`
	// Return is the registered return URL the OAuth callback redirects back to.
	Return string `json:"return,omitempty"`
}

// IntegrationsConnectOut is what the provider process returns for a connect request.
type IntegrationsConnectOut struct {
	// AuthorizeURL is set on the 3-legged OAuth path.
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	// Connected is true when a credential connector verified and sealed immediately.
	Connected bool `json:"connected,omitempty"`
	// Provider is the connector id.
	Provider string `json:"provider,omitempty"`
	// Account is the verified human account label.
	Account string `json:"account,omitempty"`
	// ExternalID is the verified platform account id.
	ExternalID string `json:"externalId,omitempty"`
	// Scopes are the granted permissions.
	Scopes []string `json:"scopes,omitempty"`
}

// PlaneChannel is one connected social channel on the social plane.
type PlaneChannel struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name,omitempty"`
	Handle        string `json:"handle"`
	Profile       string `json:"profile,omitempty"`
	Picture       string `json:"picture,omitempty"`
	Status        string `json:"status"`
	ExternalID    string `json:"externalId,omitempty"`
	GroupID       string `json:"groupId,omitempty"`
	GroupName     string `json:"groupName,omitempty"`
	RefreshNeeded bool   `json:"refreshNeeded,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// PlaneChannelFilter filters the org's social channels.
type PlaneChannelFilter struct {
	Provider string `json:"provider,omitempty"`
	Group    string `json:"group,omitempty"`
	Limit    string `json:"limit,omitempty"`
}

// PlaneChannels is a list of channels.
type PlaneChannels struct {
	Data []PlaneChannel `json:"data"`
}

// PlaneEmptyIn is an empty plane request body when the caller's org rides the call.
type PlaneEmptyIn struct{}

// PlaneChannelGroup summarizes one employee or brand channel group.
type PlaneChannelGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Channels int    `json:"channels"`
}

// PlaneChannelGroups lists channel groups in the caller's org.
type PlaneChannelGroups struct {
	Data []PlaneChannelGroup `json:"data"`
}

// PlaneChannelProvider reports publish-readiness and OAuth availability for one network.
type PlaneChannelProvider struct {
	Provider              string   `json:"provider"`
	CredentialsConfigured bool     `json:"credentialsConfigured"`
	MissingCredentials    []string `json:"missingCredentials,omitempty"`
}

// PlaneChannelProviders lists all supported social channel providers.
type PlaneChannelProviders struct {
	Data []PlaneChannelProvider `json:"data"`
}

// PlaneChannelConnectIn starts an OAuth flow or connects a credentialed channel,
// optionally pre-assigning it to a named employee or brand group.
type PlaneChannelConnectIn struct {
	Provider   string  `json:"provider"`
	Group      string  `json:"group,omitempty"`
	Return     string  `json:"return,omitempty"`
	Credential *string `json:"credential,omitempty"`
	AccountID  string  `json:"accountId,omitempty"`
	Fields     []Field `json:"fields,omitempty"`
	Handle     string  `json:"handle,omitempty"`
}

// PlaneChannelConnectOut answers an OAuth authorize URL or the newly connected channel.
type PlaneChannelConnectOut struct {
	AuthorizeURL string        `json:"authorizeUrl,omitempty"`
	Connected    bool          `json:"connected,omitempty"`
	Provider     string        `json:"provider,omitempty"`
	Group        string        `json:"group,omitempty"`
	Channel      *PlaneChannel `json:"channel,omitempty"`
}

// PlaneChannelRef names one channel by id.
type PlaneChannelRef struct {
	ID string `json:"id"`
}

// PlaneChannelUpdateIn updates a channel's metadata, status, or group.
type PlaneChannelUpdateIn struct {
	ID       string `json:"id"`
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
	Handle   string `json:"handle,omitempty"`
	Picture  string `json:"picture,omitempty"`
	Status   string `json:"status,omitempty"`
	Group    string `json:"group,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// PlaneChannelSetGroupIn assigns or clears a channel's employee/brand group.
type PlaneChannelSetGroupIn struct {
	ID    string `json:"id"`
	Group string `json:"group"`
}

// PlaneChannelAnalyticsIn requests analytics for one channel over a window of days.
type PlaneChannelAnalyticsIn struct {
	ID   string `json:"id"`
	Days string `json:"days,omitempty"`
}

// PlaneAnalyticsMetric is one metric series on a channel or post analytics response.
type PlaneAnalyticsMetric struct {
	Label string               `json:"label"`
	Total int64                `json:"total"`
	Data  []PlaneAnalyticsTick `json:"data"`
}

// PlaneAnalyticsTick is one dated data point in a metric series.
type PlaneAnalyticsTick struct {
	Date  string `json:"date"`
	Total int64  `json:"total"`
}

// PlaneChannelAnalyticsOut reports publishing and upstream analytics for one channel.
type PlaneChannelAnalyticsOut struct {
	ChannelID string                 `json:"channelId"`
	Provider  string                 `json:"provider"`
	Handle    string                 `json:"handle"`
	GroupName string                 `json:"groupName,omitempty"`
	Posts     int                    `json:"posts"`
	Published int                    `json:"published"`
	Scheduled int                    `json:"scheduled"`
	Failed    int                    `json:"failed"`
	Metrics   []PlaneAnalyticsMetric `json:"metrics"`
}

// PlanePost is one social post on the social plane.
type PlanePost struct {
	ID                    string   `json:"id"`
	Content               string   `json:"content"`
	Channel               string   `json:"channel"`
	Status                string   `json:"status"`
	ScheduleAt            int64    `json:"scheduleAt"`
	Media                 []string `json:"media"`
	AccountID             string   `json:"accountId,omitempty"`
	ExternalID            string   `json:"externalId,omitempty"`
	Error                 string   `json:"error,omitempty"`
	Origin                string   `json:"origin,omitempty"`
	ExternalIDPostiz      string   `json:"externalIdPostiz,omitempty"`
	ExternalIntegrationId string   `json:"externalIntegrationId,omitempty"`
	ExternalGroup         string   `json:"externalGroup,omitempty"`
	ReleaseURL            string   `json:"releaseUrl,omitempty"`
	CreatedAt             int64    `json:"createdAt"`
	UpdatedAt             int64    `json:"updatedAt"`
}

// PlanePostFilter filters the org's posts.
type PlanePostFilter struct {
	Status    string `json:"status,omitempty"`
	Channel   string `json:"channel,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	Group     string `json:"group,omitempty"`
	Limit     string `json:"limit,omitempty"`
}

// PlanePosts is a list of posts.
type PlanePosts struct {
	Data []PlanePost `json:"data"`
}

// PlanePostCreateIn creates, schedules, drafts, or immediately publishes a post
// to one or more channels or specific channel IDs.
type PlanePostCreateIn struct {
	Content    string   `json:"content"`
	Channel    string   `json:"channel,omitempty"`
	Channels   []string `json:"channels,omitempty"`
	AccountID  string   `json:"accountId,omitempty"`
	AccountIDs []string `json:"accountIds,omitempty"`
	Group      string   `json:"group,omitempty"`
	Status     string   `json:"status,omitempty"`
	ScheduleAt int64    `json:"scheduleAt,omitempty"`
	Media      []string `json:"media,omitempty"`
}

// PlanePostCreateOut returns the primary created post and, when a multi-channel
// fanout created more than one post row, all created posts in Posts.
type PlanePostCreateOut struct {
	PlanePost
	Posts []PlanePost `json:"posts,omitempty"`
}

// PlanePostRef names one post by id.
type PlanePostRef struct {
	ID string `json:"id"`
}

// PlanePostUpdateIn replaces one post by id.
type PlanePostUpdateIn struct {
	ID         string   `json:"id"`
	Content    string   `json:"content"`
	Channel    string   `json:"channel,omitempty"`
	AccountID  string   `json:"accountId,omitempty"`
	Status     string   `json:"status,omitempty"`
	ScheduleAt int64    `json:"scheduleAt,omitempty"`
	Media      []string `json:"media,omitempty"`
}

// PlanePostAnalyticsOut reports delivery state and engagement metrics for one post.
type PlanePostAnalyticsOut struct {
	PostID     string                 `json:"postId"`
	Channel    string                 `json:"channel"`
	AccountID  string                 `json:"accountId,omitempty"`
	Status     string                 `json:"status"`
	ReleaseURL string                 `json:"releaseUrl,omitempty"`
	ExternalID string                 `json:"externalId,omitempty"`
	Metrics    []PlaneAnalyticsMetric `json:"metrics"`
}
