// Copyright © 2026 Hanzo AI. MIT License.

// kmsfetch reads secrets over the node's KMS socket for a container that holds
// no credential of its own. The socket is $ZIP_RUNTIME_DIR/kms.sock and the
// kernel says who is asking, so there is nothing to present.
//
//	kmsfetch [-out DIR] REF...              write each secret to DIR/<name>, in memory
//	kmsfetch -env NAME=REF... -- CMD ARG...  set each as an environment variable and exec CMD
//	kmsfetch install DIR                     copy this binary into DIR, for an image without it
//
// The exec form is how a container that reads its configuration from the
// environment gets a secret with no file, no shell and no Secret object: the
// value exists in the process that replaces this one and nowhere else.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/kms"
)

type pairs []string

func (p *pairs) String() string     { return strings.Join(*p, ",") }
func (p *pairs) Set(s string) error { *p = append(*p, s); return nil }

func main() {
	if len(os.Args) > 2 && os.Args[1] == "install" {
		if err := install(os.Args[2]); err != nil {
			fail(err)
		}
		return
	}
	var env pairs
	out := flag.String("out", "/run/hanzo/secrets", "directory the secrets are written to")
	wait := flag.Duration("wait", 2*time.Minute, "how long to keep asking for a socket that is not answering yet")
	flag.Var(&env, "env", "NAME=REF: set NAME in the environment of the command that follows --")
	flag.Parse()
	refs := flag.Args()

	ctx, cancel := context.WithTimeout(context.Background(), *wait)
	defer cancel()

	if len(env) > 0 {
		if len(refs) == 0 {
			fail(fmt.Errorf("-env needs a command after --"))
		}
		for _, e := range env {
			name, ref, ok := strings.Cut(e, "=")
			if !ok || name == "" || ref == "" {
				fail(fmt.Errorf("-env %q: want NAME=REF", e))
			}
			v, err := read(ctx, ref)
			if err != nil {
				fail(err)
			}
			os.Setenv(name, string(v))
		}
		path, err := lookPath(refs[0])
		if err != nil {
			fail(err)
		}
		fail(syscall.Exec(path, refs, os.Environ()))
	}

	if len(refs) == 0 {
		fail(fmt.Errorf("no refs"))
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		fail(err)
	}
	for _, ref := range refs {
		v, err := read(ctx, ref)
		if err != nil {
			fail(err)
		}
		if err := write(filepath.Join(*out, filepath.Base(ref)), v); err != nil {
			fail(err)
		}
	}
}

// read keeps asking until the socket answers or the deadline passes: the
// process that serves it may still be coming up when this one runs.
func read(ctx context.Context, ref string) ([]byte, error) {
	var last error
	for {
		s, err := kms.KMSGet(ctx, &client.SecretIn{Ref: ref})
		if err == nil {
			return s.Value, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: %w", ref, last)
		case <-time.After(2 * time.Second):
		}
	}
}

// write lands the file whole or not at all, readable by its owner only.
func write(path string, value []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, value, 0o400); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// install copies this executable into dir, so a container built from an image
// that does not carry it can run it from a volume shared with an init that does.
func install(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "kmsfetch")
	f, err := os.OpenFile(dst+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(dst+".tmp", dst)
}

// lookPath resolves a bare command name on PATH the way a shell would, so the
// exec form takes the same words a Dockerfile CMD does.
func lookPath(cmd string) (string, error) {
	if strings.Contains(cmd, "/") {
		return cmd, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(dir, cmd)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found on PATH", cmd)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "kmsfetch:", err)
	os.Exit(1)
}
