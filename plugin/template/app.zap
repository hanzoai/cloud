# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package template

struct StarterKit {
    Slug        text        @0
    Title       text        @8
    Category    text        @16
    Description text        @24
    Framework   text        @32
    Features    list<text>  @40
    UseCase     text        @48
    Tier        i64         @56
    Rating      f64         @64
    Source      text        @72
    Preview     text        @80
    Demo        text        @88
    Variants    list<bytes> @96
    Org         text        @104
}

struct kitList {
    Data list<bytes> @0
}

struct kitRef {
    Slug text @0
}

struct publishKitIn {
    Slug        text        @0
    Title       text        @8
    Category    text        @16
    Description text        @24
    Framework   text        @32
    Features    list<text>  @40
    UseCase     text        @48
    Source      text        @56
    Preview     text        @64
    Demo        text        @72
    Variants    list<bytes> @80
}

struct replaceKitIn {
    Slug        text        @0
    Title       text        @8
    Category    text        @16
    Description text        @24
    Framework   text        @32
    Features    list<text>  @40
    UseCase     text        @48
    Source      text        @56
    Preview     text        @64
    Demo        text        @72
    Variants    list<bytes> @80
}

interface template {
    # Deletes the caller org's OWN starter kit. A slug they do not own is a
    # 404, never a delete: the DELETE binds org.
    delete_template_by_slug(req: kitRef)
    # Lists the public starter-kit catalog plus, for a validated caller, that
    # org's own private kits. No request field can widen the scope: the org comes
    # from the validated principal, so an anonymous or cross-org caller structurally
    # sees the public catalog only.
    get_template() returns (rep: kitList)
    # Returns one starter kit: the caller org's own by that slug, else the public
    # catalog's. A slug another org owns reads as not found.
    get_template_by_slug(req: kitRef) returns (rep: StarterKit)
    # Creates a starter kit PRIVATE to the caller's org and answers 201 with
    # the stored kit. The owner is stamped by the server, so a body "org" is never
    # trusted; publishing over a public-catalog slug is 409, so a slug still names
    # exactly one kit.
    post_template(req: publishKitIn) returns (rep: StarterKit)
    # Overwrites the caller org's OWN starter kit at the path slug, answering
    # the stored kit. A slug they do not own is 404, never a create: the UPDATE binds
    # org, so a PUT can never reach another org's kit.
    put_template_by_slug(req: replaceKitIn) returns (rep: StarterKit)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# opaque (4) — crosses, arrives without its name:
#   StarterKit.Variants  template.Variant (list element)
#   kitList.Data  template.StarterKit (list element)
#   publishKitIn.Variants  template.Variant (list element)
#   replaceKitIn.Variants  template.Variant (list element)
