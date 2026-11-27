package forgeimport

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/apps/git/objects"
)

// Verify says how far a copy is checked once it is committed. Every copy has its refs
// compared with the forge's advertisement, and a first copy its object count with the
// pack the forge sent. Then a clone is made from the store and git checks it: strictly
// (git fsck --strict) for a repository stored in StrictMax bytes or less, and for a
// Sample fraction of the larger ones by connectivity (every object reachable from every
// ref is present and well formed enough to walk).
type Verify struct {
	// None checks nothing past the refs.
	NoClone bool
	// StrictMax is the stored size up to which every repository is fsck'd in full
	// (default 64 MiB).
	StrictMax int64
	// Sample is the fraction of larger repositories whose clone is checked, chosen by a
	// hash of the name so a re-run checks the same ones (default 0.05).
	Sample float64
}

func (v Verify) run(ctx context.Context, s *objects.Store, id string, adv *Advert, count, packed int64, full bool, size int64) (string, error) {
	repo := s.Repo(ctx, id)
	refs, err := repo.References()
	if err != nil {
		return "", err
	}
	got := map[string]plumbing.Hash{}
	for _, r := range refs {
		if r.Type() == plumbing.HashReference {
			got[r.Name().String()] = r.Hash()
		}
	}
	if len(got) != len(adv.Refs) {
		return "", fmt.Errorf("the store holds %d refs and the forge %d", len(got), len(adv.Refs))
	}
	for n, h := range adv.Refs {
		if got[n] != h {
			return "", fmt.Errorf("ref %s is %s in the store and %s on the forge", n, got[n], h)
		}
	}
	how := "refs"
	if full && packed > 0 {
		if packed != count {
			return "", fmt.Errorf("the store holds %d objects and the forge sent %d", count, packed)
		}
		how += "+count"
	}
	if v.NoClone || len(got) == 0 {
		return how, nil
	}
	strictMax := v.StrictMax
	if strictMax == 0 {
		strictMax = 64 << 20
	}
	sample := v.Sample
	if sample == 0 {
		sample = 0.05
	}
	strict := size <= strictMax
	if !strict {
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		if float64(h.Sum32())/float64(1<<32) >= sample {
			return how, nil
		}
	}
	if err := fsckClone(ctx, repo, got, count, strict); err != nil {
		return "", err
	}
	if strict {
		return how + "+fsck", nil
	}
	return how + "+fsck-connectivity", nil
}

// fsckClone builds a bare repository from what the store serves (the whole object set as
// one pack, the refs) and has git check it. The object count git sees must equal the
// store's.
func fsckClone(ctx context.Context, repo *objects.Repo, refs map[string]plumbing.Hash, count int64, strict bool) error {
	dir, err := os.MkdirTemp("", "forgeimport-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	git := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return out.Bytes(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
	if _, err := git(nil, "init", "--bare", "--quiet", "."); err != nil {
		return err
	}
	oids, err := repo.Hashes(plumbing.AnyObject)
	if err != nil {
		return err
	}
	if int64(len(oids)) != count {
		return fmt.Errorf("the store lists %d objects and counts %d", len(oids), count)
	}
	if len(oids) > 0 {
		idx := exec.CommandContext(ctx, "git", "-C", dir, "index-pack", "--stdin", "--strict")
		pr, pw, err := os.Pipe()
		if err != nil {
			return err
		}
		idx.Stdin = pr
		var errb bytes.Buffer
		idx.Stderr = &errb
		if err := idx.Start(); err != nil {
			return err
		}
		_ = pr.Close()
		werr := repo.Pack(ctx, pw, oids)
		_ = pw.Close()
		if err := idx.Wait(); err != nil {
			return fmt.Errorf("the store's pack does not index: %w: %s", err, strings.TrimSpace(errb.String()))
		}
		if werr != nil {
			return fmt.Errorf("serving the pack: %w", werr)
		}
	}
	var upd bytes.Buffer
	for n, h := range refs {
		fmt.Fprintf(&upd, "create %s %s\n", n, h)
	}
	if _, err := git(upd.Bytes(), "update-ref", "--stdin"); err != nil {
		return err
	}
	args := []string{"fsck", "--no-progress", "--no-dangling"}
	if strict {
		args = append(args, "--strict")
	} else {
		args = append(args, "--connectivity-only")
	}
	if _, err := git(nil, args...); err != nil {
		return fmt.Errorf("fsck of a clone from the store: %w", err)
	}
	b, err := git(nil, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	if err != nil {
		return err
	}
	if n := int64(bytes.Count(b, []byte("\n"))); n != count {
		return fmt.Errorf("a clone from the store holds %d objects and the store %d", n, count)
	}
	return nil
}
