// Package nscontext provides namespace context functionality that replaces
// google.golang.org/appengine.Namespace. It uses context values to store
// the namespace, which can be retrieved by the datastore package.
package nscontext

import (
	"context"
)

// namespaceKey is the context key for namespace values.
type namespaceKey struct{}

// WithNamespace returns a new context with the given namespace.
// This replaces appengine.Namespace(ctx, ns).
func WithNamespace(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, namespaceKey{}, namespace)
}

// GetNamespace returns the namespace from the context, or empty string if not set.
func GetNamespace(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if ns, ok := ctx.Value(namespaceKey{}).(string); ok {
		return ns
	}
	return ""
}

// HasNamespace returns true if the context has a namespace set.
func HasNamespace(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(namespaceKey{}).(string)
	return ok
}
