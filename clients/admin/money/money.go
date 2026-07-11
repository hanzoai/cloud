// Package money is the admin cockpit's one billing unit: USD cents as a typed
// value, so no field or method anywhere has to spell "Cents" again and a dollar
// amount can never be silently passed where cents are meant. The underlying type
// is int64, so arithmetic is ordinary integer math and the JSON wire is the raw
// integer — unchanged.
package money

// Cents is an amount of US-dollar cents.
type Cents int64
