//go:build windows

package cli

import (
	"os"
	"os/exec"
)

// execEngine runs hanzoai as a child with inherited stdio (Windows has no exec()).
// The child shares the console, so Ctrl-C reaches it; we return its exit error.
func execEngine(bin string, args []string) error {
	c := exec.Command(bin, args[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
