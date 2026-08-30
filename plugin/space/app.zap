# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package space

struct driveIn {
    Space text @0
    Name  text @8
}

struct driveItem {
    Name text @0
}

struct driveList {
    Space  text        @0
    Drives list<bytes> @8
    Total  i64         @16
}

struct driveRef {
    Space text @0
    Drive text @8
}

struct fileList {
    Space  text        @0
    Drive  text        @8
    Folder text        @16
    Files  list<bytes> @24
    Total  i64         @32
}

struct fileRef {
    Space text @0
    Drive text @8
    File  text @16
}

struct fileURL {
    URL    text @0
    Method text @8
    File   text @16
    Expiry i64  @24
}

struct folderRef {
    Space     text @0
    Drive     text @8
    Folder    text @16
    Recursive text @24
}

struct spaceHealth {
    Service text @0
    Status  text @8
    Ready   bool @16
    Presign bool @17
    Error   text @24
}

struct spaceIn {
    Name text @0
}

struct spaceItem {
    Name      text @0
    CreatedAt i64  @8
}

struct spaceList {
    Spaces list<bytes> @0
    Total  i64         @8
}

struct spaceRef {
    Space text @0
}

interface space {
    # Removes an EMPTY drive and answers 204.
    # A drive holding files is 409 rather than a cascade: deleting an org's files
    # behind a single drive call is not a thing this surface will do silently. A
    # drive or space the caller's org does not own is the same 404 an unknown name
    # gives.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing deleted, and the debit lands only once the
    # drive is gone.
    delete_space_by_space_drives_by_drive(req: driveRef)
    # Removes one file and answers 204.
    # It removes ONE file and never a folder: a name that looks like a folder is
    # refused, because a folder is emergent from the names beneath it and deleting
    # one would have to mean deleting them. The name is path-cleaned first, so the
    # delete cannot reach outside the drive it names, and a space the caller's org
    # does not own is the same 404 an unknown name gives.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing deleted, and the debit lands only once the
    # file is gone.
    delete_space_by_space_drives_by_drive_files_by_wildcard1(req: fileRef)
    # Lists a space's drives.
    # Listing the drives IS listing the space's root folder, because a drive is the
    # first segment of a key and nothing else — so the two can never disagree the way
    # a drives table and the keys under it would. A space the caller's org does not
    # own is the same 404 an unknown name gives.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing read, and the debit lands only once the
    # listing has succeeded.
    get_space_by_space_drives(req: spaceRef) returns (rep: driveList)
    # Lists one folder level of a drive.
    # Folder-style by default: sub-folders come back as folder entries, which is the
    # file-manager view. `?recursive=true` lists every file flat under the folder
    # instead. Names are RELATIVE to `?folder=`, and the listing is bounded so a huge
    # drive cannot exhaust memory — Total is what came back, not what the drive holds.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing read, and the debit lands only once the
    # listing has succeeded.
    get_space_by_space_drives_by_drive_files(req: folderRef) returns (rep: fileList)
    # Mints a URL the caller downloads the file from DIRECTLY.
    # The bytes never pass through this binary and the store credential never leaves
    # the server: the URL is signed against the PUBLIC host, scoped to exactly this
    # space, drive and file, and expires. It carries a content disposition of
    # attachment naming the file, so a browser following it saves the file rather
    # than rendering it in place. A deployment with no public endpoint configured
    # cannot mint one and answers 503 rather than a URL that will not work.
    # Billed per call — for MINTING the URL, which is the work this operation does;
    # the download that follows comes straight from the store and is not seen here.
    # The balance is checked BEFORE anything is touched, so an unfunded org is
    # refused with no URL issued.
    get_space_by_space_drives_by_drive_files_by_wildcard1(req: fileRef) returns (rep: fileURL)
    # Health reports whether this deployment can serve spaces, drives and files.
    # It is a REAL probe rather than a constant: 200 when object-store credentials
    # are present, so the store is reachable in principle, and 503 with the reason
    # when they are not. It is deliberately NOT gated — liveness has to be probe-able
    # without a token — so it is the one operation here that names no space and bills
    # nothing.
    get_space_health() returns (rep: spaceHealth)
    # Lists the caller org's own spaces.
    # Only the caller's: every space is physically named under a per-org prefix and
    # the listing strips that prefix, so another org's spaces are not in the answer at
    # all. Another org's space is not refused but INVISIBLE, so this cannot be used to
    # learn that a name is taken elsewhere.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing done, and the debit lands only once the
    # work has succeeded.
    get_space_spaces() returns (rep: spaceList)
    # Makes a new drive in a space and answers 201 with it.
    # A drive is a PREFIX and not a bucket, so making one writes a zero-byte marker
    # at "<name>/" — which is what makes an empty drive visible to a listing that has
    # no other key to find. A name already taken in the space is 409.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing created, and the debit lands only once the
    # drive exists.
    post_space_by_space_drives(req: driveIn) returns (rep: driveItem)
    # Makes a new space for the caller's org and answers 201 with it.
    # The one bucket a space's files live in is derived from the caller's VALIDATED
    # org, so an org can only ever create inside its own namespace and no request
    # field can redirect that. A name already taken in the org is 409.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing created, and the debit lands only once the
    # space exists.
    post_space_spaces(req: spaceIn) returns (rep: spaceItem)
    # Mints a URL the caller uploads the file to DIRECTLY.
    # The bytes never pass through this binary and the store credential never leaves
    # the server: the URL is signed against the PUBLIC host, scoped to exactly this
    # space, drive and file, and expires. Writing into a folder that does not exist
    # is fine and creates nothing — a folder is emergent from "/" in the name. A
    # deployment with no public endpoint configured cannot mint a URL and answers 503
    # rather than one that will not work.
    # Billed per call — for MINTING the URL, which is the work this operation does;
    # the upload that follows goes straight to the store and is not seen here. The
    # balance is checked BEFORE anything is touched, so an unfunded org is refused
    # with no URL issued.
    put_space_by_space_drives_by_drive_files_by_wildcard1(req: fileRef) returns (rep: fileURL)
}

# ---------------------------------------------------------------------
# 10 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   driveList.Drives  space.driveItem (list element)
#   fileList.Files  space.fileItem (list element)
#   spaceList.Spaces  space.spaceItem (list element)
