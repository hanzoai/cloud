// Package db provides database abstractions.
// This file contains the Model base type for entities using the new db.DB interface.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrEntityNotFound is returned when Get operations fail to find an entity
	ErrEntityNotFound = ErrNoSuchEntity

	// ErrValidationFailed is returned when entity validation fails
	ErrValidationFailed = errors.New("db: validation failed")
)

// Kind interface for entities that have a kind/table name
type Kind interface {
	Kind() string
}

// Validator interface for entities that support validation
type Validator interface {
	Validate() error
}

// BeforeCreateHook is called before entity creation
type BeforeCreateHook interface {
	BeforeCreate() error
}

// AfterCreateHook is called after entity creation
type AfterCreateHook interface {
	AfterCreate() error
}

// BeforeUpdateHook is called before entity update
type BeforeUpdateHook interface {
	BeforeUpdate(prev interface{}) error
}

// AfterUpdateHook is called after entity update
type AfterUpdateHook interface {
	AfterUpdate(prev interface{}) error
}

// BeforeDeleteHook is called before entity deletion
type BeforeDeleteHook interface {
	BeforeDelete() error
}

// AfterDeleteHook is called after entity deletion
type AfterDeleteHook interface {
	AfterDelete() error
}

// Model is a base type that provides common functionality for entities
// using the new db.DB interface. Embed this in your entity structs.
type Model struct {
	// Database reference
	db DB `json:"-"`

	// Key for this entity
	key Key `json:"-"`

	// Entity reference (the struct embedding this model)
	entity Kind `json:"-"`

	// Parent key for hierarchical data
	Parent Key `json:"-"`

	// Common fields
	ID        string    `json:"id,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
	Deleted   bool      `json:"deleted,omitempty"`

	// Version for optimistic locking
	Version int64 `json:"version,omitempty"`

	// Namespace/tenant for multi-tenancy
	Namespace_ string `json:"-"`

	// Flag for mock mode (testing)
	Mock bool `json:"-"`

	// Flag for string-based keys vs auto-generated
	UseStringKey bool `json:"-"`

	// Loaded flag to prevent duplicate deserialization
	loaded bool `json:"-"`
}

// Init initializes the model with a database and entity reference
func (m *Model) Init(database DB, entity Kind) {
	m.db = database
	m.entity = entity
}

// DB returns the database interface
func (m *Model) DB() DB {
	return m.db
}

// SetDB sets the database interface
func (m *Model) SetDB(database DB) {
	m.db = database
}

// Entity returns the entity reference
func (m *Model) Entity() Kind {
	return m.entity
}

// SetEntity sets the entity reference
func (m *Model) SetEntity(entity Kind) {
	m.entity = entity
}

// Kind returns the entity kind/table name
func (m *Model) Kind() string {
	if m.entity != nil {
		return m.entity.Kind()
	}
	return ""
}

// GetID returns the entity ID
func (m *Model) GetID() string {
	if m.ID == "" && m.key != nil {
		m.ID = m.key.Encode()
	}
	return m.ID
}

// SetID sets the entity ID
func (m *Model) SetID(id string) {
	m.ID = id
	m.key = nil // Reset key so it gets regenerated
}

// Key returns the database key for this entity
func (m *Model) Key() Key {
	if m.key != nil {
		return m.key
	}

	// Generate key from ID if available
	if m.ID != "" {
		if m.UseStringKey {
			m.key = m.db.NewKey(m.Kind(), m.ID, 0, m.Parent)
		} else {
			m.key = m.db.NewKey(m.Kind(), m.ID, 0, m.Parent)
		}
		return m.key
	}

	// Allocate a new key
	keys, err := m.db.AllocateIDs(m.Kind(), m.Parent, 1)
	if err != nil {
		// Fallback to incomplete key
		m.key = m.db.NewIncompleteKey(m.Kind(), m.Parent)
	} else {
		m.key = keys[0]
	}

	// Update ID from key
	m.ID = m.key.Encode()
	return m.key
}

// SetKey sets the database key
func (m *Model) SetKey(key Key) error {
	if key == nil {
		return ErrInvalidKey
	}
	if key.Kind() != m.Kind() {
		return fmt.Errorf("db: key kind %q does not match entity kind %q", key.Kind(), m.Kind())
	}
	m.key = key
	m.ID = key.Encode()
	if key.Parent() != nil {
		m.Parent = key.Parent()
	}
	return nil
}

// SetKeyFromString sets the key from a string ID
func (m *Model) SetKeyFromString(id string) error {
	if id == "" {
		return ErrInvalidKey
	}
	m.key = m.db.NewKey(m.Kind(), id, 0, m.Parent)
	m.ID = id
	return nil
}

// Namespace returns the namespace for this entity
func (m *Model) GetNamespace() string {
	if m.key != nil {
		return m.key.Namespace()
	}
	return m.Namespace_
}

// SetNamespace sets the namespace
func (m *Model) SetNamespace(ns string) {
	m.Namespace_ = ns
}

// IsLoaded returns true if the entity has been loaded from the database
func (m *Model) IsLoaded() bool {
	return m.loaded
}

// MarkLoaded marks the entity as loaded
func (m *Model) MarkLoaded() {
	m.loaded = true
}

// IsCreated returns true if the entity has been persisted
func (m *Model) IsCreated() bool {
	return !m.CreatedAt.IsZero()
}

// Get retrieves the entity from the database
func (m *Model) Get(ctx context.Context) error {
	return m.db.Get(ctx, m.Key(), m.entity)
}

// GetByID retrieves an entity by its ID
func (m *Model) GetByID(ctx context.Context, id string) error {
	if err := m.SetKeyFromString(id); err != nil {
		return err
	}
	return m.Get(ctx)
}

// Put saves the entity to the database
func (m *Model) Put(ctx context.Context) error {
	// Set timestamps
	now := time.Now()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	m.Version++

	if m.Mock {
		return nil
	}

	key, err := m.db.Put(ctx, m.Key(), m.entity)
	if err != nil {
		return err
	}

	if m.key == nil || m.key.Incomplete() {
		m.key = key
		m.ID = key.Encode()
	}

	return nil
}

// Create creates a new entity
func (m *Model) Create(ctx context.Context) error {
	// Execute BeforeCreate hook if defined
	if hook, ok := m.entity.(BeforeCreateHook); ok {
		if err := hook.BeforeCreate(); err != nil {
			return err
		}
	}

	// Validate if supported
	if validator, ok := m.entity.(Validator); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrValidationFailed, err)
		}
	}

	if err := m.Put(ctx); err != nil {
		return err
	}

	// Execute AfterCreate hook if defined
	if hook, ok := m.entity.(AfterCreateHook); ok {
		if err := hook.AfterCreate(); err != nil {
			return err
		}
	}

	return nil
}

// Update updates an existing entity
func (m *Model) Update(ctx context.Context) error {
	// Clone current state for hooks
	var prev interface{}
	if _, ok := m.entity.(BeforeUpdateHook); ok {
		prev = m.clone()
	} else if _, ok := m.entity.(AfterUpdateHook); ok {
		prev = m.clone()
	}

	// Execute BeforeUpdate hook if defined
	if hook, ok := m.entity.(BeforeUpdateHook); ok {
		if err := hook.BeforeUpdate(prev); err != nil {
			return err
		}
	}

	// Validate if supported
	if validator, ok := m.entity.(Validator); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrValidationFailed, err)
		}
	}

	if err := m.Put(ctx); err != nil {
		return err
	}

	// Execute AfterUpdate hook if defined
	if hook, ok := m.entity.(AfterUpdateHook); ok {
		if err := hook.AfterUpdate(prev); err != nil {
			return err
		}
	}

	return nil
}

// Delete removes the entity from the database
func (m *Model) Delete(ctx context.Context) error {
	// Execute BeforeDelete hook if defined
	if hook, ok := m.entity.(BeforeDeleteHook); ok {
		if err := hook.BeforeDelete(); err != nil {
			return err
		}
	}

	if m.Mock {
		return nil
	}

	if err := m.db.Delete(ctx, m.Key()); err != nil {
		return err
	}

	// Execute AfterDelete hook if defined
	if hook, ok := m.entity.(AfterDeleteHook); ok {
		if err := hook.AfterDelete(); err != nil {
			return err
		}
	}

	return nil
}

// SoftDelete marks the entity as deleted without removing it
func (m *Model) SoftDelete(ctx context.Context) error {
	m.Deleted = true
	return m.Update(ctx)
}

// Exists checks if the entity exists in the database
func (m *Model) Exists(ctx context.Context) (bool, error) {
	err := m.db.Get(ctx, m.Key(), m.entity)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNoSuchEntity) {
		return false, nil
	}
	return false, err
}

// RunInTransaction executes a function within a transaction
func (m *Model) RunInTransaction(ctx context.Context, fn func(tx Transaction) error) error {
	return m.db.RunInTransaction(ctx, fn, nil)
}

// Query returns a new query for this entity's kind
func (m *Model) Query() Query {
	return m.db.Query(m.Kind())
}

// clone creates a shallow copy of the entity
func (m *Model) clone() interface{} {
	// Use JSON marshaling for a simple deep copy
	data, err := json.Marshal(m.entity)
	if err != nil {
		return nil
	}

	// Create a new instance of the same type
	// This is a simplified clone - in production you'd use reflection
	return data
}

// JSON returns the JSON representation of the entity
func (m *Model) JSON() ([]byte, error) {
	return json.Marshal(m.entity)
}

// JSONString returns the JSON string representation
func (m *Model) JSONString() string {
	data, _ := m.JSON()
	return string(data)
}

// MustGet retrieves the entity or panics
func (m *Model) MustGet(ctx context.Context) {
	if err := m.Get(ctx); err != nil {
		panic(err)
	}
}

// MustGetByID retrieves by ID or panics
func (m *Model) MustGetByID(ctx context.Context, id string) {
	if err := m.GetByID(ctx, id); err != nil {
		panic(err)
	}
}

// MustPut saves the entity or panics
func (m *Model) MustPut(ctx context.Context) {
	if err := m.Put(ctx); err != nil {
		panic(err)
	}
}

// MustCreate creates the entity or panics
func (m *Model) MustCreate(ctx context.Context) {
	if err := m.Create(ctx); err != nil {
		panic(err)
	}
}

// MustUpdate updates the entity or panics
func (m *Model) MustUpdate(ctx context.Context) {
	if err := m.Update(ctx); err != nil {
		panic(err)
	}
}

// MustDelete deletes the entity or panics
func (m *Model) MustDelete(ctx context.Context) {
	if err := m.Delete(ctx); err != nil {
		panic(err)
	}
}
