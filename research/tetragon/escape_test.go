package tetragon_test

import (
	"os/exec"
	"testing"
)

// TestEscapeAttempt simulates a malicious agent trying to break out of the sandbox.
// When Tetragon is active with the provided policy, these tests should trigger a SIGKILL.
func TestEscapeAttempt(t *testing.T) {
	t.Run("UnauthorizedExec", func(t *testing.T) {
		// Attempting to run 'cat' which is not in the allow-list
		cmd := exec.Command("cat", "/etc/shadow")
		err := cmd.Run()
		if err == nil {
			t.Errorf("Security Failure: Managed to execute unauthorized binary")
		} else {
			t.Logf("Success: Process blocked/killed as expected: %v", err)
		}
	})

	t.Run("UnauthorizedNetwork", func(t *testing.T) {
		// Attempting to reach an external IP
		cmd := exec.Command("curl", "--connect-timeout", "2", "http://1.1.1.1")
		err := cmd.Run()
		if err == nil {
			t.Errorf("Security Failure: Managed to access external network")
		} else {
			t.Logf("Success: Network access blocked: %v", err)
		}
	})
}
