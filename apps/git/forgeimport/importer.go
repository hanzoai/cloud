package forgeimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/apps/git/objects"
)

// Config is an importer's wiring. Everything it reaches is a value handed in, so a run
// from a pod, a workstation or a test is the same code.
type Config struct {
	Registry *Registry
	// Stores answers the objects store of an org.
	Stores func(org string) (*objects.Store, error)
	// Org maps a forge namespace to the org whose store holds it. The default is the
	// closed table of forge.Org (hanzoai is the hanzo org's), else the namespace itself.
	Org func(owner string) string
	// Remote builds the connection to one forge repository.
	Remote func(owner, repo string) *Remote
	// Workers is how many repositories are copied at once (default 4). Memory is
	// Workers x MaxPack, a pack being held whole.
	Workers int
	// MaxPack is the largest pack taken in one fetch (default 2 GiB).
	MaxPack int64
	// MaxAttempts is how often a failing repository is tried (default 3).
	MaxAttempts int64
	// Resync also compares the refs of repositories already done and fetches the
	// difference of those that moved.
	Resync bool
	// Verify says how far a copy is checked; see Verify.
	Verify Verify
	// Holder names this runner in the lease.
	Holder string
	Log    *slog.Logger
}

// StoreID is a repository's id in its org's store. The owner is in it because several
// forge namespaces belong to one org (hanzoai and hanzo-inc are both hanzo's), and two
// of them hold a repository of the same name.
func StoreID(owner, repo string) string { return owner + "/" + repo }

// Importer copies forge repositories into the store.
type Importer struct{ c Config }

// New checks the config and applies the defaults.
func New(c Config) (*Importer, error) {
	if c.Registry == nil || c.Stores == nil || c.Remote == nil {
		return nil, errors.New("forgeimport: a registry, stores and a remote are required")
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.MaxPack <= 0 {
		c.MaxPack = 2 << 30
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.Holder == "" {
		c.Holder = "importer"
	}
	if c.Org == nil {
		c.Org = DefaultOrg
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Importer{c: c}, nil
}

// Summary is what a run did.
type Summary struct {
	Done, Failed, Unchanged int
	Bytes                   int64
}

const leaseName = "forge-import"
const leaseTTL = 90 * time.Second

// Run copies every repository the registry has work for, smallest first. One runner at a
// time holds the lease; a second returns at once with ErrLeased.
func (im *Importer) Run(ctx context.Context) (Summary, error) {
	var sum Summary
	reg := im.c.Registry
	ok, err := reg.Lease(ctx, leaseName, im.c.Holder, leaseTTL)
	if err != nil {
		return sum, err
	}
	if !ok {
		return sum, ErrLeased
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		t := time.NewTicker(leaseTTL / 3)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if ok, err := reg.Lease(ctx, leaseName, im.c.Holder, leaseTTL); err != nil || !ok {
					cancel(ErrLeased)
					return
				}
			}
		}
	}()
	defer func() {
		cancel(nil)
		<-renewed
		reg.Release(context.Background(), leaseName, im.c.Holder)
	}()

	work, err := reg.Work(ctx, im.c.MaxAttempts, im.c.Resync)
	if err != nil {
		return sum, err
	}
	jobs := make(chan Row)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range im.c.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for x := range jobs {
				res := im.One(ctx, x)
				mu.Lock()
				switch {
				case res.State == Failed:
					sum.Failed++
				case res.unchanged:
					sum.Unchanged++
				default:
					sum.Done++
					sum.Bytes += res.Bytes
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, x := range work {
		select {
		case jobs <- x:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err := context.Cause(ctx); err != nil && ctx.Err() != nil {
		return sum, err
	}
	return sum, nil
}

// ErrLeased is a run that does not hold the lease: another runner has it, or this one
// lost it.
var ErrLeased = errors.New("forgeimport: another runner holds the lease")

// DefaultOrg maps a forge namespace to an org name for the store: the org that owns it
// (forge.Org) is applied by the command; with no table the namespace names its own org.
func DefaultOrg(owner string) string { return strings.ToLower(owner) }

type outcome struct {
	Row
	unchanged bool
}

// One copies a single repository and records the result. It never returns an error: a
// failure is the row's state.
func (im *Importer) One(ctx context.Context, x Row) outcome {
	reg := im.c.Registry
	log := im.c.Log.With("repo", x.Owner+"/"+x.Repo)
	if err := reg.Start(ctx, x.Owner, x.Repo); err != nil {
		x.State, x.Error = Failed, err.Error()
		return outcome{Row: x}
	}
	started := time.Now()
	res, err := im.copy(ctx, x)
	if err != nil {
		res.State, res.Error = Failed, err.Error()
		log.Warn("forgeimport failed", "err", err)
	} else {
		res.State, res.Error = Done, ""
		log.Info("forgeimport", "refs", res.Refs, "objects", res.Objects, "bytes", res.Bytes,
			"unchanged", res.unchanged, "verify", res.VerifyHow, "took", time.Since(started).Round(time.Millisecond).String())
	}
	if ferr := reg.Finish(context.WithoutCancel(ctx), res.Row); ferr != nil {
		log.Error("forgeimport: recording the outcome failed", "err", ferr)
	}
	return res
}

// digest names a ref set: sorted "name value" lines and HEAD, hashed.
func digest(a *Advert) string {
	names := make([]string, 0, len(a.Refs))
	for n := range a.Refs {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s %s\n", n, a.Refs[n])
	}
	fmt.Fprintf(h, "HEAD %s\n", a.Head)
	return hex.EncodeToString(h.Sum(nil))
}

func (im *Importer) copy(ctx context.Context, x Row) (outcome, error) {
	org := im.c.Org(x.Owner)
	x.Org, x.StoreID = org, StoreID(x.Owner, x.Repo)
	out := outcome{Row: x}
	remote := im.c.Remote(x.Owner, x.Repo)
	remote.MaxPack = im.c.MaxPack
	adv, err := remote.Refs(ctx)
	if err != nil {
		return out, err
	}
	sha := digest(adv)
	store, err := im.c.Stores(org)
	if err != nil {
		return out, err
	}
	has, err := store.Has(ctx, x.StoreID)
	if err != nil {
		return out, err
	}
	if has && x.State == Done && x.LastSHA == sha {
		out.unchanged, out.LastSHA = true, sha
		return out, nil
	}

	var in *objects.Incoming
	var full bool
	var packed int64
	wants := distinct(adv.Refs)
	var haves []plumbing.Hash
	if has {
		repo := store.Repo(ctx, x.StoreID)
		if wants, err = repo.Absent(wants); err != nil {
			return out, err
		}
		haves = tips(ctx, store, x.StoreID)
	} else {
		full = true
	}
	if len(wants) > 0 {
		pack, err := remote.Fetch(ctx, adv, wants, haves)
		if err != nil {
			return out, err
		}
		packed = PackObjects(pack)
		if in, err = store.Ingest(ctx, x.StoreID, bytes.NewReader(pack)); err != nil {
			return out, fmt.Errorf("ingest: %w", err)
		}
	}

	head := strings.TrimPrefix(adv.Head, "refs/heads/")
	if head == "" || head == adv.Head {
		head = "main"
	}
	var tx *objects.Tx
	if has {
		tx, err = store.Begin(ctx, x.StoreID)
	} else if tx, err = store.Make(ctx, x.StoreID, head); errors.Is(err, objects.ErrExists) {
		tx, err = store.Begin(ctx, x.StoreID)
		full = false
	}
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if in != nil {
		if err := tx.Attach(in); err != nil {
			return out, err
		}
	}
	if err := setRefs(tx, adv, head); err != nil {
		return out, err
	}
	tipMap := make(map[plumbing.ReferenceName]plumbing.Hash, len(adv.Refs))
	for n, h := range adv.Refs {
		tipMap[plumbing.ReferenceName(n)] = h
	}
	if err := tx.Connected(tipMap); err != nil {
		return out, fmt.Errorf("the copy is not connected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}

	repo := store.Repo(ctx, x.StoreID)
	n, err := repo.Count()
	if err != nil {
		return out, err
	}
	size, err := store.Size(ctx, x.StoreID)
	if err != nil {
		return out, err
	}
	out.Refs, out.Objects, out.Bytes, out.LastSHA = int64(len(adv.Refs)), n, size, sha
	how, err := im.c.Verify.run(ctx, store, x.StoreID, adv, n, packed, full, size)
	if err != nil {
		return out, fmt.Errorf("verify: %w", err)
	}
	out.Verified, out.VerifyHow = time.Now().Unix(), how
	return out, nil
}

// setRefs makes the repository's refs the advertised ones: moved or new ones are set,
// ones the forge no longer has are removed, and HEAD names the forge's branch.
func setRefs(tx *objects.Tx, adv *Advert, head string) error {
	have, err := tx.References()
	if err != nil {
		return err
	}
	same := map[string]bool{}
	for _, r := range have {
		name := r.Name().String()
		if name == "HEAD" {
			continue
		}
		want, ok := adv.Refs[name]
		switch {
		case !ok:
			if err := tx.RemoveReference(r.Name()); err != nil {
				return err
			}
		case r.Type() == plumbing.HashReference && r.Hash() == want:
			same[name] = true
		}
	}
	for name, h := range adv.Refs {
		if same[name] {
			continue
		}
		if err := tx.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), h)); err != nil {
			return fmt.Errorf("ref %s: %w", name, err)
		}
	}
	return tx.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(head)))
}

func distinct(refs map[string]plumbing.Hash) []plumbing.Hash {
	seen := map[plumbing.Hash]bool{}
	var out []plumbing.Hash
	for _, h := range refs {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// tips are the commits the store already has refs at, up to 256, sent as haves so a
// re-sync fetches only the difference.
func tips(ctx context.Context, s *objects.Store, id string) []plumbing.Hash {
	refs, err := s.Repo(ctx, id).References()
	if err != nil {
		return nil
	}
	m := map[string]plumbing.Hash{}
	for _, r := range refs {
		if r.Type() == plumbing.HashReference {
			m[r.Name().String()] = r.Hash()
		}
	}
	out := distinct(m)
	if len(out) > 256 {
		out = out[:256]
	}
	return out
}
