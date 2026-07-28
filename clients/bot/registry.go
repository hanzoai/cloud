// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The node registry: which bot nodes are connected, and how to reach one.
//
// A bot node runs on someone's machine and dials in, holding a long-lived
// socket. That socket is the only way to reach it — you cannot spawn a
// rendezvous per request, because the node is already attached to one. This is
// that rendezvous.
//
// # Org is part of a node's identity, not a lookup filter
//
// The TypeScript registry this replaces keyed nodes by id alone
// (nodesById: Map<string, NodeSession>), with no tenant dimension anywhere in
// the type. Isolation was then a property of deployment — one shared singleton,
// per-viewer bearers, careful caching — rather than of the data structure.
//
// Here a node is addressed by (org, nodeID). Two orgs may use the same node id
// and never see each other, and a lookup without an org cannot compile. That is
// the difference between multi-tenant and single-tenant-with-care.
package bot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Errors a caller is expected to handle.
var (
	// ErrNoSuchNode means no node with that id is connected FOR THAT ORG. It is
	// deliberately indistinguishable from "exists but belongs to another org":
	// telling those apart would leak the existence of another tenant's nodes.
	ErrNoSuchNode = errors.New("bot: no such node")

	// ErrInvokeTimeout means the node held the socket but did not answer.
	ErrInvokeTimeout = errors.New("bot: node invoke timed out")

	// ErrNodeGone means the socket closed while the call was in flight.
	ErrNodeGone = errors.New("bot: node disconnected mid-invoke")
)

// NodeKey addresses a node. Both fields are required; there is no way to name a
// node without naming its tenant.
type NodeKey struct {
	Org    string
	NodeID string
}

func (k NodeKey) String() string { return k.Org + "/" + k.NodeID }

// Session is a connected node.
type Session struct {
	Key         NodeKey
	ConnID      string
	DisplayName string
	Platform    string
	Version     string
	Caps        []string
	Commands    []string
	Permissions map[string]bool
	RemoteIP    string
	ConnectedAt time.Time

	// send delivers one frame to this node's socket. The transport owns
	// framing; the registry only needs a way to write.
	send func(context.Context, []byte) error
}

// InvokeResult is what a node answered.
type InvokeResult struct {
	OK      bool
	Payload []byte
	Code    string
	Message string
}

// Registry tracks connected nodes and correlates in-flight invocations.
//
// It is transport-agnostic on purpose: the WS layer registers a Session with a
// send func and feeds answers back by correlation id. Keeping the socket out of
// here is what lets the routing be tested without a network.
type Registry struct {
	mu       sync.RWMutex
	byKey    map[NodeKey]*Session
	byConn   map[string]NodeKey
	pending  map[string]chan InvokeResult
	nextCorr uint64
}

func NewRegistry() *Registry {
	return &Registry{
		byKey:   make(map[NodeKey]*Session),
		byConn:  make(map[string]NodeKey),
		pending: make(map[string]chan InvokeResult),
	}
}

// Register adds a connected node.
//
// A second connection for the same (org, node) replaces the first and closes
// nothing: the old socket is simply no longer addressable. A node that
// reconnects after a network blip would otherwise be unreachable behind a dead
// entry until a timeout expired.
func (r *Registry) Register(s *Session) error {
	if s == nil || s.Key.Org == "" || s.Key.NodeID == "" {
		return fmt.Errorf("bot: a session needs both an org and a node id")
	}
	if s.send == nil {
		return fmt.Errorf("bot: session %s has no send func", s.Key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.byKey[s.Key]; ok {
		delete(r.byConn, prev.ConnID)
	}
	r.byKey[s.Key] = s
	r.byConn[s.ConnID] = s.Key
	return nil
}

// Unregister removes a node by its connection id, and fails every call still
// waiting on it. A pending invoke whose node has gone will never be answered;
// leaving it to time out would hold the caller for the full timeout on a
// question that is already unanswerable.
func (r *Registry) Unregister(connID string) {
	r.mu.Lock()
	key, ok := r.byConn[connID]
	if ok {
		delete(r.byConn, connID)
		if s, exists := r.byKey[key]; exists && s.ConnID == connID {
			delete(r.byKey, key)
		}
	}
	var orphaned []chan InvokeResult
	for id, ch := range r.pending {
		if len(id) > len(connID) && id[:len(connID)] == connID {
			orphaned = append(orphaned, ch)
			delete(r.pending, id)
		}
	}
	r.mu.Unlock()

	for _, ch := range orphaned {
		select {
		case ch <- InvokeResult{OK: false, Code: "node_gone", Message: ErrNodeGone.Error()}:
		default:
		}
		close(ch)
	}
}

// List returns the nodes connected for one org, and only that org.
func (r *Registry) List(org string) []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Session, 0, len(r.byKey))
	for k, s := range r.byKey {
		if k.Org == org {
			out = append(out, s)
		}
	}
	return out
}

// Invoke sends a command to a node and waits for its answer.
//
// The org comes from the caller's validated identity, never from the request
// body, so a caller cannot reach another tenant's node by naming it.
func (r *Registry) Invoke(ctx context.Context, key NodeKey, frame func(corrID string) ([]byte, error), timeout time.Duration) (InvokeResult, error) {
	r.mu.Lock()
	s, ok := r.byKey[key]
	if !ok {
		r.mu.Unlock()
		return InvokeResult{}, ErrNoSuchNode
	}
	r.nextCorr++
	corrID := fmt.Sprintf("%s:%d", s.ConnID, r.nextCorr)
	ch := make(chan InvokeResult, 1)
	r.pending[corrID] = ch
	send := s.send
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.pending, corrID)
		r.mu.Unlock()
	}()

	payload, err := frame(corrID)
	if err != nil {
		return InvokeResult{}, err
	}
	if err := send(ctx, payload); err != nil {
		return InvokeResult{}, fmt.Errorf("bot: send to %s: %w", key, err)
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res, open := <-ch:
		if !open {
			return InvokeResult{}, ErrNodeGone
		}
		return res, nil
	case <-timer.C:
		return InvokeResult{}, ErrInvokeTimeout
	case <-ctx.Done():
		return InvokeResult{}, ctx.Err()
	}
}

// Answer delivers a node's reply to whoever is waiting on it.
//
// An answer with no waiter is dropped rather than queued: it means the caller
// already timed out or went away, and holding it would only surface as a reply
// to an unrelated later call.
func (r *Registry) Answer(corrID string, res InvokeResult) {
	r.mu.Lock()
	ch, ok := r.pending[corrID]
	if ok {
		delete(r.pending, corrID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- res:
	default:
	}
}
