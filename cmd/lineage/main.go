package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hanzoai/cloud/internal/lineage"
)

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: lineage <commit> [branch]")
		os.Exit(2)
	}
	sha := os.Args[1]

	a := lineage.Cloud
	if len(os.Args) == 3 {
		var ok bool
		if a, ok = lineage.Of(os.Args[2]); !ok {
			fmt.Fprintf(os.Stderr, "lineage: no lane builds branch %q\n", os.Args[2])
			os.Exit(2)
		}
	}
	if err := a.Verify(context.Background(), os.Getenv("GIT_TOKEN"), sha); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%s is on %s at %s\n", sha, a.Branch, a.Remote)
	fmt.Printf("remote=%s\napi=%s\nbranch=%s\nimage=%s\n", a.Remote, a.API(), a.Branch, a.Image)
}
