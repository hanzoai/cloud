package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdaptiveLimiter(t *testing.T) {
	limiter := NewAdaptiveLimiter(1, 1)
	
	// Test first request success
	w1 := httptest.NewRecorder()
	limiter.ServeHTTP(w1, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	
	if w1.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w1.Code)
	}

	// Test second request rejection (budget_exhausted)
	w2 := httptest.NewRecorder()
	limiter.ServeHTTP(w2, nil, func(w http.ResponseWriter, r *http.Request) {})
	
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429, got %d", w2.Code)
	}
}
