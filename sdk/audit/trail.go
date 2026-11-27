package audit

// The TRAIL: every chain a deployment keeps, verified one by one.
//
// A deployment writes ONE chain: the holder's (admin's), which every other process
// appends to over the plane through a Remote recorder. Two writers cannot share a
// hash chain unless the database orders them, they can only fork it (see Name), so
// one process writes it at a time: on SQLite the holder, on hanzo-sql whichever
// replica's holder takes the chain lock, which is the same chain whichever it is.
//
// The chains are still a FAMILY. Before the trail had one holder every
// process wrote its own, "audit" for the host and "audit-<app>" for each plugin
// child — 128 chains and 1.7 GB live — and those files are history now: never
// written again, still evidence, still verified here. A reader that walks only the
// chain it holds reports 1 of N and says nothing at all about the rest.
//
// TWO PROPERTIES THIS FILE EXISTS FOR:
//
//   - The answer is a SET, not a boolean. One bool cannot describe N independent
//     chains, and folding them loses which one broke.
//   - A chain that could not be READ is reported as such and never as a pass. One
//     unreadable chain must not vanish into a green summary — that is the same
//     defect one level up, and on the pure-Go build below it is the common case
//     rather than a corner.
//
// READ-ONLY, AND IN PLACE. Chains are opened, walked and closed; nothing here
// migrates, writes or MOVES a file. cek derives a chain's key as
// HKDF(master, "hanzo/cek/v1/" + ns + "/" + subsystem), so the FILENAME IS KEY
// MATERIAL: renaming audit-iam.db makes it undecryptable, which presents as an
// empty chain rather than as an error. On hanzo-sql the family is the registry's:
// the stores the system namespace keeps whose names are the family's, not the files
// of a directory.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
)

// prefix is the family's one spelling: the host's chain IS it, and every child's
// chain is it plus the app. Spelled once so Name and member cannot disagree about
// what belongs to the family.
const prefix = "audit"

// Name is the chain a process that holds the trail writes. A fused host ("cloud",
// or an unnamed process) keeps the canonical "audit" chain; a plugin child holding
// it — admin — writes "audit-<app>". Every other process writes no chain at all.
//
// ONE CHAIN, ONE WRITER. audit_log.seq is a gapless chain position and each row's
// prev_hash seals the one before it, so the chain is only meaningful while a single
// process appends to it. Every process used to open the same file, which was
// harmless while cloud was one binary and became a total write outage the moment
// subsystems became plugin CHILD PROCESSES: each recovers its own in-memory nextSeq
// from the shared file, then they all race for one PRIMARY KEY. Observed on
// v1.801.313 as "audit: persist: UNIQUE constraint failed: audit_log.seq" at
// ~94/minute, and because the audit gate fails CLOSED (correctly), every POST in
// the fleet was refused.
func Name(proc string) string {
	if proc == "" || proc == "cloud" {
		return prefix
	}
	return prefix + "-" + proc
}

// member reports whether a store name is a chain of this family. It is the inverse
// of Name and lives beside it, because enumeration and naming disagreeing is how a
// reader either misses a live chain or walks a store that is not one — "auditlog"
// is a subsystem name that starts with the prefix and is not a chain.
func member(name string) bool {
	return name == prefix || strings.HasPrefix(name, prefix+"-")
}

// Trail is the family verified: one Integrity per chain, in name order, plus the
// counts a reader needs to answer "is the audit trail intact?" without folding
// three states into one.
//
// There is deliberately NO single ok. Intact+Broken+Unread == len(Chains), and a
// caller that wants one answer must decide for itself what an UNREAD chain means to
// it — which is the question a boolean was silently answering "yes" to.
type Trail struct {
	// Chains is every chain of the family, in name order.
	Chains []Integrity `json:"chains"`
	// Intact counts the chains that verified end to end.
	Intact int `json:"intact"`
	// Broken counts the chains whose hash chain fails; each names where.
	Broken int `json:"broken"`
	// Unread counts the chains that could not be read. NOT a pass: nothing is known
	// about their contents.
	Unread int `json:"unread"`
	// Records is the total number of records walked across every chain that was
	// read. It counts nothing for an unread chain.
	Records uint64 `json:"records"`
}

// Trail verifies EVERY chain of the audit family this deployment keeps and reports
// each one separately.
//
// The chains are found where namespace puts them, beside this recorder's own, never
// at a path spelled here: a second spelling of the layout is a reader that looks in
// the wrong directory and reports a clean, empty trail. (The pre-cek location, the
// data root itself, is exactly that mistake made once already.) On hanzo-sql they
// are found in the registry of stores.
//
// Failure is per chain, not per trail: a chain that cannot be read is reported
// Unread with the reason, and the walk continues. Only a failure to enumerate at all
// is an error — including finding NO chain, because a deployment that keeps a trail
// always has at least this recorder's own, so zero chains means the layout moved and
// an empty Trail would be a fabricated pass.
//
// Chains are opened one at a time and closed before the next, so a 128-chain family
// costs one extra file handle rather than 128, and they are walked in name order, so
// the answer is stable and a cancelled walk is reproducible. Cancelling ctx stops the
// walk; the chains not yet reached are simply absent, never reported as passing.
func (r *Recorder) Trail(ctx context.Context) (Trail, error) {
	if r.owner != nil {
		return Trail{}, r.held()
	}
	names, err := r.family()
	if err != nil {
		return Trail{}, err
	}

	t := Trail{Chains: make([]Integrity, 0, len(names))}
	for _, name := range names {
		iv := r.verifyChain(ctx, name)
		switch iv.Verdict {
		case Intact:
			t.Intact++
		case Broken:
			t.Broken++
		default:
			t.Unread++
		}
		t.Records += iv.Count
		t.Chains = append(t.Chains, iv)
	}
	return t, nil
}

// verifyChain walks one chain of the family. Every failure — a key that does not
// open the file, a file that is not a chain, a read that dies mid-walk — becomes an
// Unread verdict carrying the reason, because the caller is asking about N chains
// and one bad chain must not cost it the other N-1.
func (r *Recorder) verifyChain(ctx context.Context, name string) Integrity {
	unread := func(reason string) Integrity {
		return Integrity{Name: name, Verdict: Unread, BrokenAt: -1, Reason: reason}
	}

	// This process's OWN chain is walked through the handle it already holds. That
	// is not an optimisation: it is the only reading that is correct on every build
	// (see below), and it is the freshest — the live handle sees appends a second
	// opener would not.
	if name == r.name {
		read, _ := r.view()
		iv, err := walk(ctx, read, name)
		if err != nil {
			return unread(err.Error())
		}
		return iv
	}

	// On hanzo-sql a sibling is a schema any replica reads: no file, no envelope, and
	// no process holds it open in a way that reading could disturb. The registry
	// named it, so opening it makes nothing. A chain is there once it has moved, and
	// the history chains move one at a time, so which chains are is asked of each.
	if r.onServer(name) {
		db, err := locus.Open(namespace.System(), name, r.dir)
		if err != nil {
			return unread(err.Error())
		}
		defer func() { _ = db.Close() }()
		iv, err := walk(ctx, db, name)
		if err != nil {
			return unread(err.Error())
		}
		return iv
	}

	// A SIBLING chain belongs to ANOTHER LIVE PROCESS, and whether it can be read at
	// all is a property of the storage engine this binary linked.
	//
	// With the C codec the file stays in place and ordinary SQLite sharing applies,
	// so a reader is a reader. WITHOUT it, cek falls back to the pure-Go envelope,
	// which decrypts the whole file into a handle-private RAM copy and RE-ENCRYPTS
	// THAT COPY BACK OVER THE REAL PATH ON Close (envelope.go: "SINGLE WRITER per
	// file ... last close wins"). So merely opening a sibling to read it would seal a
	// stale snapshot over a chain another process is still appending to — a
	// verification that destroys the evidence it was asked about. Nothing in cek or
	// hanzoai/sqlite offers a read-only open that skips the seal.
	//
	// This is reachable, not theoretical: mk/fleet.mk's `dist` builds every published
	// plugin CGO_ENABLED=0 on purpose, so a fleet resolving plugins through the
	// release index runs exactly this build.
	//
	// So the honest answer is that the chain is UNREAD. It fails safe and it fails
	// LOUD: the surface reports how many chains it could not open and why, which an
	// operator can act on — where the alternatives are corrupting the trail, or the
	// defect this file exists to end, calling one chain the whole trail.
	if !sqlitedrv.CodecLinked() {
		return unread("not read: this build links no SQLCipher codec, so opening a chain another process holds would seal a private copy over it; only " + r.name + " is readable here")
	}

	db, err := locus.Open(namespace.System(), name, r.dir)
	if err != nil {
		return unread(err.Error())
	}
	defer func() { _ = db.Close() }()

	// A chain whose file vanished between enumeration and here is RE-CREATED empty by
	// Open (it creates what it names), and an empty file has no audit_log — so the
	// walk fails and this reports Unread rather than an intact chain of zero records.
	// That ordering is the guard: never conclude "empty" from a query that could not
	// run.
	iv, err := walk(ctx, db, name)
	if err != nil {
		return unread(err.Error())
	}
	return iv
}

// family lists the chains of the family this deployment keeps, in name order: the
// registry's on hanzo-sql, the directory's on SQLite. The recorder's own is always
// among them.
func (r *Recorder) family() ([]string, error) {
	// A chain is a file, a schema, or both: the history chains move to hanzo-sql one
	// at a time and a moved one leaves its file where it was, so with a server the
	// family is the union of what the registry and the directory name.
	names, err := chains(r.dir)
	if locus.DSN() == "" {
		if err != nil {
			return nil, err
		}
	} else {
		subs, serr := locus.Subsystems(namespace.System())
		if serr != nil {
			return nil, fmt.Errorf("audit: list chains: %w", serr)
		}
		for _, name := range subs {
			if member(name) && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	// The LIVE chain has no sealed file until Close writes the envelope, so the
	// glob can never see this process's own — measured: a recorder that has
	// appended all day leaves nothing on disk under its own name. It is walked
	// through the handle, so it joins the family by name here; without this the
	// trail reports every sibling and silently omits the one chain this process is
	// responsible for.
	if !slices.Contains(names, r.name) {
		names = append(names, r.name)
	}
	sort.Strings(names)
	return names, nil
}

// onServer reports whether the chain is a schema on hanzo-sql now.
func (r *Recorder) onServer(name string) bool {
	if _, d := r.view(); d.Postgres() && name == r.name {
		return true
	}
	if locus.DSN() == "" {
		return false
	}
	place, err := locus.Where(namespace.System(), name, r.dir)
	return err == nil && place == locus.OnServer
}

// chains lists the family's store names under dir, in order. It reads the DIRECTORY
// namespace renders for the system namespace — resolved through namespace.Path so
// the layout is stated in exactly one place, the same place cek.Open reads it — and
// keeps only the names Name could have produced.
func chains(dir string) ([]string, error) {
	// Path validates dir and renders {dir}/orgs/_platform/{sub}.db; the chains are
	// this one's siblings. Asking for a member of the family rather than composing
	// the directory by hand is what keeps this from being a second layout.
	anchor, err := namespace.Path(dir, namespace.System(), prefix)
	if err != nil {
		return nil, fmt.Errorf("audit: locate chains: %w", err)
	}
	// The family directory must EXIST: cek creates it at the first Open, so a
	// dir without it is not a fresh deployment, it is the wrong dir — and a
	// quiet empty answer there is a fabricated clean trail.
	fam := filepath.Dir(anchor)
	if _, err := os.Stat(fam); err != nil {
		return nil, fmt.Errorf("audit: no chain family under %s — the layout moved: %w", dir, err)
	}
	files, err := filepath.Glob(filepath.Join(fam, "*.db"))
	if err != nil {
		return nil, fmt.Errorf("audit: list chains: %w", err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".db")
		if member(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
