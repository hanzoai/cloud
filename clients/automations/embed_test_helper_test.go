package automations

import (
	"context"
	"net"
	"testing"

	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// embedEngine starts an embedded tasks engine on a free port, RETRYING when the
// port is taken between picking it and binding it.
//
// Picking a port with Listen(":0"), closing the listener and handing the bare
// number to Embed is a time-of-check/time-of-use race: the port is free when we
// look and can be gone when Embed binds it. It is rare serially and reproducible
// under `go test ./...`, where several packages play the same trick at once — it
// is what made TestTriggerPayloadThreadsThroughDurableRun fail in a parallel run
// and pass in isolation.
//
// The race cannot be closed by holding the listener, because Embed binds the port
// itself. Retrying is what actually converges: a second draw lands on a different
// ephemeral port, so a collision costs a retry instead of a red build.
func embedEngine(ctx context.Context, t *testing.T, namespace string) (*tasksengine.Embedded, int) {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()

		srv, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{
			ZAPPort: port, Namespace: namespace, DataDir: t.TempDir(),
		})
		if err == nil {
			return srv, port
		}
		lastErr = err
	}
	t.Fatalf("embed: no free port after 5 attempts: %v", lastErr)
	return nil, 0
}
