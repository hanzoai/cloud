package secret

// fetch.go is the same read, for a workload that is not Go.
//
//	cloud secret fetch --path <p> --key <k> [--key <k> …] --out <dir>
//
// It runs as an init container beside the app, with the ServiceAccount token
// projected and an emptyDir of medium Memory mounted at --out. Each value lands
// in its own file, mode 0400, named for its key; the app reads the file it
// needs and the tmpfs dies with the pod. Nothing is written to a disk and
// nothing is exported into the app's environment, which is the point — this is
// the Go Boot above with a filesystem as the delivery, not a second mechanism.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Run executes the `secret` verb. args is everything after the verb itself.
func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("cloud secret: fetch --path <p> --key <k> [--key <k> …] --out <dir>")
	}
	switch args[0] {
	case "fetch":
		return fetch(ctx, args[1:])
	default:
		return fmt.Errorf("cloud secret: no verb %q — the verb is fetch", args[0])
	}
}

// keys collects a repeated --key flag.
type keys []string

func (k *keys) String() string     { return fmt.Sprint(*k) }
func (k *keys) Set(v string) error { *k = append(*k, v); return nil }

func fetch(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("cloud secret fetch", flag.ContinueOnError)
	path := set.String("path", "", "coordinate beneath your org's root, such as cloud/prod")
	dir := set.String("out", "", "directory to write one file per key into (mount a memory-backed emptyDir)")
	var want keys
	set.Var(&want, "key", "secret name to fetch; repeat for more than one")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("cloud secret fetch: --out is required")
	}

	values, err := Boot(ctx, *path, want...)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return fmt.Errorf("cloud secret fetch: %w", err)
	}
	for _, key := range want {
		file := filepath.Join(*dir, key)
		// A 0400 file cannot be reopened for writing by the user who owns it,
		// so a re-run replaces the file rather than truncating it — which is
		// also what keeps a stale value from surviving under a fresh name.
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cloud secret fetch: %w", err)
		}
		if err := os.WriteFile(file, []byte(values[key]), 0o400); err != nil {
			return fmt.Errorf("cloud secret fetch: %w", err)
		}
	}
	// The names, never the values: this line goes to a log.
	fmt.Fprintf(os.Stderr, "cloud secret fetch: wrote %d secret(s) to %s\n", len(want), *dir)
	return nil
}
