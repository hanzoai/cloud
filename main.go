package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// ServiceConfig holds configuration for a service
type ServiceConfig struct {
	Path        string
	Network     string
	PriceUSDC   string
	Description string
	Handler     http.HandlerFunc
}

// DefaultServices returns the default service configuration
func DefaultServices() []ServiceConfig {
	return []ServiceConfig{
		{
			Path:        "/v1/inference",
			Network:     "base",
			PriceUSDC:   "0.005",
			Description: "AI inference endpoint",
			Handler:     inferenceHandler,
		},
		{
			Path:        "/v1/completion",
			Network:     "base",
			PriceUSDC:   "0.01",
			Description: "Text completion endpoint",
			Handler:     completionHandler,
		},
		{
			Path:        "/v1/embedding",
			Network:     "base",
			PriceUSDC:   "0.002",
			Description: "Text embedding endpoint",
			Handler:     embeddingHandler,
		},
	}
}

func main() {
	// Configuration from environment
	facilitatorURL := getEnv("FACILITATOR_URL", "https://x402.org/facilitator/")
	circleAPIKey := getEnv("CIRCLE_API_KEY", "")
	ethRPC := getEnv("ETH_RPC_URL", "")
	port := getEnv("PORT", "8080")

	// Initialize Ethereum client if RPC provided
	var ethClient *ethclient.Client
	var err error
	if ethRPC != "" {
		ethClient, err = ethclient.Dial(ethRPC)
		if err != nil {
			log.Printf("Warning: failed to connect to Ethereum RPC: %v", err)
		} else {
			defer ethClient.Close()
		}
	}

	// Initialize facilitator registry
	registry := NewFacilitatorRegistry()

	// Register Circle facilitator if API key provided
	if circleAPIKey != "" {
		circleFac := NewCircleFacilitator(facilitatorURL, circleAPIKey)
		registry.Register("circle", circleFac)
		log.Printf("Registered Circle facilitator: %s", circleFac.Name())
	}

	// Price configuration (USDC per request)
	priceConfig := map[string]string{
		"/v1/inference":  "0.005",
		"/v1/completion": "0.01",
		"/v1/embedding":  "0.002",
	}

	// Service networks
	serviceNetworks := map[string]string{
		"/v1/inference":  "base",
		"/v1/completion": "base",
		"/v1/embedding":  "base",
	}

	// Initialize payment processor
	processor := NewPaymentProcessor(registry, priceConfig, serviceNetworks)

	// Create HTTP mux
	mux := http.NewServeMux()

	// Register services with x402 middleware
	for _, svc := range DefaultServices() {
		handler := NewX402Handler(processor, svc.Path, svc.Handler)
		mux.Handle(svc.Path, handler)
		log.Printf("Registered service: %s (network: %s, price: %s USDC)", svc.Path, svc.Network, svc.PriceUSDC)
	}

	// Health check endpoint (no payment required)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "ok",
			"timestamp": time.Now().Unix(),
			"services":  len(DefaultServices()),
		})
	})

	// Manifest endpoint for x402 discovery
	mux.HandleFunc("/.well-known/x402.json", func(w http.ResponseWriter, r *http.Request) {
		manifest := map[string]interface{}{
			"version":    2,
			"accepts":    []string{"exact"},
			"facilitator": getEnv("FACILITATOR_URL", "https://x402.org/facilitator/"),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(manifest)
	})

	// Receipts endpoint for querying payment receipts
	mux.HandleFunc("/v1/receipts", func(w http.ResponseWriter, r *http.Request) {
		network := r.URL.Query().Get("network")
		if network == "" {
			http.Error(w, "network parameter required", http.StatusBadRequest)
			return
		}
		// This would query the receipt store
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"network":  network,
			"receipts": []interface{}{}, // Would query receipt store
		})
	})

	// Settlement endpoint for batch settlement
	mux.HandleFunc("/v1/settle", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Batch settlement logic would go here
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"message":   "settlement initiated",
			"timestamp": time.Now().Unix(),
		})
	})

	// Create server
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("Starting x402 facilitator server on port %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Service handlers
func inferenceHandler(w http.ResponseWriter, r *http.Request) {
	// Extract payer from context (set by x402 middleware)
	payer := r.Context().Value("x402_payer")
	tx := r.Context().Value("x402_tx")

	// Parse request body
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Process inference request (placeholder)
	prompt, _ := req["prompt"].(string)
	if prompt == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}

	// Simulate inference
	response := map[string]interface{}{
		"model":      "hanzo-inference-v1",
		"prompt":     prompt,
		"response":   "This is a simulated inference response for: " + prompt,
		"tokens":     len(prompt) / 4,
		"payer":      payer,
		"tx":         tx,
		"timestamp":  time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func completionHandler(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	prompt, _ := req["prompt"].(string)
	maxTokens, _ := req["max_tokens"].(float64)
	if prompt == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}
	if maxTokens == 0 {
		maxTokens = 100
	}

	response := map[string]interface{}{
		"model":       "hanzo-completion-v1",
		"prompt":      prompt,
		"completion":  "Completed: " + prompt,
		"max_tokens":  int(maxTokens),
		"timestamp":   time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func embeddingHandler(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	text, _ := req["text"].(string)
	if text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}

	// Simulate embedding (384 dimensions)
	dim := 384
	embedding := make([]float64, dim)
	for i := range embedding {
		embedding[i] = float64(len(text)+i) / 1000.0
	}

	response := map[string]interface{}{
		"model":      "hanzo-embedding-v1",
		"text":       text,
		"embedding":  embedding,
		"dimensions": dim,
		"timestamp":  time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}