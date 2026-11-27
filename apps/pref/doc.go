// Package prefs is your own settings — theme, density, pinned nav — following you
// across every Hanzo app.
//
// It is the per-USER preference plane for the unified Hanzo Cloud binary: the
// /v1/pref surface behind the user menu on every Hanzo surface (console, insights,
// and anything else that renders "signed in as").
//
// ONE preference store, EVERY surface. A user's theme, density, and pinned nav
// follow them between products instead of each app keeping its own copy in its
// own localStorage — which is what makes the same person look like two different
// users depending on which tab they are in.
//
// Surface (all user-scoped; /v1 only):
//
//	GET   /v1/pref   the caller's own document          -> prefsView
//	PATCH /v1/pref   shallow key-wise merge into it     -> prefsView
//
// PATCH, not PUT: a surface saves the keys it owns (the console saves `theme`,
// insights saves `density`) without having to send back keys it does not know
// about — a PUT would make every client responsible for preserving every other
// client's keys, and the first one to forget silently deletes them.
//
// USER ISOLATION is enforced SERVER-SIDE on every request. The subject is the
// canonical `<owner>/<name>` identity built from values the identity boundary
// minted from a VALIDATED credential (HIP-0026), and is the mandatory predicate
// on every store statement. It is NEVER read from a query param or body, and
// there is no "read another user's prefs" path at all: not for an org admin, not
// for a platform SuperAdmin. Preferences are personal, and no operational task
// requires reading someone else's.
//
// NOT SETTINGS. apps/settings is per-ORG, per-product configuration with KMS
// custody for secret fields. This is per-USER UI state with no secrets. They are
// different tenancy keys answering different questions, so they are different
// planes — collapsing them would put one user's theme under an org key and make
// an org admin the owner of everyone's UI.
package pref
