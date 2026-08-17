// Command lineage reports whether a commit is in the release lineage of the
// repository that hands out cloud's version numbers.
//
// It is what the release workflow runs immediately before it claims a number, so
// the workflow and the in-cloud release path put the same question to the same
// arbiter through the same code (internal/lineage) rather than through two
// implementations that can drift apart.
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
