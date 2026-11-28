# Solution for Issue #40

## Summary
### 1. Technical Overview

AI agent sandboxes running untrusted LLM-generated code risk host escape via kernel vulnerabilities, unauthorized namespace manipulation (`setns`/`unshare`), interactive reverse shells, and illicit access to sensitive virtual filesystems (`/proc/kcore`, `/sys/firmware`). To establish deterministic, low-overhead kernel-level confinement, we introduce a Cilium Tetragon eBPF TracingPolicy suite combined with a dedicated Visor eBPF audit and enforcement controller. The con

## Implementation / Patch
```
### 1. Technical Overview

AI agent sandboxes running untrusted LLM-generated code risk host escape via kernel vulnerabilities, unauthorized namespace manipulation (`setns`/`unshare`), interactive reverse shells, and illicit access to sensitive virtual filesystems (`/proc/kcore`, `/sys/firmware`). To establish deterministic, low-overhead kernel-level confinement, we introduce a Cilium Tetragon eBPF TracingPolicy suite combined with a dedicated Visor eBPF audit and enforcement controller. The controller provisions strict LSM/kprobe-based kernel enforcement policies that trigger in-kernel `Sigkill` actions on escape attempts, intercepts Tetragon's gRPC audit stream in real time, and correlates sandbox container IDs with structured security telemetry.

---

### 2. Implementation

#### A. Tetragon Sandbox TracingPolicy (`deploy/tetragon/agent-sandbox-tracingpolicy.yaml`)

```yaml
apiVersion: cilium.io/v1alpha1
kind: TracingPolicy
metadata:
  name: hanzo-agent-sandbox-enforcement
  namespace: hanzo-sandboxes
spec:
  # 1. Enforce process execution restrictions & block shell escapes
  kprobes:
    # Block unauthorized namespace manipulations (container breakout attempt)
    - call: "sys_setns"
      syscall: true
      args:
        - index: 0
          type: "int"
        - index: 1
          type: "int"
      selectors:
        - matchNamespaces:
            - "hanzo-sandboxes"
          matchActions:
            - action: Sigkill
            - action: Post
    - call: "sys_unshare"
      syscall: true
      args:
        - index: 0
          type: "int"
      selectors:
        - matchNamespaces:
            - "hanzo-sandboxes"
          matchActions:
            - action: Sigkill
            - action: Post

    # Block ptrace / kernel debugging evasion inside sandboxes
    - call: "sys_ptrace"
      syscall: true
      args:
        - index: 0
          type: "int"
      selectors:
        - matchNamespaces:
            - "hanzo-sandboxes"
          matchActions:
            - action: Sigkill
            - action: Post

    # Block loading kernel modules or BPF injection from sandbox
    - call: "sys_finit_module"
      syscall: true
      selectors:
        - matchNamespaces:
            - "hanzo-sandboxes"
          matchActions:
            - action: Sigkill
            - action: Post
    - call: "sys_bpf"
      syscall: true
      selectors:
        - matchNamespaces:
            - "hanzo-sandboxes"
          matchActions:
            - action: Sigkill
            - action: Post

  # 2. Filesystem Protection via LSM hooks (Kernel 5.7+)
  lsm:
    - hook: "security_file_open"
      args:
        - index: 0
          type: "file"
      selectors:
        - matchNamespaces:
            - "hanzo-sandboxes"
          matchArgs:
            - index: 0
              operator: "Prefix"
              values:
                - "/proc/kcore"
                - "/proc/kallsyms"
                - "/sys/firmware"
                - "/sys/kernel/debug"
                - "/etc/kubernetes"
                - "/var/run/secrets/kubernetes.io"
          matchActions:
            - action: Sigkill
            - action: Post

  # 3. Restrict Network egress to raw sockets (prevent packet crafting / ARP spoofing)
  tracepoints:
    - subsystem: "syscalls"
      event: "sys_enter_socket"
      args:
        - index: 0
          type: "int" # domain (AF_INET=2, AF_PACKET=17, AF_NETLINK=16)
        - index: 1
          type: "int" # type (SOCK_RAW=3)
      selectors:
        - matchNamespaces:
            - "hanzo-sandboxes"
          matchArgs:
            - index: 1
              operator: "Equal"
              values:
                - "3" # SOCK_RAW
          matchActions:
            - action: Sigkill
            - action: Post
```

---

#### B. Visor Tetragon Event Monitor & Stream Consumer (`pkg/visor/enforcement/tetragon_monitor.go`)

```go
package enforcement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// SecurityViolation records an in-kernel eBPF sandbox policy breach.
type SecurityViolation struct {
	Timestamp     time.Time `json:"timestamp"`
	ContainerID   string    `json:"container_id"`
	PodNamespace  string    `json:"pod_namespace"`
	PodName       string    `json:"pod_name"`
	ProcessBinary string    `json:"process_binary"`
	Arguments     string    `json:"arguments"`
	PolicyName    string    `json:"policy_name"`
	Action        string    `json:"action"`
	Syscall       string    `json:"syscall,omitempty"`
}

// Handler handles captured violation events.
type Handler func(violation SecurityViolation)

// TetragonMonitor streams events from the Tetragon gRPC daemon.
type TetragonMonitor struct {
	addr        string
	conn        *grpc.ClientConn
	client      tetragon.FineGuidanceSensorsClient
	handlers    []Handler
	mu          sync.RWMutex
	stopCh      chan struct{}
	running     bool
}

// NewTetragonMonitor creates an instance of TetragonMonitor.
func NewTetragonMonitor(daemonAddr string) *TetragonMonitor {
	if daemonAddr == "" {
		daemonAddr = "localhost:54321"
	}
	return &TetragonMonitor{
		addr:   daemonAddr,
		stopCh: make(chan struct{}),
	}
}

// RegisterHandler registers a security violation handler.
func (tm *TetragonMonitor) RegisterHandler(h Handler) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.handlers = append(tm.handlers, h)
}

// Connect initializes the gRPC transport to Tetragon.
func (tm *TetragonMonitor) Connect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, tm.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to tetragon daemon at %s: %w", tm.addr, err)
	}

	tm.conn = conn
	tm.client = tetragon.NewFineGuidanceSensorsClient(conn)
	return nil
}

// Start begins processing the Tetragon audit stream.
func (tm *TetragonMonitor) Start(ctx context.Context) error {
	tm.mu.Lock()
	if tm.running {
		tm.mu.Unlock()
		return errors.New("tetragon monitor is already running")
	}
	if tm.client == nil {
		tm.mu.Unlock()
		return errors.New("tetragon client not initialized; call Connect first")
	}
	tm.running = true
	tm.mu.Unlock()

	req := &tetragon.GetEventsRequest{
		AllowList: []*tetragon.Filter{
			{
				Namespaces: []string{"hanzo-sandboxes"},
			},
		},
	}

	stream, err := tm.client.GetEvents(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to subscribe to tetragon events: %w", err)
	}

	go tm.consumeStream(stream)
	return nil
}

func (tm *TetragonMonitor) consumeStream(stream tetragon.FineGuidanceSensors_GetEventsClient) {
	for {
		select {
		case <-tm.stopCh:
			return
		default:
			res, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				return
			}
			tm.evaluateEvent(res)
		}
	}
}

func (tm *TetragonMonitor) evaluateEvent(res *tetragon.GetEventsResponse) {
	var violation *SecurityViolation

	switch ev := res.Event.(type) {
	case *tetragon.GetEventsResponse_ProcessKprobe:
		kprobe := ev.ProcessKprobe
		if kprobe.Action == tetragon.KprobeAction_KPROBE_ACTION_SIGKILL {
			violation = &SecurityViolation{
				Timestamp:     time.Now().UTC(),
				ContainerID:   kprobe.Process.Pod.Container.Id,
				PodNamespace:  kprobe.Process.Pod.Namespace,
				PodName:       kprobe.Process.Pod.Name,
				ProcessBinary: kprobe.Process.Binary,
				Arguments:     kprobe.Process.Arguments,
				PolicyName:    kprobe.PolicyName,
				Action:        "SIGKILL",
				Syscall:       kprobe.FunctionName,
			}
		}
	case *tetragon.GetEventsResponse_ProcessLsm:
		lsm := ev.ProcessLsm
		if lsm.Action == tetragon.KprobeAction_KPROBE_ACTION_SIGKILL {
			violation = &SecurityViolation{
				Timestamp:     time.Now().UTC(),
				ContainerID:   lsm.Process.Pod.Container.Id,
				PodNamespace:  lsm.Process.Pod.Namespace,
				PodName:       lsm.Process.Pod.Name,
				ProcessBinary: lsm.Process.Binary,
				Arguments:     lsm.Process.Arguments,
				PolicyName:    lsm.PolicyName,
				Action:        "SIGKILL",
				Syscall:       lsm.FunctionName,
			}
		}
	}

	if violation != nil {
		tm.mu.RLock()
		handlers := make([]Handler, len(tm.handlers))
		copy(handlers, tm.handlers)
		tm.mu.RUnlock()

		for _, handler := range handlers {
			handler(*violation)
		}
	}
}

// Stop shuts down the monitor and gRPC connection.
func (tm *TetragonMonitor) Stop() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if !tm.running {
		return
	}
	close(tm.stopCh)
	if tm.conn != nil {
		_ = tm.conn.Close()
	}
	tm.running = false
}
```

---

#### C. Sandbox Escape Verification Test (`pkg/visor/enforcement/tetragon_monitor_test.go`)

```go
package enforcement

import (
	"sync"
	"testing"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
)

func TestTetragonMonitor_EvaluateEvent(t *testing.T) {
	monitor := &TetragonMonitor{}
	var captured ViolationNotification
	var wg sync.WaitGroup
	wg.Add(1)

	monitor.RegisterHandler(func(v SecurityViolation) {
		captured.Violation = v
		wg.Done()
	})

	mockEvent := &tetragon.GetEventsResponse{
		Event: &tetragon.GetEventsResponse_ProcessKprobe{
			ProcessKprobe: &tetragon.ProcessKprobe{
				PolicyName:   "hanzo-agent-sandbox-enforcement",
				Action:       tetragon.KprobeAction_KPROBE_ACTION_SIGKILL,
				FunctionName: "sys_setns",
				Process: &tetragon.Process{
					Binary:    "/usr/bin/nsenter",
					Arguments: "--target 1 --mount",
					Pod: &tetragon.Pod{
						Namespace: "hanzo-sandboxes",
						Name:      "agent-exec-uuid-9842",
						Container: &tetragon.Container{
							Id: "containerd://b8f2d8719bc4a",
						},
					},
				},
			},
		},
	}

	monitor.evaluateEvent(mockEvent)

	if waitTimeout(&wg, 2*time.Second) {
		t.Fatal("timed out waiting for security violation handler")
	}

	if captured.Violation.Action != "SIGKILL" {
		t.Errorf("expected action SIGKILL, got %s", captured.Violation.Action)
	}
	if captured.Violation.Syscall != "sys_setns" {
		t.Errorf("expected syscall sys_setns, got %s", captured.Violation.Syscall)
	}
	if captured.Violation.PodName != "agent-exec-uuid-9842" {
		t.Errorf("expected pod agent-exec-uuid-9842, got %s", captured.Violation.PodName)
	}
}

type ViolationNotification struct {
	Violation SecurityViolation
}

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false
	case <-time.After(timeout):
		return true
	}
}
```
```

## Payout Address (Solana)
`3EZnNP2kfmY161XyqnRWEFgtYw6jWFXHnimU5RMqA8g8`
