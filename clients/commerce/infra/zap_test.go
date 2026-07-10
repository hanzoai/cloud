package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/luxfi/zap"
)

func TestBuildZAPRequest(t *testing.T) {
	payload := []byte(`{"collection":"test","ids":["a","b"]}`)
	msg := BuildZAPRequest(OpVectorDelete, payload)
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	// Opcode should be encoded in flags upper 8 bits
	wantFlags := OpVectorDelete << 8
	if msg.Flags() != wantFlags {
		t.Errorf("flags = %#x, want %#x", msg.Flags(), wantFlags)
	}

	// Payload should round-trip
	got := msg.Root().Bytes(zapFieldPayload)
	if string(got) != string(payload) {
		t.Errorf("payload = %q, want %q", got, payload)
	}
}

func TestZAPOKError(t *testing.T) {
	// OK with nil payload
	ok := zapOK(nil)
	if ok.Root().Uint8(zapFieldStatus) != 0 {
		t.Error("ok status should be 0")
	}

	// Error with message
	errMsg := zapError("something broke")
	if errMsg.Root().Uint8(zapFieldStatus) != 1 {
		t.Error("error status should be 1")
	}
	got := errMsg.Root().Bytes(zapFieldPayload)
	if string(got) != "something broke" {
		t.Errorf("error payload = %q, want %q", got, "something broke")
	}
}

func TestZAPNodeHandlers(t *testing.T) {
	// We can't easily stand up Qdrant in a unit test, but we can verify
	// the ZAP node starts and accepts connections with handlers registered.

	cfg := &ZAPConfig{
		Enabled: true,
		NodeID:  "test-node",
		Port:    freePort(t),
	}

	// Create a minimal VectorClient (it won't be called in this test).
	vc := &VectorClient{
		config:  &VectorConfig{Port: 6333, DefaultCollection: "test"},
		baseURL: "http://localhost:6333",
	}

	node, err := NewZAPNode(cfg, vc, nil)
	if err != nil {
		t.Fatalf("NewZAPNode: %v", err)
	}
	defer node.Stop()

	if node.Node().NodeID() != "test-node" {
		t.Fatalf("NodeID = %q, want %q", node.Node().NodeID(), "test-node")
	}
	t.Logf("ZAP test node started: %s", node.Node().NodeID())
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestZAPNodeCallRoundTrip(t *testing.T) {
	serverPort := freePort(t)

	// Start a ZAP server node with a mock handler for search
	serverNode := zap.NewNode(zap.NodeConfig{
		NodeID:      "server",
		ServiceType: "_test._tcp",
		Port:        serverPort,
		NoDiscovery: true,
	})

	// Register a handler on OpVectorSearch
	serverNode.Handle(OpVectorSearch, func(_ context.Context, _ string, msg *zap.Message) (*zap.Message, error) {
		payload := msg.Root().Bytes(zapFieldPayload)
		_ = payload
		results := &zapSearchResponse{
			Results: []VectorSearchResult{
				{ID: "hit-1", Score: 0.95, Payload: map[string]interface{}{"name": "test"}},
			},
		}
		respBytes, _ := json.Marshal(results)
		return zapOK(respBytes), nil
	})

	if err := serverNode.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer serverNode.Stop()

	clientPort := freePort(t)

	// Start a client node
	clientNode := zap.NewNode(zap.NodeConfig{
		NodeID:      "client",
		ServiceType: "_test._tcp",
		Port:        clientPort,
		NoDiscovery: true,
	})
	if err := clientNode.Start(); err != nil {
		t.Fatalf("client start: %v", err)
	}
	defer clientNode.Stop()

	// Connect client to server directly
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	if err := clientNode.ConnectDirect(serverAddr); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Give handshake a moment
	time.Sleep(100 * time.Millisecond)

	// Build a search request
	reqPayload, _ := json.Marshal(&zapSearchRequest{
		Collection: "products",
		Vector:     []float32{0.1, 0.2, 0.3},
		Limit:      5,
	})
	reqMsg := BuildZAPRequest(OpVectorSearch, reqPayload)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := clientNode.Call(ctx, "server", reqMsg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	// Verify response
	status := resp.Root().Uint8(zapFieldStatus)
	if status != 0 {
		t.Fatalf("response status = %d, want 0", status)
	}

	respPayload := resp.Root().Bytes(zapFieldPayload)
	var searchResp zapSearchResponse
	if err := json.Unmarshal(respPayload, &searchResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(searchResp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(searchResp.Results))
	}
	if searchResp.Results[0].ID != "hit-1" {
		t.Errorf("result ID = %q, want %q", searchResp.Results[0].ID, "hit-1")
	}
	if searchResp.Results[0].Score != 0.95 {
		t.Errorf("result score = %f, want 0.95", searchResp.Results[0].Score)
	}
}
