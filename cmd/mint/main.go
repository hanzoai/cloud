// Command mint is `cloud secret token` and nothing else: the binary a build Job's
// forge-identity init container runs to exchange the pod's projected ServiceAccount
// assertion for a reader token (apps/platform forge_identity.go). It answers the same
// argv as /cloud and, being pure Go, builds for either architecture.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hanzoai/cloud/secret"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "secret" {
		fmt.Fprintln(os.Stderr, "cloud: this binary answers `secret token` only")
		os.Exit(2)
	}
	if err := secret.Run(context.Background(), os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "cloud:", err)
		os.Exit(1)
	}
}
