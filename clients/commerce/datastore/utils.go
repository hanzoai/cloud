package datastore

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

// registeredNamespaces allows external code to register available namespaces
var registeredNamespaces []string

// RegisterNamespace registers a namespace for use
func RegisterNamespace(ns string) {
	registeredNamespaces = append(registeredNamespaces, ns)
}

// GetNamespaces returns all namespaces
// In the new architecture, namespaces are registered explicitly
func GetNamespaces(ctx context.Context) []string {
	// Return registered namespaces if any
	if len(registeredNamespaces) > 0 {
		return registeredNamespaces
	}

	namespaces := make([]string, 0)

	// Try to fetch namespaces from special __namespace__ table
	db := New(ctx)
	if db.database == nil {
		log.Debug("GetNamespaces: database not initialized, returning empty list")
		return namespaces
	}

	keys, err := db.Query("__namespace__").GetKeys()
	if err != nil {
		log.Warn("GetNamespaces error: %v", err)
		return namespaces
	}

	// Append stringID's
	for _, k := range keys {
		namespaces = append(namespaces, k.StringID())
	}

	return namespaces
}

// GetKinds returns all entity kinds
func GetKinds(ctx context.Context) []string {
	kinds := make([]string, 0)
	db := New(ctx)
	if db.database == nil {
		log.Debug("GetKinds: database not initialized, returning empty list")
		return kinds
	}

	keys, err := db.Query("__kind__").GetKeys()
	if err != nil {
		log.Warn("GetKinds error: %v", err)
		return kinds
	}

	for _, k := range keys {
		kinds = append(kinds, k.StringID())
	}

	return kinds
}
