// Package infra provides infrastructure clients.
//
// This file implements the Temporal client for workflow orchestration
// and background task execution.
package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	"github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

// TasksConfig holds Temporal configuration
type TasksConfig struct {
	// Enabled enables the tasks service
	Enabled bool

	// HostPort is the Temporal server address
	HostPort string

	// Namespace is the Temporal namespace
	Namespace string

	// Identity is the worker identity
	Identity string

	// TaskQueue is the default task queue
	TaskQueue string

	// TLS configuration
	TLS *TasksTLSConfig
}

// TasksTLSConfig holds TLS configuration for Temporal
type TasksTLSConfig struct {
	// CertPath is the path to the client certificate
	CertPath string

	// KeyPath is the path to the client key
	KeyPath string

	// CACertPath is the path to the CA certificate
	CACertPath string

	// ServerName for TLS verification
	ServerName string
}

// TasksClient wraps the Temporal client
type TasksClient struct {
	config   *TasksConfig
	client   client.Client
	workers  map[string]worker.Worker
}

// NewTasksClient creates a new Temporal tasks client
func NewTasksClient(ctx context.Context, cfg *TasksConfig) (*TasksClient, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.TaskQueue == "" {
		cfg.TaskQueue = "commerce"
	}

	opts := client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
		Identity:  cfg.Identity,
	}

	c, err := client.Dial(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to temporal: %w", err)
	}

	return &TasksClient{
		config:  cfg,
		client:  c,
		workers: make(map[string]worker.Worker),
	}, nil
}

// StartWorkflow starts a new workflow execution
func (c *TasksClient) StartWorkflow(ctx context.Context, opts *StartWorkflowOptions, workflowFunc interface{}, args ...interface{}) (*WorkflowRun, error) {
	if opts.TaskQueue == "" {
		opts.TaskQueue = c.config.TaskQueue
	}

	startOpts := client.StartWorkflowOptions{
		ID:                       opts.ID,
		TaskQueue:                opts.TaskQueue,
		WorkflowExecutionTimeout: opts.ExecutionTimeout,
		WorkflowRunTimeout:       opts.RunTimeout,
		WorkflowTaskTimeout:      opts.TaskTimeout,
	}

	if opts.CronSchedule != "" {
		startOpts.CronSchedule = opts.CronSchedule
	}

	if len(opts.SearchAttributes) > 0 {
		startOpts.SearchAttributes = opts.SearchAttributes
	}

	if len(opts.Memo) > 0 {
		startOpts.Memo = opts.Memo
	}

	run, err := c.client.ExecuteWorkflow(ctx, startOpts, workflowFunc, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to start workflow: %w", err)
	}

	return &WorkflowRun{
		ID:    run.GetID(),
		RunID: run.GetRunID(),
		run:   run,
	}, nil
}

// GetWorkflow retrieves a workflow run
func (c *TasksClient) GetWorkflow(ctx context.Context, workflowID, runID string) *WorkflowRun {
	run := c.client.GetWorkflow(ctx, workflowID, runID)
	return &WorkflowRun{
		ID:    run.GetID(),
		RunID: run.GetRunID(),
		run:   run,
	}
}

// SignalWorkflow sends a signal to a workflow
func (c *TasksClient) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error {
	err := c.client.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
	if err != nil {
		return fmt.Errorf("failed to signal workflow: %w", err)
	}
	return nil
}

// SignalWithStartWorkflow signals or starts a workflow
func (c *TasksClient) SignalWithStartWorkflow(ctx context.Context, workflowID string, signalName string, signalArg interface{}, opts *StartWorkflowOptions, workflowFunc interface{}, workflowArgs ...interface{}) (*WorkflowRun, error) {
	if opts.TaskQueue == "" {
		opts.TaskQueue = c.config.TaskQueue
	}

	startOpts := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                opts.TaskQueue,
		WorkflowExecutionTimeout: opts.ExecutionTimeout,
		WorkflowRunTimeout:       opts.RunTimeout,
		WorkflowTaskTimeout:      opts.TaskTimeout,
	}

	run, err := c.client.SignalWithStartWorkflow(ctx, workflowID, signalName, signalArg, startOpts, workflowFunc, workflowArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to signal with start workflow: %w", err)
	}

	return &WorkflowRun{
		ID:    run.GetID(),
		RunID: run.GetRunID(),
		run:   run,
	}, nil
}

// CancelWorkflow cancels a workflow execution
func (c *TasksClient) CancelWorkflow(ctx context.Context, workflowID, runID string) error {
	err := c.client.CancelWorkflow(ctx, workflowID, runID)
	if err != nil {
		return fmt.Errorf("failed to cancel workflow: %w", err)
	}
	return nil
}

// TerminateWorkflow terminates a workflow execution
func (c *TasksClient) TerminateWorkflow(ctx context.Context, workflowID, runID, reason string) error {
	err := c.client.TerminateWorkflow(ctx, workflowID, runID, reason)
	if err != nil {
		return fmt.Errorf("failed to terminate workflow: %w", err)
	}
	return nil
}

// QueryWorkflow queries a running workflow
func (c *TasksClient) QueryWorkflow(ctx context.Context, workflowID, runID, queryType string, args ...interface{}) (interface{}, error) {
	resp, err := c.client.QueryWorkflow(ctx, workflowID, runID, queryType, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query workflow: %w", err)
	}

	var result interface{}
	if err := resp.Get(&result); err != nil {
		return nil, fmt.Errorf("failed to decode query result: %w", err)
	}

	return result, nil
}

// NewWorker creates a new worker for a task queue
func (c *TasksClient) NewWorker(taskQueue string, opts *WorkerOptions) (worker.Worker, error) {
	if taskQueue == "" {
		taskQueue = c.config.TaskQueue
	}

	workerOpts := worker.Options{}

	if opts != nil {
		workerOpts.MaxConcurrentActivityExecutionSize = opts.MaxConcurrentActivities
		workerOpts.MaxConcurrentWorkflowTaskExecutionSize = opts.MaxConcurrentWorkflows
		workerOpts.MaxConcurrentLocalActivityExecutionSize = opts.MaxConcurrentLocalActivities
		workerOpts.WorkerActivitiesPerSecond = opts.ActivitiesPerSecond
		workerOpts.WorkerLocalActivitiesPerSecond = opts.LocalActivitiesPerSecond
		workerOpts.TaskQueueActivitiesPerSecond = opts.TaskQueueActivitiesPerSecond
		workerOpts.EnableSessionWorker = opts.EnableSessionWorker
	}

	w := worker.New(c.client, taskQueue, workerOpts)
	c.workers[taskQueue] = w

	return w, nil
}

// StartWorker starts a worker (blocking)
func (c *TasksClient) StartWorker(taskQueue string) error {
	w, ok := c.workers[taskQueue]
	if !ok {
		return fmt.Errorf("worker not found for queue: %s", taskQueue)
	}

	return w.Run(worker.InterruptCh())
}

// StartAllWorkers starts all workers in background
func (c *TasksClient) StartAllWorkers() error {
	for queue, w := range c.workers {
		go func(q string, wrk worker.Worker) {
			if err := wrk.Run(worker.InterruptCh()); err != nil {
				fmt.Printf("Worker %s error: %v\n", q, err)
			}
		}(queue, w)
	}
	return nil
}

// Health checks the Temporal connection
func (c *TasksClient) Health(ctx context.Context) HealthStatus {
	start := time.Now()

	// Try a health check
	_, err := c.client.CheckHealth(ctx, nil)
	if err != nil {
		return HealthStatus{
			Healthy: false,
			Latency: time.Since(start),
			Error:   err.Error(),
		}
	}

	return HealthStatus{
		Healthy: true,
		Latency: time.Since(start),
	}
}

// Close closes the Temporal client and workers
func (c *TasksClient) Close() {
	for _, w := range c.workers {
		w.Stop()
	}
	c.client.Close()
}

// Client returns the underlying Temporal client for advanced operations
func (c *TasksClient) Client() client.Client {
	return c.client
}

// StartWorkflowOptions configures workflow execution
type StartWorkflowOptions struct {
	ID               string
	TaskQueue        string
	ExecutionTimeout time.Duration
	RunTimeout       time.Duration
	TaskTimeout      time.Duration
	CronSchedule     string
	SearchAttributes map[string]interface{}
	Memo             map[string]interface{}
	RetryPolicy      *RetryPolicy
}

// RetryPolicy configures activity/workflow retry behavior
type RetryPolicy struct {
	InitialInterval    time.Duration
	BackoffCoefficient float64
	MaximumInterval    time.Duration
	MaximumAttempts    int32
}

// WorkflowRun represents a workflow execution
type WorkflowRun struct {
	ID    string
	RunID string
	run   client.WorkflowRun
}

// Get waits for the workflow to complete and returns the result
func (r *WorkflowRun) Get(ctx context.Context, result interface{}) error {
	return r.run.Get(ctx, result)
}

// GetWithOptions waits with options
func (r *WorkflowRun) GetWithOptions(ctx context.Context, result interface{}, opts client.WorkflowRunGetOptions) error {
	return r.run.GetWithOptions(ctx, result, opts)
}

// WorkerOptions configures a worker
type WorkerOptions struct {
	MaxConcurrentActivities        int
	MaxConcurrentWorkflows         int
	MaxConcurrentLocalActivities   int
	ActivitiesPerSecond            float64
	LocalActivitiesPerSecond       float64
	TaskQueueActivitiesPerSecond   float64
	EnableSessionWorker            bool
}

// Workflow utilities for workflow implementations

// ActivityOptions returns activity options for use in workflows
func ActivityOptions(taskQueue string, timeout time.Duration, retries int32) workflow.ActivityOptions {
	rp := &RetryPolicyInternal{
		MaximumAttempts: retries,
	}
	return workflow.ActivityOptions{
		TaskQueue:           taskQueue,
		StartToCloseTimeout: timeout,
		RetryPolicy:         rp.ToTemporal(),
	}
}

// LocalActivityOptions returns local activity options
func LocalActivityOptions(timeout time.Duration, retries int32) workflow.LocalActivityOptions {
	rp := &RetryPolicyInternal{
		MaximumAttempts: retries,
	}
	return workflow.LocalActivityOptions{
		StartToCloseTimeout: timeout,
		RetryPolicy:         rp.ToTemporal(),
	}
}

// RetryPolicyInternal is internal retry policy that converts to temporal
type RetryPolicyInternal struct {
	InitialInterval    time.Duration
	BackoffCoefficient float64
	MaximumInterval    time.Duration
	MaximumAttempts    int32
}

// ToTemporal converts to temporal retry policy
func (r *RetryPolicyInternal) ToTemporal() *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    r.InitialInterval,
		BackoffCoefficient: r.BackoffCoefficient,
		MaximumInterval:    r.MaximumInterval,
		MaximumAttempts:    r.MaximumAttempts,
	}
}
