// Package order provides the Order model and repository.
// This file contains the OrderRepository for querying orders using the db.DB interface.
package order

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
)

// OrderRepository provides methods for querying and managing orders.
// It uses the db.DB interface to support both SQLite and PostgreSQL backends.
type OrderRepository struct {
	db db.DB
}

// NewRepository creates a new OrderRepository
func NewRepository(database db.DB) *OrderRepository {
	return &OrderRepository{db: database}
}

// Create creates a new order
func (r *OrderRepository) Create(ctx context.Context, order *OrderDB) error {
	order.Model.Init(r.db, order)
	return order.Create(ctx)
}

// Update updates an existing order
func (r *OrderRepository) Update(ctx context.Context, order *OrderDB) error {
	return order.Update(ctx)
}

// Delete soft-deletes an order
func (r *OrderRepository) Delete(ctx context.Context, order *OrderDB) error {
	return order.SoftDelete(ctx)
}

// GetByID retrieves an order by its ID
func (r *OrderRepository) GetByID(ctx context.Context, id string) (*OrderDB, error) {
	order := NewOrderDB(r.db)
	if err := order.GetByID(ctx, id); err != nil {
		return nil, err
	}
	return order, nil
}

// GetByUserID retrieves all orders for a user
func (r *OrderRepository) GetByUserID(ctx context.Context, userID string, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("userId=", userID).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	// Initialize models
	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetByEmail retrieves all orders for an email
func (r *OrderRepository) GetByEmail(ctx context.Context, email string, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("email=", email).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetByStatus retrieves orders by status
func (r *OrderRepository) GetByStatus(ctx context.Context, status Status, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("status=", string(status)).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetByPaymentStatus retrieves orders by payment status
func (r *OrderRepository) GetByPaymentStatus(ctx context.Context, status payment.Status, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("paymentStatus=", string(status)).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetByStoreID retrieves orders for a store
func (r *OrderRepository) GetByStoreID(ctx context.Context, storeID string, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("storeId=", storeID).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetByCampaignID retrieves orders for a campaign
func (r *OrderRepository) GetByCampaignID(ctx context.Context, campaignID string, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("campaignId=", campaignID).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetByReferrerID retrieves orders for a referrer
func (r *OrderRepository) GetByReferrerID(ctx context.Context, referrerID string, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("referrerId=", referrerID).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetByDateRange retrieves orders within a date range
func (r *OrderRepository) GetByDateRange(ctx context.Context, start, end time.Time, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("createdAt>=", start).
		Filter("createdAt<=", end).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetPendingOrders retrieves orders that need attention
func (r *OrderRepository) GetPendingOrders(ctx context.Context, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("status=", string(Open)).
		Filter("paymentStatus=", string(payment.Paid)).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetTestOrders retrieves test orders
func (r *OrderRepository) GetTestOrders(ctx context.Context, opts *QueryOptions) ([]*OrderDB, error) {
	query := r.db.Query("order").
		Filter("test=", true).
		Order("-createdAt")

	query = applyQueryOptions(query, opts)

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// Count returns the total number of orders
func (r *OrderRepository) Count(ctx context.Context) (int, error) {
	return r.db.Query("order").Count(ctx)
}

// CountByStatus returns the count of orders with a specific status
func (r *OrderRepository) CountByStatus(ctx context.Context, status Status) (int, error) {
	return r.db.Query("order").
		Filter("status=", string(status)).
		Count(ctx)
}

// CountByUserID returns the count of orders for a user
func (r *OrderRepository) CountByUserID(ctx context.Context, userID string) (int, error) {
	return r.db.Query("order").
		Filter("userId=", userID).
		Count(ctx)
}

// Search searches orders by various criteria
func (r *OrderRepository) Search(ctx context.Context, criteria *SearchCriteria) ([]*OrderDB, error) {
	if criteria == nil {
		criteria = &SearchCriteria{}
	}

	query := r.db.Query("order")

	// Apply filters
	if criteria.UserID != "" {
		query = query.Filter("userId=", criteria.UserID)
	}
	if criteria.Email != "" {
		query = query.Filter("email=", criteria.Email)
	}
	if criteria.Status != "" {
		query = query.Filter("status=", string(criteria.Status))
	}
	if criteria.PaymentStatus != "" {
		query = query.Filter("paymentStatus=", string(criteria.PaymentStatus))
	}
	if criteria.StoreID != "" {
		query = query.Filter("storeId=", criteria.StoreID)
	}
	if criteria.CampaignID != "" {
		query = query.Filter("campaignId=", criteria.CampaignID)
	}
	if !criteria.StartDate.IsZero() {
		query = query.Filter("createdAt>=", criteria.StartDate)
	}
	if !criteria.EndDate.IsZero() {
		query = query.Filter("createdAt<=", criteria.EndDate)
	}
	if criteria.MinTotal > 0 {
		query = query.Filter("total>=", criteria.MinTotal)
	}
	if criteria.MaxTotal > 0 {
		query = query.Filter("total<=", criteria.MaxTotal)
	}
	if criteria.Test != nil {
		query = query.Filter("test=", *criteria.Test)
	}

	// Apply sorting
	if criteria.SortBy != "" {
		if criteria.SortDesc {
			query = query.OrderDesc(criteria.SortBy)
		} else {
			query = query.Order(criteria.SortBy)
		}
	} else {
		query = query.Order("-createdAt")
	}

	// Apply pagination
	if criteria.Limit > 0 {
		query = query.Limit(criteria.Limit)
	}
	if criteria.Offset > 0 {
		query = query.Offset(criteria.Offset)
	}

	var orders []*OrderDB
	_, err := query.GetAll(ctx, &orders)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		o.Model.Init(r.db, o)
	}

	return orders, nil
}

// GetMulti retrieves multiple orders by IDs
func (r *OrderRepository) GetMulti(ctx context.Context, ids []string) ([]*OrderDB, error) {
	if len(ids) == 0 {
		return []*OrderDB{}, nil
	}

	keys := make([]db.Key, len(ids))
	for i, id := range ids {
		keys[i] = r.db.NewKey("order", id, 0, nil)
	}

	var orders []*OrderDB
	if err := r.db.GetMulti(ctx, keys, &orders); err != nil {
		return nil, err
	}

	for _, o := range orders {
		if o != nil {
			o.Model.Init(r.db, o)
		}
	}

	return orders, nil
}

// UpdateMulti updates multiple orders
func (r *OrderRepository) UpdateMulti(ctx context.Context, orders []*OrderDB) error {
	if len(orders) == 0 {
		return nil
	}

	keys := make([]db.Key, len(orders))
	for i, o := range orders {
		keys[i] = o.Key()
	}

	_, err := r.db.PutMulti(ctx, keys, orders)
	return err
}

// DeleteMulti soft-deletes multiple orders
func (r *OrderRepository) DeleteMulti(ctx context.Context, ids []string) error {
	orders, err := r.GetMulti(ctx, ids)
	if err != nil {
		return err
	}

	for _, o := range orders {
		if o != nil {
			o.Deleted = true
		}
	}

	return r.UpdateMulti(ctx, orders)
}

// QueryOptions provides options for query pagination and filtering
type QueryOptions struct {
	Limit  int
	Offset int
	Cursor db.Cursor
}

// SearchCriteria provides criteria for searching orders
type SearchCriteria struct {
	UserID        string
	Email         string
	Status        Status
	PaymentStatus payment.Status
	StoreID       string
	CampaignID    string
	StartDate     time.Time
	EndDate       time.Time
	MinTotal      int
	MaxTotal      int
	Test          *bool
	SortBy        string
	SortDesc      bool
	Limit         int
	Offset        int
}

// applyQueryOptions applies pagination options to a query
func applyQueryOptions(query db.Query, opts *QueryOptions) db.Query {
	if opts == nil {
		return query
	}

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}
	if opts.Cursor != nil {
		query = query.Start(opts.Cursor)
	}

	return query
}

// OrderStats holds order statistics
type OrderStats struct {
	TotalOrders    int            `json:"totalOrders"`
	TotalRevenue   int64          `json:"totalRevenue"`
	AverageOrder   float64        `json:"averageOrder"`
	OrdersByStatus map[Status]int `json:"ordersByStatus"`
}

// GetStats calculates order statistics
func (r *OrderRepository) GetStats(ctx context.Context, opts *QueryOptions) (*OrderStats, error) {
	stats := &OrderStats{
		OrdersByStatus: make(map[Status]int),
	}

	// Get total count
	total, err := r.db.Query("order").Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count orders: %w", err)
	}
	stats.TotalOrders = total

	// Get orders to calculate revenue
	var orders []*OrderDB
	query := r.db.Query("order").Order("-createdAt")
	query = applyQueryOptions(query, opts)
	_, err = query.GetAll(ctx, &orders)
	if err != nil {
		return nil, fmt.Errorf("failed to get orders: %w", err)
	}

	var totalRevenue int64
	for _, o := range orders {
		totalRevenue += int64(o.Total)
		stats.OrdersByStatus[o.Status]++
	}

	stats.TotalRevenue = totalRevenue
	if stats.TotalOrders > 0 {
		stats.AverageOrder = float64(totalRevenue) / float64(stats.TotalOrders)
	}

	return stats, nil
}

// RunInTransaction executes operations within a transaction
func (r *OrderRepository) RunInTransaction(ctx context.Context, fn func(tx db.Transaction) error) error {
	return r.db.RunInTransaction(ctx, fn, nil)
}
