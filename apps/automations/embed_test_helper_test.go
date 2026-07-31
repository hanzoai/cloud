package automations

import (
	"context"
	"path/filepath"
	"testing"

	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// embedEngine starts an embedded tasks engine on its own unix socket.
//
// It used to pick a free TCP port with Listen(":0"), close the listener and hand
// the bare number to Embed — a time-of-check/time-of-use race, because the port
// was free when we looked and could be gone when Embed bound it. Rare serially
// and reproducible under `go test ./...`, where several packages played the same
// trick at once; it is what made TestTriggerPayloadThreadsThroughDurableRun fail
// in a parallel run and pass in isolation. The retry loop that hid it is gone
// with the port: a path in this test's own directory cannot be taken by anyone.
func embedEngine(ctx context.Context, t *testing.T, namespace string) (*tasksengine.Embedded, string) {
	t.Helper()
	dir := t.TempDir()
	addr := filepath.Join(dir, "tasks.sock")
	srv, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{
		Address: addr, Namespace: namespace, DataDir: dir,
	})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	return srv, addr
}
