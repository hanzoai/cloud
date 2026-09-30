package clients

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/types"
)

// rpcEndpoint identifies a remote subsystem reachable over ZAP RPC.
// The transport layer is hanzoai/zap (binary, length-prefixed,
// per-stream). Per HIP-0106 this is the only inter-subsystem wire
// format when subsystems are split-deployed. JSON never appears
// between subsystems.
type rpcEndpoint struct {
	subsystem string
	addr      string // host:port of the subsystem's ZAP listener
}

func (e *rpcEndpoint) errf(verb string) error {
	// TODO(zapc-gen): replace with the real zapc-generated client
	// once `zapc generate <subsystem>/schema/*.zap --lang go --out
	// ./zap/gen/` has produced typed stubs for every subsystem.
	// Until then, the contract is enforced (the call goes through a
	// typed Go interface), and the wire transport returns a clear
	// "not wired" error so operators see what's missing.
	return fmt.Errorf("cloud: ZAP RPC client for %s@%s not yet wired (zapc-gen pending) — %s", e.subsystem, e.addr, verb)
}

// --- per-subsystem RPC stubs --------------------------------------------

type rpcIAM struct{ rpcEndpoint }

func (c *rpcIAM) VerifyJWT(_ context.Context, _ string) (types.Claims, error) {
	return types.Claims{}, c.errf("VerifyJWT")
}
func (c *rpcIAM) GetUser(_ context.Context, _ string) (*types.User, error) {
	return nil, c.errf("GetUser")
}
func (c *rpcIAM) GetOrg(_ context.Context, _ string) (*types.Org, error) {
	return nil, c.errf("GetOrg")
}

type rpcKMS struct{ rpcEndpoint }

func (c *rpcKMS) GetSecret(_ context.Context, _ string) ([]byte, error) {
	return nil, c.errf("GetSecret")
}
func (c *rpcKMS) PutSecret(_ context.Context, _ string, _ []byte) error {
	return c.errf("PutSecret")
}
func (c *rpcKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, c.errf("Sign")
}

type rpcBase struct{ rpcEndpoint }

func (c *rpcBase) Open(_ context.Context, _, _ string) (types.DBHandle, error) {
	return nil, c.errf("Open")
}

type rpcAI struct{ rpcEndpoint }

func (c *rpcAI) ChatCompletion(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	return nil, c.errf("ChatCompletion")
}

func (c *rpcAI) Embed(_ context.Context, _ *types.EmbedRequest) ([][]float32, error) {
	return nil, c.errf("Embed")
}

type rpcO11y struct{ rpcEndpoint }

func (c *rpcO11y) Counter(_ string, _ ...string) types.Counter { return noopCounter{} }
func (c *rpcO11y) Timing(_ string, _ ...string) types.Timing   { return noopTiming{} }
func (c *rpcO11y) Span(ctx context.Context, _ string) (context.Context, types.Span) {
	return ctx, noopSpan{}
}

type rpcVFS struct{ rpcEndpoint }

func (c *rpcVFS) Put(_ context.Context, _ string, _ []byte) error {
	return c.errf("Put")
}
func (c *rpcVFS) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, c.errf("Get")
}
func (c *rpcVFS) Delete(_ context.Context, _ string) error {
	return c.errf("Delete")
}

type rpcMQ struct{ rpcEndpoint }

func (c *rpcMQ) Publish(_ context.Context, _ string, _ []byte) error {
	return c.errf("Publish")
}
func (c *rpcMQ) Subscribe(_ context.Context, _ string, _ func([]byte) error) error {
	return c.errf("Subscribe")
}

// --- constructors --------------------------------------------------------

// IAMRPCAt returns a ZAP-RPC IAM client targeting addr.
func IAMRPCAt(addr string) types.IAMClient {
	return &rpcIAM{rpcEndpoint{subsystem: "iam", addr: addr}}
}

// KMSRPCAt returns a ZAP-RPC KMS client targeting addr.
func KMSRPCAt(addr string) types.KMSClient {
	return &rpcKMS{rpcEndpoint{subsystem: "kms", addr: addr}}
}

// BaseRPCAt returns a ZAP-RPC Base client targeting addr.
func BaseRPCAt(addr string) types.BaseClient {
	return &rpcBase{rpcEndpoint{subsystem: "base", addr: addr}}
}

// AIRPCAt returns a ZAP-RPC AI client targeting addr.
func AIRPCAt(addr string) types.AIClient {
	return &rpcAI{rpcEndpoint{subsystem: "ai", addr: addr}}
}

// O11yRPCAt returns a ZAP-RPC O11y client targeting addr.
func O11yRPCAt(addr string) types.O11yClient {
	return &rpcO11y{rpcEndpoint{subsystem: "o11y", addr: addr}}
}

// VFSRPCAt returns a ZAP-RPC VFS client targeting addr.
func VFSRPCAt(addr string) types.VFSClient {
	return &rpcVFS{rpcEndpoint{subsystem: "vfs", addr: addr}}
}

// MQRPCAt returns a ZAP-RPC MQ client targeting addr.
func MQRPCAt(addr string) types.MQClient {
	return &rpcMQ{rpcEndpoint{subsystem: "mq", addr: addr}}
}
