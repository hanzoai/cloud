package todo

// typed.go is the VOCABULARY todo's typed ops are written in: the receiver
// they hang off, and the In and Out types more than one of them shares. The ops
// themselves live beside the source they read — source.go for the forge, search.go
// for the org-wide index — because an op belongs next to the thing it answers
// from, and a file that collected them by their signature would separate every one
// of them from its own store.
//
// A typed op is ONE registry entry with N projections — the OpenAPI operation's
// schema and prose, the MCP tool, the CLI command and every generated SDK method
// all come from it. An untyped route gets a route and nothing else. The only
// untyped routes left on this surface are the three that answer a bare 405
// (todo.go says why): they have one answer and no shape to describe.

import (
	"github.com/hanzoai/cloud"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listProjects), which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// projectList is one org's todo projects, newest first. Empty is an empty
// JSON array, never null.
type projectList []todoProject

// issueList is one project's issues. Empty is an empty JSON array, never null.
type issueList []issueView

// ----- projects -------------------------------------------------------------

// projectRef addresses ONE of the caller's todo projects by its key. The key
// is the path segment — the URL is the addressing authority — and GET and DELETE
// carry no request body at all, so there is nothing a caller could smuggle a
// second key in through.
type projectRef struct {
	// Key is the project's org-unique handle: 2-8 uppercase alphanumerics starting
	// with a letter ("ENG", "OPS2"). Matched case-insensitively.
	Key string `json:"key"`
}

// ----- issues ---------------------------------------------------------------

// issueRef addresses ONE work item: the board it is on and its number there.
// Both come from the path, for the reason projectRef gives — the URL is the
// addressing authority, and a GET carries no body to smuggle a second one in.
type issueRef struct {
	// Key is the board — the repository name, or an index board's key.
	Key string `json:"key"`
	// Num is the issue's number on that board.
	Num int64 `json:"num"`
}

// issueQuery lists a project's issues, optionally narrowed. Every filter is a
// query parameter and every one is optional; omitting all of them returns the
// whole board.
type issueQuery struct {
	// Key is the project whose issues to list, from the path. EMPTY means every
	// project in the org — the global board. It is a filter like the rest of
	// this struct rather than an address, which is what lets one op answer both
	// "this board" and "all the work" without a second surface disagreeing with
	// the first about what a column is.
	Key string `json:"key"`
	// Status keeps only issues in that board column: backlog, todo, in_progress,
	// done or canceled. An unknown value is refused with 400.
	Status string `json:"status"`
	// Kind keeps only work items of that shape: issue, pr or epic. An unknown
	// value is refused with 400.
	Kind string `json:"kind"`
	// Repo keeps only issues bound to that git repository.
	Repo string `json:"repo"`
	// Label keeps only issues carrying that label, compared case-insensitively.
	//
	// This is how a board narrows to something SMALLER than a repository — the
	// one mechanism for it. An estate whose apps are directories inside one
	// repository (hanzoai/cloud carries ~140 of them) has no repository per app
	// to address, so the app is a label: `label=app/meet` is the meet board.
	// Nothing is provisioned to make one exist; a board is the query.
	Label string `json:"label"`
	// Source keeps only issues opened from that surface: team, git, crm,
	// helpdesk, cms or agent. An unknown value is refused with 400.
	Source string `json:"source"`
	// Scheduled keeps only issues that carry a date — a start, a due date or
	// both. This is the timeline's slice of the board: pass scheduled=true to
	// get exactly the rows a gantt has somewhere to draw, instead of fetching
	// every issue and discarding the undated ones client-side.
	Scheduled bool `json:"scheduled"`
}
