package visor

import (
	"os"
	"os/exec"
	"testing"
)

// TestEscapeAttempt verifies that the eBPF policy (if active) blocks unauthorized file access.
// Note: This test requires Tetragon to be running on the host.
func TestEscapeAttempt(t *testing.T) {
	if os.Getenv("TETRAGON_ENABLED") != "true" {
		t.Skip("Skipping eBPF enforcement test: TETRAGON_ENABLED not set")
	}

	// Attempt to read a sensitive file defined in policy.yaml
	cmd := exec.Command("cat", "/etc/shadow")
	err := cmd.Run()

	// If the policy is working, the process should be killed (exit status -1 or signal killed)
	if err == nil {
		t.Errorf("Security Breach: Successfully read /etc/shadow. eBPF policy not enforcing.")
	} else {
		t.Logf("Success: Process blocked/killed as expected: %v", err)
	}
}

// BenchmarkSyscallOverhead measures the impact of eBPF probes on standard file operations.
func BenchmarkSyscallOverhead(b *testing.B) {
	f, err := os.CreateTemp("", "visor-bench")
	if err != nil {
		b.Fatal(err)
	}
	defer os.Remove(f.Name())

	data := []byte("hanzo-cloud-test-data")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := f.Write(data)
		if err != nil {
			b.Fatal(err)
		}
		f.Sync()
	}
}
