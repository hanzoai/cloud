// Package order provides the Order model with support for the new db.DB interface.
// This file contains the modernized Order implementation that works with SQLite
// and PostgreSQL backends through the unified db.DB abstraction.
package order

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/models/discount"
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// OrderDB is the modernized Order model using the new db.DB interface.
// It provides full compatibility with SQLite and PostgreSQL backends.
type OrderDB struct {
	db.Model

	Number int `json:"number,omitempty"`

	// Store this was sold from (if any)
	StoreID string `json:"storeId,omitempty"`

	// Associated campaign
	CampaignID string `json:"campaignId,omitempty"`

	// Associated user or buyer
	UserID string `json:"userId,omitempty"`
	Email  string `json:"email,omitempty"`

	// Associated cart
	CartID string `json:"cartId,omitempty"`

	// Associated referrer
	ReferrerID string `json:"referrerId,omitempty"`
	ReferralID string `json:"referralId,omitempty"`

	// Status
	Status        Status         `json:"status"`
	PaymentStatus payment.Status `json:"paymentStatus"`

	// Whether this was a preorder
	Preorder bool `json:"preorder"`

	// Order is unconfirmed if user has not declared variant options
	Unconfirmed bool `json:"unconfirmed,omitempty"`

	// 3-letter ISO currency code (lowercase)
	Currency currency.Type `json:"currency"`

	// Payment processor type - paypal, stripe, etc
	Type accounts.Type `json:"type,omitempty"`

	// Payment Method
	PaymentMethodID string                      `json:"paymentMethodId,omitempty"`
	PaymentMethod   paymentmethod.PaymentMethod `json:"-"`

	// Payment mode
	Mode Mode `json:"mode,omitempty"`

	// Shipping method
	ShippingMethod string `json:"shippingMethod,omitempty"`

	// Sum of the line item amounts (cents)
	LineTotal currency.Cents `json:"lineTotal"`

	// Sum of line totals less discount (cents)
	TaxableLineTotal currency.Cents `json:"taxableLineTotal"`

	// Discount amount applied (cents)
	Discount currency.Cents `json:"discount"`

	// Sum of line totals less discount (cents)
	Subtotal currency.Cents `json:"subtotal"`

	// Shipping cost applied (cents)
	Shipping currency.Cents `json:"shipping"`

	// Sales tax applied (cents)
	Tax currency.Cents `json:"tax"`

	// Price adjustments (cents)
	Adjustment currency.Cents `json:"-"`

	// Total = subtotal + shipping + taxes + adjustments (cents)
	Total currency.Cents `json:"total"`

	// Amount owed to the seller (cents)
	Balance currency.Cents `json:"balance,omitempty"`

	// Gross amount paid (cents)
	Paid currency.Cents `json:"paid,omitempty"`

	// Amount refunded (cents)
	Refunded currency.Cents `json:"refunded"`

	// Address information
	Company         string  `json:"company,omitempty"`
	BillingAddress  Address `json:"billingAddress"`
	ShippingAddress Address `json:"shippingAddress"`

	// Line items - stored as JSON
	Items []lineitem.LineItem `json:"items"`

	// Adjustments
	Adjustments []Adjustment `json:"adjustments,omitempty"`

	// Discounts - stored as JSON
	Discounts []*discount.Discount `json:"discounts,omitempty"`

	// Coupons - stored as JSON
	Coupons     []coupon.Coupon `json:"coupons,omitempty"`
	CouponCodes []string        `json:"couponCodes,omitempty"`

	// Payment references
	PaymentIDs []string           `json:"payments"`
	Payments   []*payment.Payment `json:"-"`

	// Date order was cancelled
	CancelledAt time.Time `json:"cancelledAt,omitempty"`

	// Fulfillment information
	Fulfillment fulfillment.Fulfillment `json:"fulfillment"`

	// Return IDs
	ReturnIDs []string `json:"returnIds"`

	// Gift options
	Gift        bool   `json:"gift,omitempty"`
	GiftMessage string `json:"giftMessage,omitempty"`
	GiftEmail   string `json:"giftEmail,omitempty"`

	// Token sales
	TokenSaleID string `json:"tokenSaleId,omitempty"`

	// Mailchimp tracking
	Mailchimp MailchimpTracking `json:"mailchimp,omitempty"`

	// Notification preferences
	Notifications NotificationPrefs `json:"notifications"`

	// Arbitrary key/value pairs
	Metadata Map `json:"metadata,omitempty"`

	// Event history
	History []Event `json:"history,omitempty"`

	// Test flag
	Test bool `json:"test"`

	// Wallet passphrase (never sent to client)
	WalletPassphrase string `json:"-"`

	// Subscriptions
	Subscriptions []Subscription `json:"subscriptions,omitempty"`

	// Form ID
	FormID string `json:"formId,omitempty"`

	// Template ID
	TemplateID string `json:"templateId,omitempty"`
}

// MailchimpTracking holds Mailchimp integration data
type MailchimpTracking struct {
	ID           string `json:"id,omitempty"`
	CampaignID   string `json:"campaignId,omitempty"`
	TrackingCode string `json:"trackingCode,omitempty"`
}

// NotificationPrefs holds notification settings
type NotificationPrefs struct {
	Email EmailNotificationPrefs `json:"email"`
	SMS   SMSNotificationPrefs   `json:"sms"`
}

// EmailNotificationPrefs holds email notification settings
type EmailNotificationPrefs struct {
	Enabled    bool   `json:"enabled"`
	TemplateID string `json:"templateId"`
	ProviderID string `json:"providerId"`
}

// SMSNotificationPrefs holds SMS notification settings
type SMSNotificationPrefs struct {
	Enabled bool `json:"enabled"`
}

// Kind returns the entity kind/table name
func (o *OrderDB) Kind() string {
	return "order"
}

// Defaults sets default values for a new order
func (o *OrderDB) Defaults() {
	o.Status = Open
	o.PaymentStatus = payment.Unpaid
	o.Fulfillment.Status = fulfillment.Pending
	o.Adjustments = make([]Adjustment, 0)
	o.History = make([]Event, 0)
	o.Items = make([]lineitem.LineItem, 0)
	o.Metadata = make(Map)
	o.Notifications.Email.Enabled = true
	o.Coupons = make([]coupon.Coupon, 0)
	o.Discounts = make([]*discount.Discount, 0)
	o.PaymentIDs = make([]string, 0)
	o.ReturnIDs = make([]string, 0)
	o.CouponCodes = make([]string, 0)
	o.Subscriptions = make([]Subscription, 0)
}

// NewOrderDB creates a new Order using the db.DB interface
func NewOrderDB(database db.DB) *OrderDB {
	o := &OrderDB{}
	o.Model.Init(database, o)
	o.Defaults()
	return o
}

// Validate validates the order before saving
func (o *OrderDB) Validate() error {
	if o.Currency == "" {
		return fmt.Errorf("currency is required")
	}
	return nil
}

// BeforeCreate is called before entity creation
func (o *OrderDB) BeforeCreate() error {
	// Generate order number from ID if not set
	if o.Number == 0 {
		o.Number = o.NumberFromID()
	}
	return nil
}

// AfterCreate is called after entity creation
func (o *OrderDB) AfterCreate() error {
	// Log order creation
	log.Info("Order created: %s", o.GetID())
	return nil
}

// NumberFromID generates an order number from the ID
func (o *OrderDB) NumberFromID() int {
	// Simple number generation - in production use hashid or similar
	id := o.GetID()
	if id == "" {
		return 0
	}
	// Use last 8 chars of ID as base for number
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	num := 0
	for _, c := range id {
		num = num*31 + int(c)
	}
	if num < 0 {
		num = -num
	}
	return num % 1000000
}

// HasDiscount returns true if a discount is applied
func (o *OrderDB) HasDiscount() bool {
	return o.Discount != 0
}

// Description returns a string description of the order items
func (o *OrderDB) Description() string {
	if o.Items == nil {
		return ""
	}

	buffer := bytes.NewBufferString("")
	for i, item := range o.Items {
		if i > 0 {
			buffer.WriteString(", ")
		}
		buffer.WriteString(item.String())
		buffer.WriteString(" x")
		buffer.WriteString(strconv.Itoa(item.Quantity))
	}
	return buffer.String()
}

// DescriptionLong returns a detailed description of order items
func (o *OrderDB) DescriptionLong() string {
	if o.Items == nil {
		return ""
	}

	buffer := bytes.NewBufferString("")
	for _, li := range o.Items {
		buffer.WriteString(fmt.Sprintf("%v (%v) x %v\n", li.DisplayName(), li.DisplayId(), li.Quantity))
	}
	return buffer.String()
}

// Display methods for formatting

// DisplaySubtotal returns formatted subtotal
func (o *OrderDB) DisplaySubtotal() string {
	return DisplayPrice(o.Currency, o.Subtotal)
}

// DisplayDiscount returns formatted discount
func (o *OrderDB) DisplayDiscount() string {
	return DisplayPrice(o.Currency, o.Discount)
}

// DisplayTax returns formatted tax
func (o *OrderDB) DisplayTax() string {
	return DisplayPrice(o.Currency, o.Tax)
}

// DisplayShipping returns formatted shipping
func (o *OrderDB) DisplayShipping() string {
	return DisplayPrice(o.Currency, o.Shipping)
}

// DisplayTotal returns formatted total
func (o *OrderDB) DisplayTotal() string {
	return DisplayPrice(o.Currency, o.Total)
}

// DisplayRefunded returns formatted refunded amount
func (o *OrderDB) DisplayRefunded() string {
	return DisplayPrice(o.Currency, o.Refunded)
}

// DisplayRemaining returns formatted remaining balance
func (o *OrderDB) DisplayRemaining() string {
	return DisplayPrice(o.Currency, o.Total-o.Refunded)
}

// DisplayCreatedAt returns human-readable creation time
func (o *OrderDB) DisplayCreatedAt() string {
	duration := time.Since(o.CreatedAt)

	if duration.Hours() > 24 {
		year, month, day := o.CreatedAt.Date()
		return fmt.Sprintf("%s %d, %d", month.String(), day, year)
	}

	return humanize.Time(o.CreatedAt)
}

// ItemsJSON returns items as JSON string
func (o *OrderDB) ItemsJSON() string {
	data, _ := json.Marshal(o.Items)
	return string(data)
}

// UpdatePaymentStatus updates the order's payment status based on payments
func (o *OrderDB) UpdatePaymentStatus(ctx context.Context) error {
	if len(o.PaymentIDs) == 0 {
		return nil
	}

	database := o.DB()
	if database == nil {
		return fmt.Errorf("database not initialized")
	}

	// Build keys for payment lookup
	keys := make([]db.Key, len(o.PaymentIDs))
	for i, id := range o.PaymentIDs {
		keys[i] = database.NewKey("payment", id, 0, nil)
	}

	// Get payments
	var payments []*payment.Payment
	if err := database.GetMulti(ctx, keys, &payments); err != nil {
		log.Error("Unable to fetch payments for order '%s': %v", o.GetID(), err)
		return err
	}

	// Sum payments and check statuses
	var badStatus payment.Status
	failed := false
	disputed := false
	refunded := false
	totalPaid := currency.Cents(0)

	for _, pay := range payments {
		if pay == nil {
			continue
		}
		switch pay.Status {
		case payment.Paid:
			totalPaid += pay.Amount
		case payment.Failed, payment.Fraudulent:
			badStatus = pay.Status
			failed = true
		case payment.Disputed:
			disputed = true
		case payment.Refunded:
			refunded = true
		}
	}

	// Update order status
	o.Paid = o.Paid + totalPaid

	if o.Paid >= o.Total {
		o.PaymentStatus = payment.Paid
		if o.Status != Completed {
			o.Status = Open
		}
	}

	if failed {
		log.Warn("Payment failed for order %s: %v", o.GetID(), badStatus)
		o.Status = Cancelled
		o.PaymentStatus = badStatus
	} else if refunded {
		o.Status = Cancelled
		o.PaymentStatus = payment.Refunded
	} else if disputed {
		o.Status = Locked
		o.PaymentStatus = payment.Disputed
	}

	return nil
}

// Tally calculates all order totals
func (o *OrderDB) Tally() {
	// Calculate line total
	lineTotal := currency.Cents(0)
	taxableLineTotal := currency.Cents(0)

	for _, item := range o.Items {
		itemTotal := item.TotalPrice()
		lineTotal += itemTotal
		if item.Taxable {
			taxableLineTotal += itemTotal
		}
	}

	o.LineTotal = lineTotal
	o.TaxableLineTotal = taxableLineTotal

	// Calculate subtotal (line total - discount)
	o.Subtotal = o.LineTotal - o.Discount
	if o.Subtotal < 0 {
		o.Subtotal = 0
	}

	// Calculate adjustment total
	adjustmentTotal := currency.Cents(0)
	for _, adj := range o.Adjustments {
		adjustmentTotal += adj.Amount
	}
	o.Adjustment = adjustmentTotal

	// Calculate total
	o.Total = o.Subtotal + o.Shipping + o.Tax + o.Adjustment

	// Update balance
	o.Balance = o.Total - o.Paid
	if o.Balance < 0 {
		o.Balance = 0
	}
}

// AddItem adds a line item to the order
func (o *OrderDB) AddItem(item lineitem.LineItem) {
	// Check if item already exists
	for i, existing := range o.Items {
		if existing.HasId(item.Id()) {
			o.Items[i].Quantity += item.Quantity
			return
		}
	}
	o.Items = append(o.Items, item)
}

// RemoveItem removes a line item from the order
func (o *OrderDB) RemoveItem(id string) bool {
	for i, item := range o.Items {
		if item.HasId(id) {
			o.Items = append(o.Items[:i], o.Items[i+1:]...)
			return true
		}
	}
	return false
}

// UpdateItemQuantity updates the quantity of a line item
func (o *OrderDB) UpdateItemQuantity(id string, quantity int) bool {
	for i, item := range o.Items {
		if item.HasId(id) {
			if quantity <= 0 {
				return o.RemoveItem(id)
			}
			o.Items[i].Quantity = quantity
			return true
		}
	}
	return false
}

// ApplyDiscount applies a discount to the order
// Note: The discount calculation is handled by the discount's Rules/Actions system
func (o *OrderDB) ApplyDiscount(d *discount.Discount) {
	if d == nil {
		return
	}

	o.Discounts = append(o.Discounts, d)

	// Discounts use a rule-based system - the actual discount calculation
	// should be done through the discount's Rules and Actions.
	// This method just adds the discount to the order for tracking.
	// The actual discount amount calculation is performed by Tally() or
	// external discount calculation logic.
}

// ApplyCoupon applies a coupon to the order
func (o *OrderDB) ApplyCoupon(c coupon.Coupon) error {
	// Check if coupon already applied
	code := c.Code()
	for _, existing := range o.CouponCodes {
		if existing == code {
			return fmt.Errorf("coupon already applied")
		}
	}

	o.Coupons = append(o.Coupons, c)
	o.CouponCodes = append(o.CouponCodes, code)

	// Apply coupon discount based on type
	switch c.Type {
	case coupon.Percent:
		// Amount represents percentage (e.g., 10 = 10%)
		o.Discount += currency.Cents(math.Floor(float64(o.LineTotal) * float64(c.Amount) / 100))
	case coupon.Flat:
		// Amount is in cents
		o.Discount += currency.Cents(c.Amount)
	case coupon.FreeShipping:
		// Free shipping - set shipping to 0
		o.Shipping = 0
	}

	return nil
}

// Cancel cancels the order
func (o *OrderDB) Cancel(ctx context.Context) error {
	o.Status = Cancelled
	o.CancelledAt = time.Now()
	return o.Update(ctx)
}

// Complete marks the order as completed
func (o *OrderDB) Complete(ctx context.Context) error {
	o.Status = Completed
	return o.Update(ctx)
}

// AddEvent adds an event to the order history
func (o *OrderDB) AddEvent(eventType, desc string) {
	o.History = append(o.History, Event{
		Type: eventType,
		Desc: desc,
	})
}

// ToJSON returns the order as a JSON map
func (o *OrderDB) ToJSON() map[string]interface{} {
	data, _ := json.Marshal(o)
	var result map[string]interface{}
	json.Unmarshal(data, &result)
	return result
}

// FromJSON populates the order from a JSON map
func (o *OrderDB) FromJSON(data map[string]interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonData, o)
}

// Clone creates a copy of the order
func (o *OrderDB) Clone() *OrderDB {
	data, _ := json.Marshal(o)
	clone := &OrderDB{}
	json.Unmarshal(data, clone)
	clone.Model.Init(o.DB(), clone)
	return clone
}
