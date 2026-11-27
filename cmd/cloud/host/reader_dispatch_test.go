package host

import (
	"strings"
	"testing"
)

// This invokes the binary's actual run() entrypoint, not the cloud.Listen
// variant. It would have failed before the role dispatch was wired into
// cmd/cloud: the process would proceed to open the writer lease and plugins.
func TestShippedRouterReaderWithoutTargetFailsBeforeWriterStartup(t *testing.T) {
	t.Setenv("CLOUD_ROLE", "reader")
	t.Setenv("CLOUD_WRITER_URL", "")
	err := run("127.0.0.1:0", "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "CLOUD_WRITER_URL") {
		t.Fatalf("reader run must fail closed without writer target, got %v", err)
	}
}

func TestShippedRouterRejectsInvalidHARole(t *testing.T) {
	t.Setenv("CLOUD_ROLE", "redar")
	err := run("127.0.0.1:0", "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "CLOUD_ROLE") {
		t.Fatalf("invalid role must refuse to boot, got %v", err)
	}
}
