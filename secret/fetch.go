package secret

// fetch.go is the same read, for a workload that is not Go.
//
//	cloud secret fetch --path <p> --key <k>[=<file>] [--key …] --out <dir>
//
// It runs as an init container beside the app, with the ServiceAccount token
// projected and an emptyDir of medium Memory mounted at --out. Each value lands
// in its own file, mode 0400, named for its key — or for the name after `=`,
// when the app reads a file it did not name the same thing; the app reads it
// needs and the tmpfs dies with the pod. Nothing is written to a disk and
// nothing is exported into the app's environment, which is the point — this is
// the Go Boot above with a filesystem as the delivery, not a second mechanism.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/brand"
)

// Run executes the `secret` verb. args is everything after the verb itself.
func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("cloud secret: fetch --path <p> --key <k>[=<file>] [--key …] --out <dir>")
	}
	switch args[0] {
	case "token":
		return writeToken(ctx, args[1:])
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
	set.Var(&want, "key", "secret name to fetch, or name=file when the app reads another name; repeat for more than one")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("cloud secret fetch: --out is required")
	}

	// `name=file` says the app reads this value under another name. The
	// alternative is a second mount for one file, which is how a pod ends up
	// with two ideas of where its configuration lives.
	names := make([]string, 0, len(want))
	file := make(map[string]string, len(want))
	for _, entry := range want {
		key, as, renamed := strings.Cut(entry, "=")
		if !renamed {
			as = key
		}
		if !name(key) {
			return fmt.Errorf("cloud secret fetch: %q is not a secret name", key)
		}
		if !name(as) {
			return fmt.Errorf("cloud secret fetch: %q is not a file name", as)
		}
		names = append(names, key)
		file[key] = as
	}

	values, err := Boot(ctx, *path, names...)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return fmt.Errorf("cloud secret fetch: %w", err)
	}
	for _, key := range names {
		dest := filepath.Join(*dir, file[key])
		// A 0400 file cannot be reopened for writing by the user who owns it,
		// so a re-run replaces the file rather than truncating it — which is
		// also what keeps a stale value from surviving under a fresh name.
		if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cloud secret fetch: %w", err)
		}
		if err := os.WriteFile(dest, []byte(values[key]), 0o400); err != nil {
			return fmt.Errorf("cloud secret fetch: %w", err)
		}
	}
	// The names, never the values: this line goes to a log.
	fmt.Fprintf(os.Stderr, "cloud secret fetch: wrote %d secret(s) to %s\n", len(names), *dir)
	return nil
}

// writeToken lends a short-lived workload bearer to a non-Go reader via tmpfs.
// The projected assertion stays mounted only in the init container.
func writeToken(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cloud secret token", flag.ContinueOnError)
	out := flags.String("out", "", "token file on a memory-backed emptyDir")
	reader := flags.String("forge-reader", "", "Forge origin whose reader role must admit this token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("cloud secret token: --out is required")
	}
	token, err := WorkloadToken(ctx)
	if err != nil {
		return err
	}
	if token == nil || !strings.HasPrefix(token.AccessToken, "ey") || strings.Count(token.AccessToken, ".") != 2 || time.Until(token.Expiry) < 30*time.Minute {
		return errors.New("cloud secret token: IAM did not issue a JWT valid for the build window")
	}
	if *reader != "" {
		if err := verifyForgeReader(ctx, token.AccessToken, *reader); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(*out), ".iam-token-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.WriteString(token.AccessToken); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(0400); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, *out); err != nil {
		return err
	}

	return nil
}

func verifyForgeReader(ctx context.Context, bearer, origin string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(origin, "/")+"/v1/identity-authority", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := exchange.Do(req)
	if err != nil {
		return errors.New("secret: Forge reader admission is unreachable")
	}
	defer resp.Body.Close()
	var role struct {
		Authority      string `json:"authority"`
		Issuer         string `json:"issuer"`
		CredentialRole string `json:"credentialRole"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&role) != nil || role.Authority != "hanzo-iam" || !brand.Trusts(brand.IssuerFor(brand.Default), role.Issuer) || role.CredentialRole != "reader" {
		return errors.New("secret: Forge did not admit a native IAM reader")
	}
	return nil
}
