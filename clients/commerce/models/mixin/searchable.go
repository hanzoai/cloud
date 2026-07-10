package mixin

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

var DefaultIndex = "everything"

// Document represents a searchable document
type Document interface {
	Id() string
}

// Searchable is implemented by models that can be indexed for search
type Searchable interface {
	Document() Document
}

// SearchIndex provides an interface for search operations
// This allows pluggable search backends (Elasticsearch, Meilisearch, etc.)
type SearchIndex interface {
	Put(ctx context.Context, id string, doc interface{}) error
	Delete(ctx context.Context, id string) error
}

// searchIndexProvider holds the current search index implementation
var searchIndexProvider SearchIndex

// SetSearchIndex sets the search index implementation to use
func SetSearchIndex(index SearchIndex) {
	searchIndexProvider = index
}

// GetSearchIndex returns the current search index, or nil if not set
func GetSearchIndex() SearchIndex {
	return searchIndexProvider
}

func (m BaseModel) PutDocument() error {
	hook, ok := m.Entity.(Searchable)
	if !ok {
		// Not a searchable model, do nothing
		return nil
	}

	if doc := hook.Document(); doc != nil {
		if searchIndexProvider == nil {
			log.Debug("Search index not configured, skipping PutDocument for %s/%s", m.Kind(), m.Id(), m.Db.Context)
			return nil
		}

		err := searchIndexProvider.Put(m.Db.Context, m.Id(), doc)
		if err != nil {
			log.Error("Could not save search document for '%s' with id %s\nError: %s", m.Kind(), m.Id(), err, m.Db.Context)
			return err
		}
	}

	return nil
}

func (m BaseModel) DeleteDocument() error {
	hook, ok := m.Entity.(Searchable)
	if !ok {
		// Not a searchable model, do nothing
		return nil
	}

	if doc := hook.Document(); doc != nil {
		if searchIndexProvider == nil {
			log.Debug("Search index not configured, skipping DeleteDocument for %s/%s", m.Kind(), m.Id(), m.Db.Context)
			return nil
		}

		err := searchIndexProvider.Delete(m.Db.Context, m.Id())
		if err != nil {
			log.Error("Could not delete search document for model with id %v", m.Id(), m.Db.Context)
			return err
		}
	}

	return nil
}
