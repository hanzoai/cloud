// The Hanzo work-item contract.
//
// Hanzo has THREE orthogonal planes for "state that changes over time". Each is
// ONE way to do its job; none is a substitute for another, and the boundaries
// between them are load-bearing. This file is law for where a new thing goes.
//
//	task Issue            engineering / project WORK ITEMS
//	framework.DocType     schema-defined DOMAIN RECORDS with a lifecycle
//	workflow              durable EXECUTION
//
// # 1. task Issue — the one work-item primitive
//
// A work item is a unit of engineering/project work someone (human or agent)
// moves across a board: a bug, a feature, a pull request, a parent epic. There
// is ONE such primitive — the task Issue — and no second issues table, no
// per-tool board. It is purpose-built native-Go (fixed schema, per-project
// monotonic KEY-N numbering, git binding, deterministic high-perf lists) — the
// durable replacement for the hanzo.team Svelte task.
//
// An Issue is (org, project, number) identity + mutable board state (title,
// description, status, priority, assignee, labels) + four IMMUTABLE
// discriminators set once at Create:
//
//	Kind    what it IS       issue | pr | epic
//	Source  who OPENED it    team | git | crm | helpdesk | cms | agent
//	Repo    git binding      "<repo>" for git-sourced rows, "" otherwise
//	ExtRef  external anchor   PR branch, or a link INTO another plane, ""
//
// Kind and Source are orthogonal and validated independently. Kind is the small
// closed set of genuine work-item shapes — deliberately NOT "task" (that word is
// the async plane, see §3) and NOT "deal"/"ticket"/"doc" (those are domain
// records, see §2). Source is the ORIGIN: source=helpdesk means "an engineering
// issue opened FROM a support escalation", not "a support ticket".
//
// Every work-item surface is a FILTER over this one table, served by the SAME
// /v1/task endpoints — never a parallel store:
//
//	hanzo.team board      Filter{}                       every issue in the project
//	board column          Filter{Status: "todo"}
//	git repo Issues tab   Filter{Repo: r, Kind: "issue"}
//	git repo PRs tab      Filter{Repo: r, Kind: "pr"}
//	an epic's children    (issues whose ExtRef is the epic)
//	an agent's work        Filter{Source: "agent"}
//
// # 2. framework.DocType — the one domain-record plane
//
// A domain record is a structured, schema-defined row with its own fields and a
// status lifecycle: a CMS article (Draft -> Published), a helpdesk ticket
// (Open -> Pending -> Resolved -> Closed), an ERP document, a knowledge entry.
// These are framework.DocType on Hanzo Base (help/cms/content/erp/knowledge),
// and richly-relational domains that predate the framework (crm Company /
// Contact / Opportunity) keep their bespoke stores. They are NOT task Issues:
// forcing a ticket or an opportunity into the flat Issue shape would DROP its
// schema and relations — the opposite of decomplecting. A ticket's queue is a
// DocType query; a CRM pipeline is an Opportunity query. Do not rebuild them on
// the task, and do not rebuild the task as a DocType.
//
// # 3. workflow — the one durable-execution substrate
//
// Neither plane above runs background work. Retries, scheduling, and anything
// that must survive a restart is a workflow on github.com/hanzoai/tasks (its
// CONTRACT: "there is no second async system"), served at /v1/workflow by
// apps/workflow. If you write `go func(){}` you are writing a bug.
//
// # The clients — compose by reference, never by embedding
//
// The three planes touch only across thin, one-directional clients:
//
//	Issue   <-> DocType   a domain record links to the engineering work item
//	                      about it (and back) by ExtRef. A helpdesk ticket that
//	                      needs a code fix opens an Issue{Source:"helpdesk"} and
//	                      stores its number; neither table embeds the other.
//	Issue    -> workflow  creating/moving an Issue may START a workflow (notify an
//	                      assignee, kick a git build). The Issue never blocks.
//	workflow -> Issue     a workflow may PATCH an Issue's board state as its
//	                      durable side effect (build passed -> move the pr to done).
//
// Never invert a client: the board holds no queue and no domain schema; DocType
// holds no board and no queue; workflow holds no records and no columns. Intent,
// records, and execution stay decomplected — three values, each complete.

package task
