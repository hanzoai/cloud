package benchmark

// legacy.go — what the file stores recorded, carried into the store once.
//
// Before the store was one database the arena kept append-only JSONL under the data
// directory: {dir}/benchmark/attempts/<model>.jsonl, {dir}/benchmark/claims/<benchmark>.jsonl
// and {dir}/benchmark/presets.jsonl. A deployment that recorded anything there has those
// files on its volume, and a store that opened empty beside them would serve an arena
// that had forgotten every claim, import and blend.
//
// So the first open reads them. Each plane is carried in ONE transaction under the
// store's write lock with a marker row beside it, so a plane lands whole or not at all,
// a second replica that finds the marker has nothing to do, and a replica with no volume
// finds no files and writes nothing. It is FAIL-SECURE: an error fails the mount rather
// than serving an empty arena over real rows. The files are READ and left exactly where
// they are, which is what makes the upgrade reversible.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// Legacy is what one plane's files held, and how much of it was carried.
type Legacy struct {
	Plane   string // attempts, claims or presets
	Carried int    // rows now in the store
	Skipped int    // rows the store cannot hold (text PostgreSQL refuses)
}

// importLegacy carries every plane whose files exist under dir and whose marker is not
// yet in the store. It answers one entry per plane it carried.
func (s *Store) importLegacy(ctx context.Context, dir string) ([]Legacy, error) {
	var out []Legacy
	for _, plane := range []struct {
		name  string
		carry func(ctx context.Context, b book, dir string) (carried, skipped int, found bool, err error)
	}{
		{"attempts", carryAttempts},
		{"claims", carryClaims},
		{"presets", carryPresets},
	} {
		var done Legacy
		err := s.write(ctx, func(b book) error {
			var marked int
			if err := b.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM benchmark_legacy WHERE source=$1`, plane.name).Scan(&marked); err != nil {
				return err
			}
			if marked > 0 {
				return nil
			}
			carried, skipped, found, err := plane.carry(ctx, b, dir)
			if err != nil || !found {
				return err
			}
			if _, err := b.q.ExecContext(ctx, `INSERT INTO benchmark_legacy (source, imported, n) VALUES ($1, $2, $3)`,
				plane.name, time.Now().Unix(), carried); err != nil {
				return err
			}
			done = Legacy{Plane: plane.name, Carried: carried, Skipped: skipped}
			return nil
		})
		if err != nil {
			return out, fmt.Errorf("benchmark: carry the %s the file stores recorded: %w", plane.name, err)
		}
		if done.Plane != "" {
			out = append(out, done)
		}
	}
	return out, nil
}

// lines calls fn for each non-empty line of the files that match pattern.
func lines(pattern string, fn func(line []byte)) (found bool, err error) {
	files, err := filepath.Glob(pattern)
	if err != nil {
		return false, err
	}
	for _, name := range files {
		fh, err := os.Open(name)
		if err != nil {
			return found, err
		}
		found = true
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
		for sc.Scan() {
			if len(sc.Bytes()) > 0 {
				fn(sc.Bytes())
			}
		}
		err = sc.Err()
		_ = fh.Close()
		if err != nil {
			return found, fmt.Errorf("%s: %w", name, err)
		}
	}
	return found, nil
}

func carryAttempts(ctx context.Context, b book, dir string) (carried, skipped int, found bool, err error) {
	found, err = lines(filepath.Join(dir, "benchmark", "attempts", "*.jsonl"), func(line []byte) {
		var a attempt
		if json.Unmarshal(line, &a) != nil || a.ID == "" || a.Model == "" {
			return
		}
		if aerr := b.Append(ctx, a); aerr != nil {
			if err == nil && !isText(aerr) {
				err = aerr
			}
			skipped++
			return
		}
		carried++
	})
	return carried, skipped, found, err
}

func carryClaims(ctx context.Context, b book, dir string) (carried, skipped int, found bool, err error) {
	found, err = lines(filepath.Join(dir, "benchmark", "claims", "*.jsonl"), func(line []byte) {
		var c storedClaim
		if json.Unmarshal(line, &c) != nil {
			return
		}
		if cerr := b.insertClaim(ctx, c); cerr != nil {
			if err == nil && !isText(cerr) {
				err = cerr
			}
			skipped++
			return
		}
		carried++
	})
	return carried, skipped, found, err
}

func carryPresets(ctx context.Context, b book, dir string) (carried, skipped int, found bool, err error) {
	found, err = lines(filepath.Join(dir, "benchmark", "presets.jsonl"), func(line []byte) {
		var p Preset
		if json.Unmarshal(line, &p) != nil || p.Owner == "" || p.Name == "" {
			return
		}
		if perr := b.PutPreset(ctx, p); perr != nil {
			if err == nil && !isText(perr) {
				err = perr
			}
			skipped++
			return
		}
		carried++
	})
	return carried, skipped, found, err
}

// isText reports whether err is a row whose text the store refuses on every engine.
func isText(err error) bool { return errors.Is(err, dialect.ErrText) }
