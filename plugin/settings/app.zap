# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package settings

struct productIn {
    Product text @0
}

struct settingsView {
    Product    text       @0
    Config     bytes      @8
    SecretKeys list<text> @16
    UpdatedAt  text       @24
    CreatedAt  text       @32
}

interface settings {
    # Reads the caller org's configuration for one product, with every
    # secret field MASKED — only the names of the set secrets come back, never their
    # values, which live in KMS. A product the org has never configured is not a 404:
    # it answers 200 with an empty config object, so the console's Settings tab always
    # renders and merges its own display defaults on top.
    get_settings_by_product(req: productIn) returns (rep: settingsView)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# blocked (2) — the op is absent; the field has no wire form:
#   put_settings_by_product  settingsReq.Config  map[string]interface {}  (map)
#   put_settings_by_product  settingsReq.Secrets  map[string]string  (map)
