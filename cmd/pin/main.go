// Copyright 2026 Hanzo AI, Inc. All rights reserved.

// Command pin rewrites plugins.lock from the binaries.json a plugin repo's
// release published:
//
//	pin bump <binaries.json> [plugins.lock]
//
// The stanza for each plugin in the index is replaced whole (or added), the file
// is written in canonical form, and nothing is written if any line would not
// parse: no digest, no pin.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud/internal/pin"
)

const lockPath = "internal/pin/plugins.lock"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pin:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 || len(args) > 3 || args[0] != "bump" {
		return fmt.Errorf("usage: pin bump <binaries.json> [plugins.lock]")
	}
	path := lockPath
	if len(args) == 3 {
		path = args[2]
	}
	idx, err := os.Open(args[1])
	if err != nil {
		return err
	}
	defer idx.Close()
	entries, err := pin.FromIndex(idx)
	if err != nil {
		return err
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f, err := pin.Parse(strings.NewReader(string(text)))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if f, err = f.With(e); err != nil {
			return err
		}
	}
	for _, e := range entries {
		fmt.Printf("pinned %s %s %s\n", e.Name, e.Repo, e.Tag)
	}
	return os.WriteFile(path, []byte(f.Format()), 0o644)
}
