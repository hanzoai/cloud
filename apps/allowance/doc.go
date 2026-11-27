// Package allowance is how much a plan lets you do without paying, and how much of
// it you have left in the window that holds you.
//
// It bounds the ONE thing a wallet cannot. Money gates every priced route, but a
// route priced at ZERO leaves the balance gate nothing to refuse — which is
// deliberate, so that a caller with no wallet can still reach the free pool — and
// the free pool runs on our own compute. Without a ceiling that lane is unlimited
// for anyone who can name a free model. The allowance is that ceiling: a COUNT of
// calls, per subject, per window, taken from the caller's plan. A window is the UTC
// hour, the UTC day, or a SESSION: one that opens at the subject's first call after
// the last one ended and runs ai_plan_session_hours (5), exactly as a paid plan's
// session does (apps/ai/limits). The Free plan is flags.SeedFreeSession (50) calls a session.
//
// COUNT AND MONEY NEVER STAND IN FOR EACH OTHER. A free call costs nothing, so a
// balance can neither express it nor refuse it; a paid call is bounded by the wallet
// and never reaches this store. Keeping them apart is what lets the free tier be
// generous without issuing credit we do not owe, and lets a paid plan be metered in
// dollars without a second opinion about it.
//
// The surface:
//
//	GET /v1/allowance     what the CALLER has left in their window, and when it reopens
//	plane allowance_read  the same, for any app that shows it
//	plane allowance_take  admit one call, counting it, and answer the same
//
// A CALL IS COUNTED WHEN IT IS ADMITTED. The gate takes a unit before it reaches any
// upstream, so a call that then fails — a vendor that errors, a stream that dies —
// has still spent its unit. That is the point: a loop retrying a failing upstream
// cannot run forever on a free allowance it never spends. A call refused at the
// ceiling counts nothing, and two calls cannot both take the last unit.
//
// FREE AI CALLS ARE COUNTED PER PERSON. The subject is the caller's IAM home org and
// user id (client.Person), so an API key counts against the user it was minted for,
// and the members of one org — every consumer signup shares one — each hold their own
// count. The ceiling is decided by the WALLET that pays (AllowanceIn.Payer): its plan
// and its balance.
//
// The count lives on the KV every pod shares (Store), so a person spread over pods is
// held to one count. The gate is in another process (the pod runs each subsystem as
// its own program), so it asks this app over the plane.
//
// THE LIMIT IS A PLATFORM SWITCH, per tier, editable live at admin.hanzo.ai through
// the existing /v1/admin/flags cockpit, whose shipped default is seed below.
//
// UNBOUNDED IS SAID ONLY ABOUT SOMEONE WE CAN NAME. A named tier's switch may be 0,
// which is an admin's decision about a subscriber commerce identified. A caller
// nobody can name — the public lane, an unresolved tier, a tier read that failed —
// gets the floor, and no setting of that switch can turn it off. A route STATED at
// zero can still be served by a vendor who bills us, so "we could not tell who this
// is" must never mean "as much as they want".
package allowance
