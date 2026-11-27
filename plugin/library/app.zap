# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package library

struct LibraryItem {
    ID       text  @0
    Kind     text  @8
    Source   text  @16
    Name     text  @24
    Mime     text  @32
    Bytes    i64   @40
    Width    i64   @48
    Height   i64   @56
    Seconds  f64   @64
    Pages    i64   @72
    Owner    text  @80
    Mine     bool  @88
    Shared   bool  @89
    Project  text  @96
    Ref      text  @104
    Origin   text  @112
    URL      text  @120
    Download text  @128
    Preview  text  @136
    Expires  text  @144
    Created  text  @152
    Updated  text  @160
    By       text  @168
    Upload   bytes @176
}

struct itemPage {
    Data list<bytes> @0
    Next text        @8
}

struct itemPatch {
    ID      text       @0
    Name    list<text> @8
    Shared  list<bool> @16
    Project list<text> @24
    Commit  list<bool> @32
}

struct itemRef {
    ID text @0
}

struct keepIn {
    Name    text  @0
    Mime    text  @8
    Data    bytes @16
    Bytes   i64   @24
    Source  text  @32
    Kind    text  @40
    Ref     text  @48
    Origin  text  @56
    Project text  @64
    Preview bytes @72
    Width   i64   @80
    Height  i64   @88
    Seconds f64   @96
    Pages   i64   @104
    Key     text  @112
}

struct listQuery {
    Kind    text @0
    Source  text @8
    Ref     text @16
    Project text @24
    Q       text @32
    Since   text @40
    Until   text @48
    Scope   text @56
    Limit   i64  @64
    Cursor  text @72
}

struct unownedQuery {
    Limit i64 @0
}

interface library {
    # Deletes one of your items and its bytes. Only its owner deletes an
    # item.
    delete_library_by_id(req: itemRef)
    # Lists what you made and what members of your organization shared
    # with you, newest first. Narrow it by kind, source, ref, project, a name search
    # and a time range; `scope` mine keeps only yours and shared only what others
    # shared. Each item carries signed URLs that work for an hour.
    get_library(req: listQuery) returns (rep: itemPage)
    # Reads one item with fresh signed URLs. An item that is not yours and
    # was not shared with your organization answers 404, as one that does not exist.
    get_library_by_id(req: itemRef) returns (rep: LibraryItem)
    # ListUnowned reads, for a SuperAdmin alone, the organization's items that record
    # no owner. Such an item is nobody's: no member lists it, reads it
    # or changes it, and this view neither changes it nor signs its bytes. Every
    # read of it is written to the audit trail.
    get_library_unowned(req: unownedQuery) returns (rep: itemPage)
    # Renames one of your items, shares it with your organization or
    # stops sharing it, moves it to a project, or commits a reserved upload. Only
    # its owner changes an item; a member's shared item answers 404 here as one
    # that does not exist.
    patch_library_by_id(req: itemPatch) returns (rep: LibraryItem)
    # Keeps one thing you made in your Library: its bytes go to your own
    # prefix of the Library's store, and it is listed to you alone until you share
    # it. Send `data` up to 10 MiB inline, or send `bytes` up to 2 GiB to reserve
    # a signed `upload` PUT and finish it with `PATCH /v1/library/:id` `{"commit": true}`.
    # A save with the `key` of one already kept answers that item.
    post_library(req: keepIn) returns (rep: LibraryItem)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   LibraryItem.Upload  client.LibraryUpload
#   itemPage.Data  client.LibraryItem (list element)
