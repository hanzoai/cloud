# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package pref

struct prefsView {
    Prefs     bytes @0
    UpdatedAt i64   @8
}

interface pref {
    # Returns the signed-in caller's OWN preference document — the theme,
    # density and pinned nav that follow them across every Hanzo surface. There is no
    # path to another user's preferences: not for an org admin, not for a platform
    # SuperAdmin, because the subject is built from the validated credential and is the
    # mandatory predicate on the read. A caller who has never saved anything gets an
    # empty document at 200, never a 404, so the user menu always renders.
    get_pref() returns (rep: prefsView)
}
