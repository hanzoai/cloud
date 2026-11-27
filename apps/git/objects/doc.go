// Package objects keeps a git repository's objects, refs, shallow list, config and
// index, so a replica holds no repository on its disk.
//
// One Store is one org's repositories. Object bytes are packfiles in the blob store
// (S3 in production), and the org's database (a schema of hanzo-sql, a file on SQLite)
// keeps what is needed to find an object and to say what a repository is: which pack
// each object is in and where (entries), the packs a repository has (packs), its refs,
// shallow list and config. A repository is a row of repos; everything else carries that
// row's id.
//
// # A pack is what a push sent
//
// A push's pack is read once (Ingest: every object named, every delta resolved, a thin
// pack's bases found in the repository, what each object refers to noted) and uploaded
// as it came, under a name that is its checksum. Objects a client sent as deltas stay
// deltas; objects it sent whole stay whole. Nothing is inflated and deflated again to
// keep it. Objects written one at a time (a client-less push, a copy of a directory)
// are collected into a pack of their own, written whole.
//
// A clone copies the stored bytes into the pack it answers: a whole object as it is, a
// delta as a REF_DELTA when its base is also sent. Only a delta whose base is not sent
// is applied and deflated again.
//
// # A write is one transaction, and a pack nothing points at is garbage
//
// Begin takes the repository's lock (Dialect.Serialize: pushes to one repository queue
// and pushes to another do not). Attaching a pack inserts its row and a row for each
// object it holds that the repository did not, and refs move by compare and set; all
// of it lands at Commit or not at all. The pack's bytes are in the blob store before
// that, so a crash, a refused push or a lost race leaves a pack that nothing points at.
// Nothing reads a pack the database does not name, and Collect removes it once it is
// old enough that no upload can still be on its way to a commit.
//
// # Compaction
//
// Every push adds a pack, so a busy repository collects many small ones. Compact merges
// them into one and swaps the index rows in one transaction; the packs it replaces are
// deleted after a grace period, long enough for a reader that resolved its locations
// before the swap to finish reading them.
//
// A Tx is not for concurrent use; neither is any Repo that was begun on one (database/sql
// serialises a transaction's statements, and an open result set would hold the next one
// off). A Repo on the pool may be used from several goroutines.
package objects
