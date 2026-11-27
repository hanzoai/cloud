package main

import (
	"os"
	"os/exec"
	"testing"
)

// The token image is built natively for each architecture with no C toolchain.
func TestTheBinaryBuildsWithoutCgoForBothArchitectures(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			cmd := exec.Command(goBin, "build", "-o", os.DevNull, ".")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("GOARCH=%s CGO_ENABLED=0: %v\n%s", arch, err, out)
			}
		})
	}
}
