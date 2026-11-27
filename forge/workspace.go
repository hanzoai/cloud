package forge

// workspace.go is where an org's code lives on the forge when the org is not
// one of the estate's own.
//
// [Owner] answers for the estate, from a CLOSED table, and it stays closed: an
// IAM org's name is never a forge coordinate. Every other org gets a namespace
// the PLATFORM makes for it on first need, and the only link between the two is
// a record the platform writes down once it has made one ([Book]). So the org
// still never selects a namespace by being spelled a certain way; the record,
// keyed by the validated org, is the whole of the mapping.
//
// # Who acts there, and what checks the person
//
// Cloud makes the namespace, its repositories and a run's keys as the machine
// ([Client.Machine]), and reads its code as the machine. A person acts there only
// through their seat ([Client.Enroll], member.go): an account cloud makes for
// them that cannot sign in, seated in the namespace's members team. Either way
// the access check is that the caller's validated org is the org the record
// names — which is exactly what [Find] and [Provision] read, and nothing else can.
//
// # The three properties a made namespace keeps
//
//	NEVER ADOPTED   the record is written only after the machine's own create of a
//	                name that did not exist returned success ([Client.CreateOrg]). A
//	                name that is taken — by the estate, a stranger, anyone — moves
//	                to the next number and is never touched.
//	CLOSED          the namespace is private, and every repository made in it is
//	                private with Actions off ([Client.Create]).
//	NOT DELIVERED   [Org] reads the estate's tables and nothing else, so a push
//	                from a made namespace rebuilds nobody's applications and wakes
//	                no runner. A name either table knows, or one the estate goes
//	                by elsewhere, is never made ([Reserved]).

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Workspace is where an org keeps its code on the forge.
type Workspace struct {
	// Owner is the forge namespace. Empty is an org with none yet.
	Owner string
	// Made says the platform made this namespace for the org: the machine makes
	// and reads there, people act through their seats, and the org's own record
	// is the access check. False is the estate, where the forge's ACL applied to
	// the person (Sudo) decides, exactly as it always has.
	Made bool
}

// Book is where the platform writes down the namespaces it made. One org holds
// at most one, and one namespace belongs to at most one org.
type Book interface {
	// Read answers the namespace recorded for org, "" when there is none.
	Read(ctx context.Context, org string) (string, error)
	// Write records owner as org's namespace unless org already holds one, and
	// answers the one org holds after the write — the earlier one when two
	// writers raced. An owner another org holds is [ErrExists].
	Write(ctx context.Context, org, owner string) (string, error)
}

// makeTries bounds the numbering. Past it an org has met twenty taken names in
// a row, which is somebody squatting the pattern rather than a collision.
const makeTries = 20

// strays is how many random names follow the numbered ones ([stray]). A
// squatter can take `acme` through `acme-20` in advance; it cannot take names
// nobody has drawn yet, so an org whose numbered names are all gone still gets a
// workspace, and a forge answering every name as taken still ends the search.
const strays = 5

// nameMax is the forge's own bound on a user or org name.
const nameMax = 40

// provisionBudget bounds one making, whoever asked for it and however long they
// wait: every numbered create, its read-back and its record.
const provisionBudget = 90 * time.Second

// Find is org's workspace: the estate's from the closed table, else the one the
// book records, else none (an empty Owner and no error).
//
// org is a VALIDATED org, the principal's own — never a value a caller sent.
//
// A recorded namespace is checked before it is answered: one that is not a path
// segment, or that either estate table knows, is refused rather than acted in,
// so a record that was corrupted or written by hand still cannot reach the
// estate.
func Find(ctx context.Context, book Book, org string) (Workspace, error) {
	if owner, err := Owner(org); err == nil {
		return Workspace{Owner: owner}, nil
	}
	if strings.TrimSpace(org) == "" {
		return Workspace{}, fmt.Errorf("%w: %q", ErrNoOwner, org)
	}
	owner, err := book.Read(ctx, org)
	if err != nil {
		return Workspace{}, fmt.Errorf("forge: read the workspace of %q: %w", org, err)
	}
	if owner == "" {
		return Workspace{}, nil
	}
	if err := validSegment(owner); err != nil {
		return Workspace{}, fmt.Errorf("forge: the workspace recorded for %q: %w", org, err)
	}
	if Reserved(owner) {
		return Workspace{}, fmt.Errorf("forge: the workspace recorded for %q is the estate's namespace %q", org, owner)
	}
	return Workspace{Owner: owner, Made: true}, nil
}

// Answered is a made workspace as the project store answered it from another
// process, checked on this side: a namespace the record names must be a plain
// namespace name the estate does not hold ([Reserved]), and must say the
// platform made it. owner "" is an org with none. It is for an org [Owner] does not name; the
// estate's answer comes from the table, never across a process.
func Answered(owner string, made bool) (Workspace, error) {
	if owner == "" {
		return Workspace{}, nil
	}
	if !made {
		return Workspace{}, fmt.Errorf("forge: the workspace %q is not one the platform made", owner)
	}
	if !plain(owner) {
		return Workspace{}, fmt.Errorf("forge: the workspace %q is not a namespace name", owner)
	}
	if Reserved(owner) {
		return Workspace{}, fmt.Errorf("forge: the workspace %q is a name the estate keeps", owner)
	}
	return Workspace{Owner: owner, Made: true}, nil
}

// plain reports whether name is a forge namespace name: led by a letter or a
// digit, then letters, digits, '.', '_' and '-', no longer than the forge allows.
func plain(name string) bool {
	if name == "" || len(name) > nameMax {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return !strings.Contains(name, "..")
}

// Provision is [Find] that makes the workspace when org has none: a new private
// namespace, created by the machine under the first free name — the org's own,
// then `-2`, `-3`, … and past those a few random ones ([stray]) — and recorded
// only once the forge has said it made it.
//
// It is serialized per org within the process, so two first runs at once make
// one namespace: the second waits and reads the first's record. A second
// process racing it (an org moving between replicas) is settled by the book,
// whose Write answers the namespace recorded first; the loser's namespace, made a
// moment ago and holding nothing, is deleted rather than left behind.
//
// c must be the machine: creating a top-level namespace is a platform act with
// no person behind it.
func Provision(ctx context.Context, c *Client, book Book, org string) (Workspace, error) {
	if !c.machine {
		return Workspace{}, fmt.Errorf("forge: a workspace is made by the machine identity; %q may not make one", c.actor)
	}
	mu := orgLock(org)
	mu.Lock()
	defer mu.Unlock()
	// The making is the platform's and outlives the caller's patience. A create
	// cut short after the forge made the name, and before the book held it, would
	// leave a namespace no record reaches and push the org's name one number on,
	// until no name is left to try. So a caller that stops
	// waiting stops waiting, and the create and its record still land, for the
	// caller's next ask to find.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), provisionBudget)
	defer cancel()
	ws, err := Find(ctx, book, org)
	if err != nil || ws.Owner != "" {
		return ws, err
	}
	for n := 1; n <= makeTries+strays; n++ {
		name := candidate(org, n)
		if n > makeTries {
			if name, err = stray(org); err != nil {
				return Workspace{}, err
			}
		}
		if name == "" {
			return Workspace{}, fmt.Errorf("forge: %q yields no namespace name", org)
		}
		if Reserved(name) {
			continue
		}
		err := c.CreateOrg(ctx, name)
		if errors.Is(err, ErrExists) {
			continue // taken — by anyone, the estate included — and never adopted
		}
		if err != nil {
			return Workspace{}, err
		}
		kept, err := book.Write(ctx, org, name)
		if errors.Is(err, ErrExists) {
			// Recorded for another org, so it was not free after all whatever the
			// forge said; it is theirs, and this org takes the next name.
			continue
		}
		if err != nil {
			// Unrecorded, the namespace is nobody's: take it back rather than leave
			// a name no record can ever reach.
			_ = c.deleteOrg(context.WithoutCancel(ctx), name)
			return Workspace{}, fmt.Errorf("forge: record the workspace of %q: %w", org, err)
		}
		if kept != name {
			_ = c.deleteOrg(context.WithoutCancel(ctx), name)
		}
		return Find(ctx, book, org)
	}
	return Workspace{}, fmt.Errorf("forge: no free namespace for %q in %d tries", org, makeTries+strays)
}

// Reserved reports whether name is one the estate holds or keeps, and so is
// never a made workspace: a namespace a delivery is attributed from ([Org]),
// one an org is mapped to ([Owner]), or one of the estate's own accounts on
// GitHub and its brands' ([kept]). It is the one predicate every side of the
// workspace asks — the maker before it creates, [Find] before it answers a
// record, and a caller reading an answer that crossed a process boundary —
// which keeps every made namespace out of the delivery direction and every
// name the estate goes by out of a customer's hands.
func Reserved(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if _, ok := orgs[n]; ok {
		return true
	}
	if _, ok := owners[n]; ok {
		return true
	}
	return kept[n]
}

// kept is every name the estate goes by outside this forge's two tables: its
// GitHub orgs and its brands'. None is a namespace here today, and each is one
// the estate may open here, or one a person reading a clone address would take
// for the estate's — so a customer org spelled like one is numbered past it
// ([Provision]), exactly as a name the tables hold is.
//
// A declared set, like the tables beside it: adding a name is a deliberate edit,
// and a name already made for a customer before it was added stays theirs —
// [Find] then refuses to answer it, which stops their runs rather than handing
// the estate's name to a customer's work.
var kept = map[string]bool{
	"hanzoai": true, "hanzo": true, "hanzo-apps": true, "hanzo-inc": true, "hanzo-js": true,
	"hanzo-ds": true, "hanzo-docs": true, "hanzo-dart": true, "hanzobot": true, "hanzodao": true,
	"hanzoid": true, "hanzozt": true, "hanzo-community": true, "hanzo-templates": true,
	"hanzokv": true, "hanzos3": true, "hanzo-ml": true, "hanzoteam": true,
	"luxfi": true, "lux": true, "lux-apps": true, "luxdao": true, "luxstatic": true, "lux-docs": true,
	"luxwallet": true, "luxcpp": true,
	"zooai": true, "zoo": true, "zoo-apps": true, "zoo-labs": true, "zoo-docs": true,
	"zenlm": true, "zen-docs": true,
	"pars": true, "parsdao": true,
	"adnexus": true, "adnexustech": true,
	"osage": true, "bootnode": true,
	"zap-proto": true, "webby-ai": true, "charts": true,
}

// candidate is the n-th namespace name for org: the org's name folded to the
// forge's alphabet for n = 1, and `-n` after it for every other. "" when the org
// folds to nothing.
//
// The folding is lowercase letters and digits with single hyphens between them,
// which is inside what the forge accepts for a name and passes [validSegment].
func candidate(org string, n int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(org)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.TrimRight(b.String(), "-")
	if base == "" {
		return ""
	}
	suffix := ""
	if n > 1 {
		suffix = "-" + strconv.Itoa(n)
	}
	if len(base)+len(suffix) > nameMax {
		base = strings.TrimRight(base[:nameMax-len(suffix)], "-")
	}
	return base + suffix
}

// stray is a namespace name for org that nobody could have taken in advance:
// the org's folded name and eight random base-36 characters, cut to the forge's
// bound. "" when the org folds to nothing.
func stray(org string) (string, error) {
	base := candidate(org, 1)
	if base == "" {
		return "", nil
	}
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("forge: rng: %w", err)
	}
	suffix := []byte{'-'}
	for _, x := range b {
		suffix = append(suffix, alphabet[int(x)%len(alphabet)])
	}
	if len(base)+len(suffix) > nameMax {
		base = strings.TrimRight(base[:nameMax-len(suffix)], "-")
	}
	return base + string(suffix), nil
}

// locks serializes [Provision] per org within this process.
var locks sync.Map // org → *sync.Mutex

func orgLock(org string) *sync.Mutex {
	mu, _ := locks.LoadOrStore(org, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
