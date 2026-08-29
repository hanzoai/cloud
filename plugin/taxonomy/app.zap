# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package taxonomy

struct Category {
    Owner   text        @0
    ID      text        @8
    Label   text        @16
    Summary text        @24
    Order   i64         @32
    Brands  list<text>  @40
    Taxa    list<bytes> @48
}

struct Taxon {
    Owner       text       @0
    ID          text       @8
    Name        text       @16
    Description text       @24
    Category    text       @32
    Tags        list<text> @40
    Icon        text       @48
    Route       text       @56
    Href        text       @64
    Brands      list<text> @72
    Order       i64        @80
    Published   bool       @88
}

struct Taxonomy {
    Categories list<bytes> @0
}

struct categoryIn {
    ID      text       @0
    Label   text       @8
    Summary text       @16
    Order   i64        @24
    Brands  list<text> @32
}

struct deleted {
    Deleted text @0
}

struct idIn {
    ID text @0
}

struct readIn {
    Brand text @0
}

struct taxonIn {
    ID          text       @0
    Name        text       @8
    Description text       @16
    Category    text       @24
    Tags        list<text> @32
    Icon        text       @40
    Route       text       @48
    Href        text       @56
    Brands      list<text> @64
    Order       i64        @72
    Published   bool       @80
}

interface taxonomy {
    # Removes one empty category. A category that still has taxa
    # filed under it is refused with 409 and a count: deleting the label off a group
    # must never silently take the products wearing it, and the alternative — orphan
    # rows naming a category that no longer exists — is a catalogue that cannot be
    # rendered. Move or delete its taxa first. An id no category holds is a 404.
    delete_taxonomy_categories_by_id(req: idIn) returns (rep: deleted)
    # Removes one product from the catalogue. An id no taxon holds is a
    # 404. To take a product out of view without losing what was written about it, set
    # `published` to false instead.
    delete_taxonomy_taxa_by_id(req: idIn) returns (rep: deleted)
    # Read returns the product catalogue as this caller sees it: the PLATFORM
    # catalogue — Hanzo's own products, the part that is true for everyone — plus the
    # caller's own org's rows, every category in display order and each carrying the
    # products filed under it in theirs. Another customer's rows are never in it. It
    # is readable signed out, and a signed-out visitor gets the platform catalogue
    # alone, which is what the marketing landing renders from.
    # Where the caller's org and the platform hold the same id, the caller's own row
    # is the one served. That rule exists because ids are unique per ORG and not
    # globally — two customers may each have a "crm", and refusing the second would
    # tell one of them the other exists — so a collision with the platform is possible
    # by construction and something has to win deterministically. Yours does: your own
    # catalogue is the one you edited.
    # `?brand=` narrows it the way a brand's own console does: only the categories
    # that brand admits, and within them only the taxa scoped to it. An unpublished
    # row is served only to whoever may edit it — a SuperAdmin for the platform's, an
    # org admin for their own — so a product can be staged before anyone sees it
    # without becoming invisible to the person staging it.
    get_taxonomy(req: readIn) returns (rep: Taxonomy)
    # Creates or replaces one category and returns it as stored. The id in
    # the URL is the one it is filed under whatever the body says, so a category can
    # never be written under a name it was not addressed by — which also makes create
    # and replace the same act, and is why there is no POST beside this.
    # Platform SuperAdmin only: one catalogue serves every tenant, so an org admin who
    # could rename a category would rename it for all of them.
    put_taxonomy_categories_by_id(req: categoryIn) returns (rep: Category)
    # Creates or replaces one product and returns it as stored. The id in the
    # URL is the one it is filed under whatever the body says. The category must
    # already exist — a taxon naming a category that does not is refused with 400
    # rather than stored where nothing can render it.
    # A taxon opens exactly one way: `route` for a product the console renders
    # itself, or `href` for one that genuinely lives at its own domain. Giving both,
    # or neither, is refused.
    # Platform SuperAdmin only.
    put_taxonomy_taxa_by_id(req: taxonIn) returns (rep: Taxon)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   Category.Taxa  taxonomy.Taxon (list element)
#   Taxonomy.Categories  taxonomy.Category (list element)
