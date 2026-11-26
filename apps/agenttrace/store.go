// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// Store isolation and lookup errors.
var (
	ErrTaskNotFound      = errors.New("agenttrace: task not found")
	ErrCrossTenantDenied = errors.New("agenttrace: cross-tenant trace access denied")
	ErrCrossTenantContam = errors.New("agenttrace: cross-tenant span injection rejected")
	ErrEmptyTenantScope  = errors.New("agenttrace: authenticated org_id required")
)

// TraceStoreConfig configures the bounded multi-tenant in-memory TraceStore.
type TraceStoreConfig struct {
	// MaxTasksPerOrg caps retained tasks per org (FIFO eviction when full). Default 1024.
	MaxTasksPerOrg int
	// MaxSpansPerTask caps retained spans per task to bound memory. Default 2048.
	MaxSpansPerTask int
}

// TraceStoreStats reports aggregate non-PII store telemetry.
type TraceStoreStats struct {
	OrgCount              int    `json:"orgCount"`
	TaskCount             int    `json:"taskCount"`
	StoredSpans           uint64 `json:"storedSpans"`
	EvictedTasks          uint64 `json:"evictedTasks"`
	CrossTenantRejections uint64 `json:"crossTenantRejections"`
	CrossTenantDenials    uint64 `json:"crossTenantDenials"`
}

type taskRecord struct {
	orgID    string
	tenantID string
	taskID   string
	traceID  TraceID
	spans    []Span
}

type orgBucket struct {
	order []string
	tasks map[string]*taskRecord
}

// TraceStore indexes exported spans strictly by (org_id, task_id) and enforces
// fail-closed multi-tenant isolation on every read and write.
type TraceStore struct {
	mu              sync.RWMutex
	maxTasksPerOrg  int
	maxSpansPerTask int
	orgs            map[string]*orgBucket
	// taskOwner tracks which org owns a task_id (used purely to detect and block
	// cross-tenant span injection or cross-tenant reads without exposing data).
	taskOwner map[string]string

	storedSpans           atomic.Uint64
	evictedTasks          atomic.Uint64
	crossTenantRejections atomic.Uint64
	crossTenantDenials    atomic.Uint64
}

// NewTraceStore creates a bounded tenant-isolated TraceStore.
func NewTraceStore(cfg TraceStoreConfig) *TraceStore {
	if cfg.MaxTasksPerOrg <= 0 {
		cfg.MaxTasksPerOrg = 1024
	}
	if cfg.MaxSpansPerTask <= 0 {
		cfg.MaxSpansPerTask = 2048
	}
	return &TraceStore{
		maxTasksPerOrg:  cfg.MaxTasksPerOrg,
		maxSpansPerTask: cfg.MaxSpansPerTask,
		orgs:            make(map[string]*orgBucket),
		taskOwner:       make(map[string]string),
	}
}

// Ingest stores a single span after validating non-empty org_id/task_id and
// enforcing that no task_id can ever be contaminated by a second org/tenant.
func (s *TraceStore) Ingest(sp Span) error {
	if sp.OrgID == "" || sp.TaskID == "" {
		return ErrEmptyTenantScope
	}
	if sp.TenantID == "" {
		sp.TenantID = sp.OrgID
	}
	cloned := sp.Clone()

	s.mu.Lock()
	defer s.mu.Unlock()

	if owner, exists := s.taskOwner[cloned.TaskID]; exists && owner != cloned.OrgID {
		s.crossTenantRejections.Add(1)
		return ErrCrossTenantContam
	}

	bucket := s.orgs[cloned.OrgID]
	if bucket == nil {
		bucket = &orgBucket{
			tasks: make(map[string]*taskRecord),
		}
		s.orgs[cloned.OrgID] = bucket
	}

	rec := bucket.tasks[cloned.TaskID]
	if rec == nil {
		// Evict oldest task in this org bucket if at capacity.
		if len(bucket.order) >= s.maxTasksPerOrg {
			evictID := bucket.order[0]
			bucket.order = bucket.order[1:]
			delete(bucket.tasks, evictID)
			if s.taskOwner[evictID] == cloned.OrgID {
				delete(s.taskOwner, evictID)
			}
			s.evictedTasks.Add(1)
		}
		rec = &taskRecord{
			orgID:    cloned.OrgID,
			tenantID: cloned.TenantID,
			taskID:   cloned.TaskID,
			traceID:  cloned.TraceID,
			spans:    make([]Span, 0, 8),
		}
		bucket.tasks[cloned.TaskID] = rec
		bucket.order = append(bucket.order, cloned.TaskID)
		s.taskOwner[cloned.TaskID] = cloned.OrgID
	} else if rec.tenantID != cloned.TenantID {
		s.crossTenantRejections.Add(1)
		return ErrCrossTenantContam
	}

	if len(rec.spans) < s.maxSpansPerTask {
		rec.spans = append(rec.spans, cloned)
		s.storedSpans.Add(1)
	}
	return nil
}

// ExportBatch implements Sink so TraceStore can plug directly into BatchExporter.
func (s *TraceStore) ExportBatch(_ context.Context, spans []Span, _ []byte) error {
	var firstErr error
	for i := range spans {
		if err := s.Ingest(spans[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// GetTask returns deep-copied spans for taskID strictly scoped to (orgID, tenantID).
// If orgID is empty, fails closed with ErrEmptyTenantScope.
// If taskID belongs to a different org or tenant, fails closed with ErrCrossTenantDenied.
func (s *TraceStore) GetTask(orgID, tenantID, taskID string) ([]Span, error) {
	if orgID == "" {
		return nil, ErrEmptyTenantScope
	}
	if taskID == "" {
		return nil, ErrTaskNotFound
	}
	if tenantID == "" {
		tenantID = orgID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	owner, exists := s.taskOwner[taskID]
	if !exists {
		return nil, ErrTaskNotFound
	}
	if owner != orgID {
		s.crossTenantDenials.Add(1)
		return nil, ErrCrossTenantDenied
	}
	bucket := s.orgs[orgID]
	if bucket == nil {
		return nil, ErrTaskNotFound
	}
	rec := bucket.tasks[taskID]
	if rec == nil {
		return nil, ErrTaskNotFound
	}
	if rec.tenantID != tenantID {
		s.crossTenantDenials.Add(1)
		return nil, ErrCrossTenantDenied
	}

	out := make([]Span, len(rec.spans))
	for i := range rec.spans {
		out[i] = rec.spans[i].Clone()
	}
	return out, nil
}

// ListTaskIDs returns all task IDs owned strictly by (orgID, tenantID), sorted
// deterministically. Never exposes tasks belonging to any other org or tenant.
func (s *TraceStore) ListTaskIDs(orgID, tenantID string) ([]string, error) {
	if orgID == "" {
		return nil, ErrEmptyTenantScope
	}
	if tenantID == "" {
		tenantID = orgID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bucket := s.orgs[orgID]
	if bucket == nil {
		return nil, nil
	}
	out := make([]string, 0, len(bucket.tasks))
	for id, rec := range bucket.tasks {
		if rec.tenantID == tenantID {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Stats returns aggregate non-tenant-specific store telemetry.
func (s *TraceStore) Stats() TraceStoreStats {
	s.mu.RLock()
	orgCount := len(s.orgs)
	taskCount := 0
	for _, b := range s.orgs {
		taskCount += len(b.tasks)
	}
	s.mu.RUnlock()
	return TraceStoreStats{
		OrgCount:              orgCount,
		TaskCount:             taskCount,
		StoredSpans:           s.storedSpans.Load(),
		EvictedTasks:          s.evictedTasks.Load(),
		CrossTenantRejections: s.crossTenantRejections.Load(),
		CrossTenantDenials:    s.crossTenantDenials.Load(),
	}
}
