// Package hooks provides an extensible hook system for Commerce.
//
// Hooks allow extensions and plugins to tap into various lifecycle events
// and modify behavior without changing core code.
//
// Available Hooks:
//   - OnBootstrap: Called during application initialization
//   - OnServe: Called when the server starts
//   - OnTerminate: Called during graceful shutdown
//   - OnRouteSetup: Called when setting up HTTP routes
//   - OnModelValidate: Called before model validation
//   - OnModelCreate: Called before creating a model
//   - OnModelUpdate: Called before updating a model
//   - OnModelDelete: Called before deleting a model
//
// Usage:
//
//	app.Hooks.OnModelCreate("Order").Bind(&hooks.Handler{
//	    ID: "validateInventory",
//	    Func: func(e *hooks.ModelEvent) error {
//	        // Check inventory before creating order
//	        return e.Next()
//	    },
//	})
package hooks

import (
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
)

// Handler represents a hook handler
type Handler[T any] struct {
	// ID is a unique identifier for this handler
	ID string

	// Priority determines execution order (lower = earlier)
	Priority int

	// Func is the handler function
	Func func(T) error
}

// Hook is a thread-safe collection of handlers
type Hook[T any] struct {
	handlers []*Handler[T]
	mu       sync.RWMutex
}

// NewHook creates a new hook
func NewHook[T any]() *Hook[T] {
	return &Hook[T]{
		handlers: make([]*Handler[T], 0),
	}
}

// Bind registers a handler (replaces if ID exists)
func (h *Hook[T]) Bind(handler *Handler[T]) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Remove existing handler with same ID
	for i, existing := range h.handlers {
		if existing.ID == handler.ID {
			h.handlers = append(h.handlers[:i], h.handlers[i+1:]...)
			break
		}
	}

	// Add new handler
	h.handlers = append(h.handlers, handler)

	// Sort by priority
	sort.Slice(h.handlers, func(i, j int) bool {
		return h.handlers[i].Priority < h.handlers[j].Priority
	})
}

// BindFunc registers a handler function with auto-generated ID
func (h *Hook[T]) BindFunc(fn func(T) error) {
	h.Bind(&Handler[T]{
		ID:   generateHandlerID(),
		Func: fn,
	})
}

// Unbind removes handlers by ID
func (h *Hook[T]) Unbind(ids ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}

	filtered := make([]*Handler[T], 0, len(h.handlers))
	for _, handler := range h.handlers {
		if !idSet[handler.ID] {
			filtered = append(filtered, handler)
		}
	}

	h.handlers = filtered
}

// Trigger executes all handlers in order
func (h *Hook[T]) Trigger(event T, defaultFn func(T) error) error {
	h.mu.RLock()
	handlers := make([]*Handler[T], len(h.handlers))
	copy(handlers, h.handlers)
	h.mu.RUnlock()

	// Build chain with default at the end
	chain := &handlerChain[T]{
		handlers:  handlers,
		defaultFn: defaultFn,
		index:     0,
	}

	return chain.next(event)
}

// Len returns the number of handlers
func (h *Hook[T]) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.handlers)
}

// handlerChain manages hook execution
type handlerChain[T any] struct {
	handlers  []*Handler[T]
	defaultFn func(T) error
	index     int
}

func (c *handlerChain[T]) next(event T) error {
	if c.index < len(c.handlers) {
		handler := c.handlers[c.index]
		c.index++
		return handler.Func(event)
	}

	// Execute default function if no more handlers
	if c.defaultFn != nil {
		return c.defaultFn(event)
	}

	return nil
}

// Event types

// AppEvent is the base event type containing the app reference
type AppEvent struct {
	App   interface{}
	chain *handlerChain[*AppEvent]
}

// Next calls the next handler in the chain
func (e *AppEvent) Next() error {
	if e.chain != nil {
		return e.chain.next(e)
	}
	return nil
}

// RouteEvent is emitted when setting up routes
type RouteEvent struct {
	App    interface{}
	Router *gin.RouterGroup
	chain  *handlerChain[*RouteEvent]
}

// Next calls the next handler in the chain
func (e *RouteEvent) Next() error {
	if e.chain != nil {
		return e.chain.next(e)
	}
	return nil
}

// ModelEvent is emitted for model operations
type ModelEvent struct {
	App   interface{}
	Kind  string
	Model interface{}
	IsNew bool
	chain *handlerChain[*ModelEvent]
}

// Next calls the next handler in the chain
func (e *ModelEvent) Next() error {
	if e.chain != nil {
		return e.chain.next(e)
	}
	return nil
}

// Registry manages all hooks for an application
type Registry struct {
	// Lifecycle hooks
	onBootstrap *Hook[*AppEvent]
	onServe     *Hook[*AppEvent]
	onTerminate *Hook[*AppEvent]

	// Route hooks
	onRouteSetup *Hook[*RouteEvent]

	// Model hooks (by kind)
	onModelValidate map[string]*Hook[*ModelEvent]
	onModelCreate   map[string]*Hook[*ModelEvent]
	onModelUpdate   map[string]*Hook[*ModelEvent]
	onModelDelete   map[string]*Hook[*ModelEvent]

	mu sync.RWMutex
}

// NewRegistry creates a new hook registry
func NewRegistry() *Registry {
	return &Registry{
		onBootstrap:     NewHook[*AppEvent](),
		onServe:         NewHook[*AppEvent](),
		onTerminate:     NewHook[*AppEvent](),
		onRouteSetup:    NewHook[*RouteEvent](),
		onModelValidate: make(map[string]*Hook[*ModelEvent]),
		onModelCreate:   make(map[string]*Hook[*ModelEvent]),
		onModelUpdate:   make(map[string]*Hook[*ModelEvent]),
		onModelDelete:   make(map[string]*Hook[*ModelEvent]),
	}
}

// OnBootstrap returns the bootstrap hook
func (r *Registry) OnBootstrap() *Hook[*AppEvent] {
	return r.onBootstrap
}

// OnServe returns the serve hook
func (r *Registry) OnServe() *Hook[*AppEvent] {
	return r.onServe
}

// OnTerminate returns the terminate hook
func (r *Registry) OnTerminate() *Hook[*AppEvent] {
	return r.onTerminate
}

// OnRouteSetup returns the route setup hook
func (r *Registry) OnRouteSetup() *Hook[*RouteEvent] {
	return r.onRouteSetup
}

// OnModelValidate returns the model validate hook for a kind
func (r *Registry) OnModelValidate(kind string) *Hook[*ModelEvent] {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h, ok := r.onModelValidate[kind]; ok {
		return h
	}

	h := NewHook[*ModelEvent]()
	r.onModelValidate[kind] = h
	return h
}

// OnModelCreate returns the model create hook for a kind
func (r *Registry) OnModelCreate(kind string) *Hook[*ModelEvent] {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h, ok := r.onModelCreate[kind]; ok {
		return h
	}

	h := NewHook[*ModelEvent]()
	r.onModelCreate[kind] = h
	return h
}

// OnModelUpdate returns the model update hook for a kind
func (r *Registry) OnModelUpdate(kind string) *Hook[*ModelEvent] {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h, ok := r.onModelUpdate[kind]; ok {
		return h
	}

	h := NewHook[*ModelEvent]()
	r.onModelUpdate[kind] = h
	return h
}

// OnModelDelete returns the model delete hook for a kind
func (r *Registry) OnModelDelete(kind string) *Hook[*ModelEvent] {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h, ok := r.onModelDelete[kind]; ok {
		return h
	}

	h := NewHook[*ModelEvent]()
	r.onModelDelete[kind] = h
	return h
}

// TriggerBootstrap triggers the bootstrap hook
func (r *Registry) TriggerBootstrap(app interface{}) error {
	event := &AppEvent{App: app}
	return r.onBootstrap.Trigger(event, nil)
}

// TriggerServe triggers the serve hook
func (r *Registry) TriggerServe(app interface{}) error {
	event := &AppEvent{App: app}
	return r.onServe.Trigger(event, nil)
}

// TriggerTerminate triggers the terminate hook
func (r *Registry) TriggerTerminate(app interface{}) error {
	event := &AppEvent{App: app}
	return r.onTerminate.Trigger(event, nil)
}

// TriggerRouteSetup triggers the route setup hook
func (r *Registry) TriggerRouteSetup(router *gin.RouterGroup) error {
	event := &RouteEvent{Router: router}
	return r.onRouteSetup.Trigger(event, nil)
}

// TriggerModelValidate triggers the model validate hook
func (r *Registry) TriggerModelValidate(kind string, model interface{}, isNew bool) error {
	r.mu.RLock()
	h, ok := r.onModelValidate[kind]
	r.mu.RUnlock()

	if !ok {
		return nil
	}

	event := &ModelEvent{Kind: kind, Model: model, IsNew: isNew}
	return h.Trigger(event, nil)
}

// TriggerModelCreate triggers the model create hook
func (r *Registry) TriggerModelCreate(kind string, model interface{}) error {
	r.mu.RLock()
	h, ok := r.onModelCreate[kind]
	r.mu.RUnlock()

	if !ok {
		return nil
	}

	event := &ModelEvent{Kind: kind, Model: model, IsNew: true}
	return h.Trigger(event, nil)
}

// TriggerModelUpdate triggers the model update hook
func (r *Registry) TriggerModelUpdate(kind string, model interface{}) error {
	r.mu.RLock()
	h, ok := r.onModelUpdate[kind]
	r.mu.RUnlock()

	if !ok {
		return nil
	}

	event := &ModelEvent{Kind: kind, Model: model, IsNew: false}
	return h.Trigger(event, nil)
}

// TriggerModelDelete triggers the model delete hook
func (r *Registry) TriggerModelDelete(kind string, model interface{}) error {
	r.mu.RLock()
	h, ok := r.onModelDelete[kind]
	r.mu.RUnlock()

	if !ok {
		return nil
	}

	event := &ModelEvent{Kind: kind, Model: model, IsNew: false}
	return h.Trigger(event, nil)
}

// handlerIDCounter for generating unique IDs
var (
	handlerIDCounter int
	handlerIDMu      sync.Mutex
)

func generateHandlerID() string {
	handlerIDMu.Lock()
	defer handlerIDMu.Unlock()
	handlerIDCounter++
	return string(rune('A' + handlerIDCounter%26)) + string(rune('0'+handlerIDCounter/26%10))
}
