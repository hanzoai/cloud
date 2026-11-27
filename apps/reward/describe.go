package reward

// describe.go is the prose of the routes the embedded hanzoai/reward module serves.
// Its handlers live in another module, so there is no doc comment in this repo for
// zipdoc to lift; left bare, each would publish an operationId and nothing else: an
// SDK method that cannot explain itself, an MCP tool a model cannot pick, a CLI
// command with no help text. openapi.Describe is the client for exactly one route,
// keyed by the router's pattern verbatim, and a description whose route the router
// does not carry never renders, so this file can explain a route but not add one.

import (
	"net/http"

	"github.com/hanzoai/cloud/sdk/spec"
)

func init() {
	for _, r := range []struct{ method, path, summary, description string }{
		{http.MethodGet, "/v1/reward/health", "Whether the rewards program can answer",
			"Answers 200 with the program's name when its store answers, and 503 when it does not. No credential is needed."},
		{http.MethodGet, "/v1/reward/catalog", "The events, pillars, levels and rule operators the program is built from",
			"Public. Lists the five pillars (use, share, community, contribute, expert), every event a rule may read with the typed fields each carries and whether it must arrive with evidence, the comparison operators and the actions a rule may take. The admin screens build their dropdowns from it."},
		{http.MethodPost, "/v1/reward/events", "State one event to the rules engine (the platform only)",
			"Only the platform principal states an event: the platform's own feeders do, in process, and a client never can. The event is refused unless it carries exactly the fields the catalog lists for its type, and, for an event that took place on another site, the evidence of where and the checks run there. The same event id again is the same claim, so a retry awards nothing twice. Answers each rule's decision: claimed, skipped, or why it was withheld."},
		{http.MethodGet, "/v1/reward/me", "Your level, lifetime points, redeemable points and badges",
			"Reads the signed-in person's standing in each program from the append-only ledger: lifetime points (which levels and rank read, and which never fall), the part of them that is redeemable, the level and the next one, and the badges held."},
		{http.MethodGet, "/v1/reward/me/claims", "Your claims, and where each stands",
			"Lists the signed-in person's claims, newest first, each with the rule, the points it would earn, its status (pending, approved, awarded, rejected, reversed) and the evidence the claim carries. A pending claim is not points."},
		{http.MethodGet, "/v1/reward/me/explore", "What you could earn next, by pillar",
			"Lists the active rules the signed-in person holds no live claim under, grouped by pillar, so a page can suggest what to try next from what they have not done."},
		{http.MethodGet, "/v1/reward/me/profile", "Your rewards profile",
			"Reads the signed-in person's handle, whether their profile is public, and their links. A profile is private until its owner turns it on."},
		{http.MethodPut, "/v1/reward/me/profile", "Set your handle, whether your profile is public, and your links",
			"Chooses a unique lower-case handle, turns the public profile on or off and sets https links. The public view shows level, lifetime points, badges and claim counts, never an email, org, subject id or redeemable balance."},
		{http.MethodGet, "/v1/reward/profile/:handle", "A person's public rewards profile",
			"Public. Reads a person's level, lifetime points, badges and counts by pillar by the handle they chose, once they turned their profile on. A private profile and an unknown handle are the same 404."},
		{http.MethodGet, "/v1/reward/badge/:handle", "The README badge of a person's level, as an SVG",
			"Public once the person turns their profile on. Name the handle with the .svg extension. theme=dark|light and style=flat|card choose the look. The image is never cached by a shared cache and answers 304 to a matching ETag, and every word on it is escaped."},

		{http.MethodGet, "/v1/admin/reward/programs", "List the reward programs (SuperAdmin)",
			"SuperAdmin only. Lists every program with its rules, budget, levels, badges and email templates."},
		{http.MethodPost, "/v1/admin/reward/programs", "Create a reward program (SuperAdmin)",
			"SuperAdmin only. Creates a program under a slug. Every save is on the audit trail."},
		{http.MethodGet, "/v1/admin/reward/programs/:program", "Read one reward program (SuperAdmin)",
			"SuperAdmin only. Reads the program: its rules with their versions, its monthly budget and its levels and badges."},
		{http.MethodPut, "/v1/admin/reward/programs/:program", "Save a reward program's settings (SuperAdmin)",
			"SuperAdmin only. Replaces the program's name, whether it is active, its budget, levels, badges and templates. Rules are saved one at a time. The last save wins and is on the trail."},
		{http.MethodDelete, "/v1/admin/reward/programs/:program", "Delete a reward program (SuperAdmin)",
			"SuperAdmin only. Refused with 400 once any claim or ledger row names the program: retire it with active=false instead."},
		{http.MethodPut, "/v1/admin/reward/programs/:program/rules/:rule", "Create or replace one rule (SuperAdmin)",
			"SuperAdmin only. A rule reads one catalog event, tests its fields, and says what an approved claim does: points, a badge, credit or an email. Its approval is manual until set to auto, and it carries its own limits. The rule's version rises only when its content changes, and a claim already made keeps awarding what its version said."},
		{http.MethodDelete, "/v1/admin/reward/programs/:program/rules/:rule", "Delete one rule (SuperAdmin)",
			"SuperAdmin only. Refused with 400 once any claim names the rule: retire it with active=false instead."},
		{http.MethodPost, "/v1/admin/reward/programs/:program/backfill", "Write legacy points for people (SuperAdmin)",
			"SuperAdmin only. Takes up to 5,000 rows of subject, source, points and evidence and writes each as a legacy ledger entry: counted in lifetime points, levels and rank, never redeemable, idempotent per person and source (the first count stands). One call is one trail record naming who ran it."},
		{http.MethodGet, "/v1/admin/reward/claims", "The staff queue of claims (SuperAdmin)",
			"SuperAdmin only. Lists claims, newest first, filtered by status, program or subject. Each carries the evidence its event stated: the canonical URL, the checks run on it (fetched_at, status, public, ref_found, author_match, with the whole history of a post's week), or needs_review when the source could not be read."},
		{http.MethodPost, "/v1/admin/reward/claims/:claim/approve", "Approve a claim (SuperAdmin)",
			"SuperAdmin only. Decides that the claim is awarded and writes what it earns, each exactly once. Approving a claim already approved finishes it, so a retry is safe."},
		{http.MethodPost, "/v1/admin/reward/claims/:claim/reject", "Reject a claim with a note (SuperAdmin)",
			"SuperAdmin only. Refuses the claim; a note saying why is required. A rejected claim frees its limits and budget."},
		{http.MethodPost, "/v1/admin/reward/claims/:claim/reverse", "Reverse an awarded claim with a note (SuperAdmin)",
			"SuperAdmin only. Undoes what an awarded claim earned with compensating ledger rows, never an edit; a note is required. A claim that granted credit cannot be reversed here."},
		{http.MethodPost, "/v1/admin/reward/simulate", "Dry-run a rule against an event (SuperAdmin)",
			"SuperAdmin only. Evaluates a rule, saved or not, against an event and says what it would decide, writing nothing."},
	} {
		spec.Describe(r.path, r.method, r.summary, r.description)
	}
	// The routes that need no credential, so a generated client does not ask for one.
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/reward/health"}, {http.MethodGet, "/v1/reward/catalog"}, {http.MethodGet, "/v1/reward/rules"},
		{http.MethodGet, "/v1/reward/profile/:handle"}, {http.MethodGet, "/v1/reward/badge/:handle"},
	} {
		spec.Open(r.path, r.method)
	}
}
