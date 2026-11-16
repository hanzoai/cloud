package market

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestMarketSimulation(t *testing.T) {
	rand.Seed(time.Now().UnixNano())
	router := NewEnsoRouter()
	
	// 1. Setup 100 Providers
	// - 70 Honest/Stable
	// - 20 Flaky
	// - 10 Adversarial (Low price, high failure)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("provider-%d", i)
		reliability := 0.95
		if i >= 70 && i < 90 {
			reliability = 0.60 // Flaky
		} else if i >= 90 {
			reliability = 0.10 // Adversarial
		}

		p := &Provider{
			ID:          id,
			Reliability: reliability,
			History:     make([]bool, 20),
		}
		// Initialize history
		for j := 0; j < 20; j++ {
			p.History[j] = rand.Float64() < reliability
		}
		router.Providers[id] = p
	}

	// 2. Simulation Parameters
	iterations := 1000
	fixedPriceBaseline := 0.50
	totalMarketCost := 0.0
	totalFixedCost := 0.0
	marketSuccesses := 0
	
	fmt.Printf("Starting simulation with %d iterations...\n", iterations)

	for i := 0; i < iterations; i++ {
		// Generate variable demand
		demand := Demand{
			RequiredVRAM: 8000,
			MinTFLOPS:    10.0,
			MaxPrice:     0.80,
			Policy:       DefaultPolicy,
		}

		// Generate offers for this round
		var currentOffers []Offer
		for id, p := range router.Providers {
			price := 0.20 + rand.Float64()*0.40
			if p.Reliability < 0.2 {
				price = 0.05 // Adversarial low-balling
			}

			currentOffers = append(currentOffers, Offer{
				ProviderID:  id,
				PricePerMin: price,
				LatencyMs:   rand.Intn(100) + 10,
				Specs: HardwareSpecs{
					VRAM:   16000,
					TFLOPS: 15.0,
				},
			})
		}

		// 3. Selection and Settlement
		best, err := router.SelectBestOffer(demand, currentOffers)
		if err == nil {
			provider := router.Providers[best.ProviderID]
			
			// Simulate failure based on provider reliability
			success := rand.Float64() < provider.Reliability
			provider.UpdateReliability(success)

			if success {
				totalMarketCost += best.PricePerMin
				marketSuccesses++
			}
			totalFixedCost += fixedPriceBaseline
		}
	}

	// 4. Results
	realizedCostPerSuccess := totalMarketCost / float64(marketSuccesses)
	fmt.Println("--- Simulation Results ---")
	fmt.Printf("Total Iterations: %d\n", iterations)
	fmt.Printf("Market Success Rate: %.2f%%\n", float64(marketSuccesses)/float64(iterations)*100)
	fmt.Printf("Average Market Cost per Success: $%.4f\n", realizedCostPerSuccess)
	fmt.Printf("Fixed Price Baseline: $%.4f\n", fixedPriceBaseline)
	fmt.Printf("Cost Savings: %.2f%%\n", (1.0-(realizedCostPerSuccess/fixedPriceBaseline))*100)

	if realizedCostPerSuccess >= fixedPriceBaseline {
		t.Errorf("Market failed to provide cost savings over baseline")
	}
}
