// Package hooks provides event types for the hook system.
package hooks

// Resolver is the interface for events that can chain to the next handler.
// This matches the Base hook system pattern for consistency.
type Resolver interface {
	Next() error
	setNextFunc(func() error)
}

// Event is the base event type that implements Resolver.
// Embed this in custom event types to get chaining support.
type Event struct {
	nextFunc func() error
}

// Next calls the next handler in the chain.
func (e *Event) Next() error {
	if e.nextFunc != nil {
		return e.nextFunc()
	}
	return nil
}

// setNextFunc sets the next function (used by Hook.Trigger).
func (e *Event) setNextFunc(fn func() error) {
	e.nextFunc = fn
}

// Tagger is the interface for events that support tag-based filtering.
// Events implementing this can be selectively triggered based on tags.
type Tagger interface {
	Resolver
	Tags() []string
}

// TaggedEvent is an event that supports tag-based filtering.
type TaggedEvent struct {
	Event
	tags []string
}

// NewTaggedEvent creates a new tagged event with the given tags.
func NewTaggedEvent(tags ...string) *TaggedEvent {
	return &TaggedEvent{tags: tags}
}

// Tags returns the event tags.
func (e *TaggedEvent) Tags() []string {
	return e.tags
}

// SetTags sets the event tags.
func (e *TaggedEvent) SetTags(tags ...string) {
	e.tags = tags
}
