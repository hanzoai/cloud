// Package search provides a search abstraction layer with pluggable backends.
// This replaces the google.golang.org/appengine/search dependency with an
// interface-based approach supporting multiple backends (Meilisearch, Elasticsearch,
// SQL LIKE queries, or in-memory).
package search

import (
	"context"
	"errors"
	"sync"
)

// Common errors
var (
	// Done is returned by Iterator.Next when the iteration is complete.
	Done = errors.New("search: no more results")

	// ErrNoBackend is returned when no search backend is configured.
	ErrNoBackend = errors.New("search: no backend configured")

	// ErrIndexNotFound is returned when the requested index doesn't exist.
	ErrIndexNotFound = errors.New("search: index not found")
)

// Backend is the interface that search backends must implement.
type Backend interface {
	// Open returns an Index for the given name.
	Open(name string) (Index, error)

	// Close releases any resources held by the backend.
	Close() error
}

// Index represents a searchable index.
type Index interface {
	// Search performs a search query and returns an iterator.
	Search(ctx context.Context, query string, opts *SearchOptions) Iterator

	// Put indexes a document with the given ID.
	Put(ctx context.Context, id string, doc interface{}) (string, error)

	// Get retrieves a document by ID.
	Get(ctx context.Context, id string, doc interface{}) error

	// Delete removes a document by ID.
	Delete(ctx context.Context, id string) error
}

// Iterator iterates over search results.
type Iterator interface {
	// Next returns the next result. Returns Done when iteration is complete.
	Next(dst interface{}) (string, error)

	// Count returns the total number of results (may be approximate).
	Count() int

	// Facets returns facet results if requested.
	Facets() ([][]FacetResult, error)
}

// SearchOptions configures a search query.
type SearchOptions struct {
	// IDsOnly returns only document IDs, not full documents.
	IDsOnly bool

	// Limit is the maximum number of results to return.
	Limit int

	// Offset is the number of results to skip.
	Offset int

	// Sort configures result sorting.
	Sort *SortOptions

	// Refinements filters results by facet values.
	Refinements []Facet

	// Facets configures which facets to return.
	Facets []FacetSearchOption

	// CountAccuracy configures count accuracy.
	CountAccuracy int
}

// SortOptions configures result sorting.
type SortOptions struct {
	Expressions []SortExpression
}

// SortExpression defines a sort field and direction.
type SortExpression struct {
	// Expr is the field to sort by.
	Expr string

	// Reverse sorts in descending order if true.
	Reverse bool

	// Default is the default value for missing fields.
	Default interface{}
}

// Facet represents a facet value for filtering or in results.
type Facet struct {
	// Name is the facet field name.
	Name string

	// Value is the facet value (Atom, Range, or string).
	Value interface{}
}

// FacetResult represents a facet value in search results.
type FacetResult struct {
	// Name is the facet field name.
	Name string

	// Value is the facet value.
	Value interface{}

	// Count is the number of documents with this facet value.
	Count int
}

// FacetSearchOption configures facet discovery in search.
type FacetSearchOption struct {
	// Name is the facet field name (empty for auto-discovery).
	Name string

	// ValueLimit limits the number of values returned per facet.
	ValueLimit int

	// DiscoveryLimit limits the number of facets discovered (for auto-discovery).
	DiscoveryLimit int
}

// Range represents a numeric range for faceting.
type Range struct {
	Start float64
	End   float64
}

// Atom is an indivisible string value for exact matching.
type Atom string

// AutoFacetDiscovery returns a FacetSearchOption for automatic facet discovery.
func AutoFacetDiscovery(discoveryLimit, valueLimit int) FacetSearchOption {
	return FacetSearchOption{
		DiscoveryLimit: discoveryLimit,
		ValueLimit:     valueLimit,
	}
}

// Global backend registry
var (
	defaultBackend Backend
	backendMu      sync.RWMutex
)

// SetBackend sets the default search backend.
func SetBackend(b Backend) {
	backendMu.Lock()
	defer backendMu.Unlock()
	defaultBackend = b
}

// GetBackend returns the default search backend.
func GetBackend() Backend {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return defaultBackend
}

// Open returns an Index for the given name using the default backend.
func Open(name string) (Index, error) {
	b := GetBackend()
	if b == nil {
		// Return a no-op index if no backend is configured
		return &noopIndex{name: name}, nil
	}
	return b.Open(name)
}

// Search performs a search on the given index using the default backend.
func Search(ctx context.Context, indexName string, query string, opts *SearchOptions) (Iterator, error) {
	index, err := Open(indexName)
	if err != nil {
		return nil, err
	}
	return index.Search(ctx, query, opts), nil
}

// Put indexes a document using the default backend.
func Put(ctx context.Context, indexName string, id string, doc interface{}) (string, error) {
	index, err := Open(indexName)
	if err != nil {
		return "", err
	}
	return index.Put(ctx, id, doc)
}

// Delete removes a document from the index using the default backend.
func Delete(ctx context.Context, indexName string, id string) error {
	index, err := Open(indexName)
	if err != nil {
		return err
	}
	return index.Delete(ctx, id)
}

// Get retrieves a document from the index using the default backend.
func Get(ctx context.Context, indexName string, id string, doc interface{}) error {
	index, err := Open(indexName)
	if err != nil {
		return err
	}
	return index.Get(ctx, id, doc)
}

// noopIndex is a no-operation index used when no backend is configured.
type noopIndex struct {
	name string
}

func (i *noopIndex) Search(ctx context.Context, query string, opts *SearchOptions) Iterator {
	return &noopIterator{}
}

func (i *noopIndex) Put(ctx context.Context, id string, doc interface{}) (string, error) {
	return id, nil
}

func (i *noopIndex) Get(ctx context.Context, id string, doc interface{}) error {
	return ErrIndexNotFound
}

func (i *noopIndex) Delete(ctx context.Context, id string) error {
	return nil
}

// noopIterator is a no-operation iterator that returns Done immediately.
type noopIterator struct{}

func (i *noopIterator) Next(dst interface{}) (string, error) {
	return "", Done
}

func (i *noopIterator) Count() int {
	return 0
}

func (i *noopIterator) Facets() ([][]FacetResult, error) {
	return [][]FacetResult{}, nil
}
