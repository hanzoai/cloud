package amqp

import (
	"net"
	"strconv"
	"testing"

	psembed "github.com/hanzoai/pubsub/embed"
	amqp091 "github.com/rabbitmq/amqp091-go"
)

// TestUseServesAMQPOnTheBus: with the bus up, Use connects the gateway and
// serves its Wire port; run standalone (no host socket) that is the TCP port
// CLOUD_AMQP_PORT names, and the reference client declares a queue on it.
func TestUseServesAMQPOnTheBus(t *testing.T) {
	gateway = nil
	ps, err := psembed.Open(psembed.Options{Host: "127.0.0.1", Port: -1, ServerName: "use", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pubsub open: %v", err)
	}
	defer ps.Shutdown()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv("CLOUD_PUBSUB_URL", ps.ClientURL())
	t.Setenv("CLOUD_AMQP_PORT", strconv.Itoa(port))

	if err := Use(testApp(), testDeps()); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })

	conn, err := amqp091.Dial("amqp://127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	if _, err := ch.QueueDeclare("use", true, false, false, false, nil); err != nil {
		t.Fatalf("queue.declare: %v", err)
	}
}
