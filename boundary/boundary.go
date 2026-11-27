package boundary

// Names is the closed set, in one order. Callers derive their own tables from
// it rather than restating it; apps/sandbox pins its table against this list
// and the CLI's installer walks it directly, so a boundary added here is a
// boundary both ends see or a build that fails.
//
// The order is alphabetical, which is what apps/sandbox derives by sorting its
// own table — the pinning test compares the two element by element, so this is
// not a free choice. runc sits among them rather than last; it is the only one
// that is not a boundary at all, and the table says so in its facts instead.
var Names = []string{"gvisor", "kata-clh", "kata-fc", "runc", "visor"}
