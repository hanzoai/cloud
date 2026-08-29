# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package help

struct helpArticle {
    Slug      text @0
    Title     text @8
    Category  text @16
    Excerpt   text @24
    Body      text @32
    UpdatedAt i64  @40
}

struct helpArticleList {
    Data list<bytes> @0
}

struct helpArticleRef {
    Slug text @0
}

struct helpArticlesQuery {
    Category text @0
    Limit    i64  @8
}

struct helpCategoryList {
    Data list<bytes> @0
}

struct helpTicketFiled {
    Ticket text @0
    Status text @8
}

struct helpTicketIntake {
    Subject     text @0
    Description text @8
    Email       text @16
    Priority    text @24
}

interface help {
    # Returns the public knowledge base: the help center's Published,
    # publicly-visible articles as cards. The org is server-fixed and the
    # status/is_public filter is server-set, so neither the tenant nor the visibility
    # can be widened by the caller. A deployment with no help center answers 404.
    get_help_articles(req: helpArticlesQuery) returns (rep: helpArticleList)
    # Returns one public article by slug, with its body. A missing, Draft,
    # or internal (non-public) article is 404 — fail-closed, so this route is no
    # existence oracle for anything beyond "published and public".
    get_help_articles_by_slug(req: helpArticleRef) returns (rep: helpArticle)
    # Returns the knowledge-base sections for the public center's
    # navigation — but ONLY the sections that front at least one Published, public
    # article, so an internal (agent-only) category name or description never leaks. A
    # section with no public article is invisible; a center with no public articles has
    # no sections, which is an empty list rather than an error.
    get_help_categories() returns (rep: helpCategoryList)
    # Files a customer support ticket into the public help center. It
    # creates the ticket (status Open, source portal) with the customer's message on
    # the description, then records that same message as the opening entry of the
    # ticket's conversation thread; the description carries it regardless, so failing
    # to write that entry loses nothing. Answers 201 with an opaque reference.
    # A deployment with no help center answers 404, one whose center has not installed
    # the Help model answers 503, and a body over 64 KiB answers 413 — in that order,
    # which is the order the route has always decided them in.
    post_help_tickets(req: helpTicketIntake) returns (rep: helpTicketFiled)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   helpArticleList.Data  help.helpArticleCard (list element)
#   helpCategoryList.Data  help.helpCategory (list element)
