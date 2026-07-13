// The Hanzo work-item contract.
//
// This is law for every Hanzo product that has a "thing someone works on" —
// a bug, a pull request, a support ticket, a sales deal, a content task, an
// epic, a doc. There is ONE such primitive: the tracker Issue. There is no
// second issue table, no per-product work-item schema, no parallel board.
//
// # One primitive
//
// An Issue is a row: (org, project, number) identity + mutable board state
// (title, description, status, priority, assignee, labels) + four IMMUTABLE
// discriminators set once at Create:
//
//	Kind    what it IS       issue | pr | task | epic | deal | ticket | doc
//	Source  who OPENED it    team | git | crm | helpdesk | cms | agent
//	Repo    git binding      "<repo>" for git-sourced rows, "" otherwise
//	ExtRef  external anchor   PR branch, deal id, ticket #, doc slug, ""
//
// Kind and Source are orthogonal: a git surface can open a task, an agent can
// open a deal. They are validated independently against closed sets and never
// braided into one "type" enum.
//
// # One law: every surface is a filter
//
// A product surface is NOT a new store. It is a FILTER over this one table,
// expressed as an IssueFilter and served by the SAME /v1/tracker endpoints:
//
//	hanzo.team board      Filter{}                       every row in the project
//	board column          Filter{Status: "todo"}
//	git repo Issues tab   Filter{Repo: r, Kind: "issue"}
//	git repo PRs tab      Filter{Repo: r, Kind: "pr"}
//	CRM pipeline          Filter{Kind: "deal"}
//	helpdesk queue        Filter{Kind: "ticket"}
//	CMS task list         Filter{Source: "cms"}
//	an agent's work       Filter{Source: "agent"}
//
// Adding a surface adds a filter, never a table. If you reach for a second
// issues store you are complecting identity with presentation — stop.
//
// # Two tenancy roots, never conflated
//
// The physical boundary is the IAM project (principal.Project) — one SQLite
// file per (org, IAM-project). WITHIN it, a tracker Project (the KEY-N handle,
// a Linear "team") groups issues. A git repo is a THIRD thing: the code layer
// under the IAM project, bound to issues by the Repo discriminator, not by
// being a tracker Project. So "the cloud repo's issues" = rows with
// Repo:"cloud"; the tracker Project they live in is an org convention (e.g. a
// per-repo or per-org default team), NOT the repo itself.
//
// # The tasks seam: tracking is not execution
//
// The Issue is the plane of INTENT and STATE — what a human or agent should do
// and where it sits on a board. It is not an execution engine. Durable async
// execution is github.com/hanzoai/tasks, the one substrate for retries,
// scheduling, and work that must survive a restart (see its CONTRACT: "there
// is no second async system").
//
// The two compose across a thin seam, and only in these two directions:
//
//	Issue -> task   creating/moving an Issue may ENQUEUE a task (notify an
//	                assignee, kick a build for a git pr, run a CRM webhook).
//	                The Issue never blocks on it and never runs it inline.
//	task  -> Issue  a task may PATCH an Issue's board state as its durable
//	                side effect (build passed -> move pr to done; ticket SLA
//	                exceeded -> raise priority).
//
// Never invert this: the tracker holds no queue, spawns no `go func()`, owns
// no retry policy; tasks holds no board columns and renders no list. Intent
// and execution stay decomplected — one seam, two values, each complete.

package tracker
