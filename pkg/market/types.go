package market

import (
	"crypto/ed25519"
	"time"
)

// Provider represents a GPU resource provider in the Hanzo network.
type Provider struct {
	ID          string
	PublicKey   ed25519.PublicKey
	Location    string
	Reliability float64 // 0.0 to 1.0
	History     []bool  // Track last N tasks for reliability calculation
}

// HardwareSpecs defines the capabilities of the offered GPU.
type HardwareSpecs struct {
	Model     string
	VRAM      int    // in MB
	CudaCores int
	TFLOPS    float64
}

// Offer is a signed commitment from a provider to supply capacity.
type Offer struct {
	ProviderID  string
	Specs       HardwareSpecs
	PricePerMin float64
	LatencyMs   int
	ExpiresAt   time.Time
	Signature   []byte
}

// Demand represents a user request for GPU capacity.
type Demand struct {
	RequiredVRAM int
	MinTFLOPS    float64
	MaxPrice     float64
	Policy       SelectionPolicy
}

// SelectionPolicy allows users to weight their preferences.
type SelectionPolicy struct {
	WeightPrice       float64
	WeightLatency     float64
	WeightReliability float64
}

// DefaultPolicy provides a balanced selection criteria.
var DefaultPolicy = SelectionPolicy{
	WeightPrice:       0.4,
	WeightLatency:     0.2,
	WeightReliability: 0.4,
}
