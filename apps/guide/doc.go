// Package guide is the AI that builds an agentic company with you, one measured
// step at a time.
//
// Hanzo OS is the operating system an autonomous company runs on, and guide is
// how an org gets there: a launch journey where every step names a real
// capability, the agent can carry the step out through the same tool plane the
// caller would, and a step is done when a SIGNAL says so rather than when
// somebody ticked a box. That last property is what makes the journey science
// instead of a checklist — progress here is measured, never asserted.
//
// It is three orthogonal things composed:
//
//   - a CHECKLIST ENGINE over a machine-readable curriculum (steps with id, section,
//     title, detail, tool, dependencies). Per-org progress tracks a state per step
//     (todo|in_progress|done|skipped); next-step logic honours dependencies; a step
//     whose done-criterion maps to a real signal auto-marks done when that signal is
//     present (auto-detect).
//   - a BUSINESS AI AGENT: for a step bound to an MCP tool, "do it for me" drafts
//     the content with the embedded AI (deps.AI) and executes the tool through the
//     per-principal MCP plane (automations.InvokeTool) AS THE CALLER — so the
//     action can never exceed the caller's own authorization and is metered +
//     audited like any MCP call.
//   - a BLUEPRINT: the full Guide™ playbook (sections, steps, strategies, templates,
//     each carrying an admin `enabled` lever). It is seeded from the embedded fixture
//     into a DB table, then authored LIVE by a SuperAdmin on admin.hanzo.ai; at
//     runtime the DB is authoritative. See blueprint.go for the schema + projection,
//     blueprint_store.go for the seeded/versioned store, admin.go for the CRUD plane.
//
// This file is the pure ENGINE: the checklist schema (JourneyStep / Curriculum), validation,
// and the dependency/next-step/auto-detect logic — all free functions over plain data,
// no I/O, so they are exhaustively unit-testable. The engine ALWAYS runs on a
// Curriculum of ENABLED steps (the projection Blueprint.Curriculum() drops disabled
// sections/steps and filters their edges), so no enable-logic braids into the engine.
// Parsing/validation of the full blueprint live in blueprint.go; storage in store.go
// (per-org) + blueprint_store.go (shared); detectors in detect.go; the agent in
// agent.go; the HTTP surface in guide.go.
package guide
