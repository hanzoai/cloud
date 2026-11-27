// Package cloud provides root-level HIP-0106 compatibility types for legacy
// standalone subsystems that import "github.com/hanzoai/cloud" directly.
//
// New subsystems and plugins import "github.com/hanzoai/cloud/sdk" (and the
// light-host router lives in "github.com/hanzoai/cloud/cmd/cloud/host").
package cloud

import (
	"sync"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/types"
)

// Deps is the legacy HIP-0106 dependency bundle kept at the module root so
// external consumers (e.g. hanzoai/mcp/go) remain source-compatible.
type Deps struct {
	Logger         luxlog.Logger
	Brand          string
	Version        string
	Env            string
	Domain         string
	IAMIssuer      string
	DataDir        string
	AIDefaultModel string
	AITransport    string

	IAM      IAMClient
	KMS      KMSClient
	Base     BaseClient
	AI       AIClient
	Embed    AIClient
	O11y     O11yClient
	VFS      VFSClient
	MQ       MQClient
	Payments any
	Vault    any

	Audit *audit.Recorder
}

type IAMClient = types.IAMClient
type KMSClient = types.KMSClient
type BaseClient = types.BaseClient
type AIClient = types.AIClient
type O11yClient = types.O11yClient
type VFSClient = types.VFSClient
type MQClient = types.MQClient

type Claims = types.Claims
type User = types.User
type Org = types.Org
type OrgRef = types.OrgRef
type DBHandle = types.DBHandle
type ChatRequest = types.ChatRequest
type ChatResponse = types.ChatResponse
type EmbedRequest = types.EmbedRequest
type Counter = types.Counter
type Timing = types.Timing
type Span = types.Span

// MountFunc mounts a legacy HIP-0106 subsystem onto an untyped router (*zip.App).
type MountFunc func(app any, deps Deps) error

// Entry is one registered legacy subsystem.
type Entry struct {
	Name  string
	Order int
	Mount MountFunc
}

var (
	regMu    sync.RWMutex
	registry []Entry
)

// Register records a legacy HIP-0106 subsystem mount hook.
func Register(name string, order int, fn MountFunc) {
	regMu.Lock()
	defer regMu.Unlock()
	registry = append(registry, Entry{Name: name, Order: order, Mount: fn})
}

// Registry returns a snapshot of registered legacy HIP-0106 mount hooks.
func Registry() []Entry {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Entry, len(registry))
	copy(out, registry)
	return out
}
