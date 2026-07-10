// Package infra provides infrastructure clients.
//
// This file implements the ZAP transport for inter-service vector operations.
// Other Hanzo services call commerce via ZAP (luxfi/zap) for vector ops.
// The Qdrant REST client remains the DB access layer -- ZAP wraps it for
// service-to-service communication.
package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/luxfi/zap"
)

// ZAP opcodes for vector operations.
const (
	OpVectorUpsert uint16 = 0x10
	OpVectorSearch uint16 = 0x11
	OpVectorDelete uint16 = 0x12
)

// ZAP message field offsets.
// Layout: [status(1)][reserved(7)][payload...]
const (
	zapFieldStatus  = 0 // uint8: 0=ok, 1=error
	zapFieldPayload = 8 // bytes: JSON payload
)

// ZAPConfig configures the ZAP transport node.
type ZAPConfig struct {
	Enabled bool
	NodeID  string
	Port    int
}

// ZAPNode wraps a zap.Node and delegates to VectorClient.
type ZAPNode struct {
	node   *zap.Node
	vector *VectorClient
}

// NewZAPNode creates and starts a ZAP node with vector operation handlers.
// The node uses NoDiscovery (K8s services connect directly).
func NewZAPNode(cfg *ZAPConfig, vector *VectorClient, logger *slog.Logger) (*ZAPNode, error) {
	if logger == nil {
		logger = slog.Default()
	}

	node := zap.NewNode(zap.NodeConfig{
		NodeID:      cfg.NodeID,
		ServiceType: "_commerce._tcp",
		Port:        cfg.Port,
		NoDiscovery: true,
		Logger:      logger,
	})

	z := &ZAPNode{node: node, vector: vector}

	node.Handle(OpVectorUpsert, z.handleUpsert)
	node.Handle(OpVectorSearch, z.handleSearch)
	node.Handle(OpVectorDelete, z.handleDelete)

	if err := node.Start(); err != nil {
		return nil, fmt.Errorf("zap node start: %w", err)
	}

	return z, nil
}

// Stop stops the ZAP node.
func (z *ZAPNode) Stop() {
	z.node.Stop()
}

// Node returns the underlying zap.Node (for health checks, peer info, etc.)
func (z *ZAPNode) Node() *zap.Node {
	return z.node
}

// --- request/response types (JSON-encoded in ZAP payload) ---

type zapUpsertRequest struct {
	Collection string         `json:"collection"`
	Points     []*VectorPoint `json:"points"`
}

type zapSearchRequest struct {
	Collection string                 `json:"collection"`
	Vector     []float32              `json:"vector"`
	Limit      int                    `json:"limit"`
	MinScore   float32                `json:"minScore"`
	Filter     map[string]interface{} `json:"filter,omitempty"`
}

type zapDeleteRequest struct {
	Collection string   `json:"collection"`
	IDs        []string `json:"ids"`
}

type zapSearchResponse struct {
	Results []VectorSearchResult `json:"results"`
}

// --- handlers ---

func (z *ZAPNode) handleUpsert(_ context.Context, _ string, msg *zap.Message) (*zap.Message, error) {
	payload := msg.Root().Bytes(zapFieldPayload)
	if payload == nil {
		return zapError("empty payload"), nil
	}

	var req zapUpsertRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return zapError("bad request: " + err.Error()), nil
	}

	if err := z.vector.Upsert(context.Background(), req.Collection, req.Points); err != nil {
		return zapError(err.Error()), nil
	}

	return zapOK(nil), nil
}

func (z *ZAPNode) handleSearch(_ context.Context, _ string, msg *zap.Message) (*zap.Message, error) {
	payload := msg.Root().Bytes(zapFieldPayload)
	if payload == nil {
		return zapError("empty payload"), nil
	}

	var req zapSearchRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return zapError("bad request: " + err.Error()), nil
	}

	results, err := z.vector.Search(context.Background(), &VectorSearchOpts{
		Collection: req.Collection,
		Vector:     req.Vector,
		Limit:      req.Limit,
		MinScore:   req.MinScore,
		Filter:     req.Filter,
	})
	if err != nil {
		return zapError(err.Error()), nil
	}

	respBytes, _ := json.Marshal(&zapSearchResponse{Results: results})
	return zapOK(respBytes), nil
}

func (z *ZAPNode) handleDelete(_ context.Context, _ string, msg *zap.Message) (*zap.Message, error) {
	payload := msg.Root().Bytes(zapFieldPayload)
	if payload == nil {
		return zapError("empty payload"), nil
	}

	var req zapDeleteRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return zapError("bad request: " + err.Error()), nil
	}

	if err := z.vector.Delete(context.Background(), req.Collection, req.IDs); err != nil {
		return zapError(err.Error()), nil
	}

	return zapOK(nil), nil
}

// --- ZAP message builders ---

// zapOK builds a success response. payload may be nil.
func zapOK(payload []byte) *zap.Message {
	return buildZAPResponse(0, payload)
}

// zapError builds an error response with the error string as payload.
func zapError(errMsg string) *zap.Message {
	return buildZAPResponse(1, []byte(errMsg))
}

func buildZAPResponse(status uint8, payload []byte) *zap.Message {
	b := zap.NewBuilder(zap.HeaderSize + 16 + len(payload))
	obj := b.StartObject(16)
	obj.SetUint8(zapFieldStatus, status)
	if len(payload) > 0 {
		obj.SetBytes(zapFieldPayload, payload)
	}
	obj.FinishAsRoot()
	msg, _ := zap.Parse(b.Finish())
	return msg
}

// BuildZAPRequest builds a ZAP request message with the given payload.
// The caller sets flags to encode the opcode: flags = opcode << 8.
func BuildZAPRequest(opcode uint16, payload []byte) *zap.Message {
	b := zap.NewBuilder(zap.HeaderSize + 16 + len(payload))
	obj := b.StartObject(16)
	obj.SetUint8(zapFieldStatus, 0)
	if len(payload) > 0 {
		obj.SetBytes(zapFieldPayload, payload)
	}
	obj.FinishAsRoot()
	flags := opcode << 8
	msg, _ := zap.Parse(b.FinishWithFlags(flags))
	return msg
}
