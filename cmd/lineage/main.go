// Command lineage reports whether a commit is one the branch that hands out
// cloud's version numbers has been.
//
// The release workflow runs it immediately before the compare-and-swap that
// claims a number, which is the only place a number is allocated.
//
// Usage:
//
//	lineage <commit>
//
// Exit 0 means the arbiter reaches that commit from its release branch. Any other
// exit means it does not, or that the arbiter could not be asked; the reason goes to
// standard error in both cases, and no version number may be claimed after either.
//
// FORGE_TOKEN authenticates the read.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hanzoai/cloud/internal/lineage"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: lineage <commit>")
		os.Exit(2)
	}
	sha := os.Args[1]

	if err := lineage.Cloud.Verify(context.Background(), os.Getenv("FORGE_TOKEN"), sha); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s is on %s at %s\n", sha, lineage.Cloud.Branch, lineage.Cloud.Remote)
}
