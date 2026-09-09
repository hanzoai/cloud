package claw

import "encoding/json"

// The run family: what a run leaves behind on a machine, and what an operator
// is asked before a command runs.
//
// Three things here belong to a host with a working copy on it. A managed
// worktree is a git checkout an agent was given, at a path, on a branch, cut
// from a base ref. A terminal is a PTY an operator types into, with a shell, a
// working directory and a stream of bytes. An exec approval is a command that
// has stopped and is waiting for a person to say yes; saying yes always can
// mint a standing grant a scheduled job later spends.
//
// Execution in this cloud is clients/exec: a request carrying a language and a
// program, forwarded to an isolated executor that answers with its output.
// That executor has no repository checkout to name, no shell to attach to, and
// no command it stops to ask about — and it is reached over HTTP, exposing no
// surface a method here could read a run off. So nothing in this cloud creates
// a worktree, a terminal or an approval, and each method answers the empty
// case its own contract already defines:
//
//	worktrees.list   no checkout is managed here
//	worktrees.gc     a sweep over nothing removes nothing
//
// Neither invents a row, and each answers something true: the Worktrees tab is
// always in the sidebar (components/sessions-hub-tabs.ts lists it
// unconditionally), so a gateway that answers the list turns a page that errors
// on load into a clean "No managed worktrees", and a collection that removes
// nothing reports three honest zeros.
//
// worktrees.restore, worktrees.branches, worktrees.create, terminal.list,
// terminal.close and exec.approval.grants.revoke are not registered. Each names
// a worktree, a branch, a terminal session or a grant, and there is none, so
// each could only refuse. The terminal pair is unreachable besides: the whole
// terminal surface is gated on terminal.open being advertised
// (ui/src/lib/terminal-availability.ts), and nothing here can open one.
//
// What a real one needs. A worktree needs a filesystem this gateway may hand
// out paths into, a git working copy on it, and a record of which session owns
// which checkout — the record type below is that record. A terminal needs a
// PTY held open per connection and its bytes streamed as terminal.data /
// terminal.exit events, which is a second execution path and belongs behind
// clients/exec rather than beside it. Neither has a counterpart here: the two
// executors in this cloud are clients/exec, which forwards a program to an
// isolated runner over HTTP, and clients/functions, which runs one subprocess —
// and neither has a checkout to name or a shell to attach to.
//
// Parameters bind strictly: both methods declare a closed schema upstream.

// The price of each method is the protocol's own, from
// src/gateway/methods/core-descriptors.ts. Reading a worktree costs read;
// changing the set of them costs admin, because it deletes and restores
// directories.
//
// No event is announced. terminal.data and terminal.exit carry one session's
// bytes; with no session there are none to carry.
func init() {
	Register("worktrees.list", Read, listWorktrees)
	Register("worktrees.gc", Admin, sweepWorktrees)
}

// ── worktrees ────────────────────────────────────────────────────────────────

// worktree is WorktreeRecordSchema (packages/gateway-protocol/src/schema/
// worktrees.ts). Everything above the break is required of a producer: the
// fingerprint groups checkouts of one repository, the base ref is what the
// branch was cut from, and the two stamps are what the list is ordered by.
// Below the break, ownerId names the session that holds it, snapshotRef is the
// commit a removal parked its working state at — restore reads that one — and
// removedAt being set is what makes a row restorable rather than live.
type worktree struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"repoFingerprint"`
	RepoRoot    string `json:"repoRoot"`
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	BaseRef     string `json:"baseRef"`
	OwnerKind   string `json:"ownerKind"`
	Created     int64  `json:"createdAt"`
	LastActive  int64  `json:"lastActiveAt"`

	OwnerID     string `json:"ownerId,omitempty"`
	SnapshotRef string `json:"snapshotRef,omitempty"`
	// RemovedAt is a pointer because absent and zero are different answers: the
	// client reads an absent removedAt as a live checkout and any number,
	// including zero, as a removed one it may restore
	// (ui/src/pages/worktrees/worktrees-page.ts). An omitempty int would report
	// a worktree removed at the epoch as live.
	RemovedAt *int64 `json:"removedAt,omitempty"`

	// RunEndCleanup is what a finished run does with the checkout. It is
	// carried as written rather than modelled, because nothing here decides it
	// and re-declaring a union of two closed objects would be a second copy of
	// a schema this file does not own.
	RunEndCleanup json.RawMessage `json:"runEndCleanup,omitempty"`
}

// listWorktrees answers with the checkouts this gateway manages. The list is
// empty and must still be a list: the page sorts it as it arrives
// (ui/src/pages/worktrees/worktrees-page.ts:90) and the session menu searches
// it for one id, so a null there is what a client maps over and dies on.
func listWorktrees(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	return map[string]any{"worktrees": []worktree{}}, nil
}

// sweepWorktrees collects what a finished run left behind: directories whose
// owner is gone, and snapshots older than the retention the collector keeps.
// Over an empty set it removes nothing, which is a true count rather than a
// refusal — the page runs this from a button and shows what it collected.
func sweepWorktrees(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	return map[string]any{
		"removed":         []string{},
		"orphansDeleted":  0,
		"snapshotsPruned": 0,
	}, nil
}
