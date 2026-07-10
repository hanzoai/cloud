// Package hooks provides tagged hooks for selective event handling.
package hooks

import "sync"

// TaggedHook wraps a Hook and filters execution based on event tags.
// This matches the Base hook system pattern for consistency.
type TaggedHook[T Tagger] struct {
	baseHook *Hook[T]
	tags     []string
	mu       sync.RWMutex
}

// NewTaggedHook creates a new TaggedHook wrapping the given base hook.
// If no tags are provided, the hook matches all events.
func NewTaggedHook[T Tagger](base *Hook[T], tags ...string) *TaggedHook[T] {
	return &TaggedHook[T]{
		baseHook: base,
		tags:     tags,
	}
}

// Bind registers a handler that will only fire for matching tags.
func (th *TaggedHook[T]) Bind(handler *Handler[T]) string {
	// Wrap the handler function to check tags
	originalFunc := handler.Func
	handler.Func = func(event T) error {
		if !th.CanTriggerOn(event.Tags()) {
			// Skip this handler but continue the chain
			return event.Next()
		}
		return originalFunc(event)
	}

	th.baseHook.Bind(handler)
	return handler.ID
}

// BindFunc registers a handler function that will only fire for matching tags.
func (th *TaggedHook[T]) BindFunc(fn func(T) error) string {
	handler := &Handler[T]{
		ID:   generateHandlerID(),
		Func: fn,
	}
	return th.Bind(handler)
}

// Unbind removes handlers by ID.
func (th *TaggedHook[T]) Unbind(ids ...string) {
	th.baseHook.Unbind(ids...)
}

// CanTriggerOn checks if this TaggedHook should trigger for the given event tags.
func (th *TaggedHook[T]) CanTriggerOn(eventTags []string) bool {
	th.mu.RLock()
	hookTags := th.tags
	th.mu.RUnlock()

	// No hook tags means match all events
	if len(hookTags) == 0 {
		return true
	}

	// Check if any hook tag matches any event tag
	for _, hookTag := range hookTags {
		for _, eventTag := range eventTags {
			if hookTag == eventTag {
				return true
			}
		}
	}

	return false
}

// SetTags updates the tags this hook filters on.
func (th *TaggedHook[T]) SetTags(tags ...string) {
	th.mu.Lock()
	th.tags = tags
	th.mu.Unlock()
}

// Tags returns the current filter tags.
func (th *TaggedHook[T]) Tags() []string {
	th.mu.RLock()
	defer th.mu.RUnlock()
	result := make([]string, len(th.tags))
	copy(result, th.tags)
	return result
}
