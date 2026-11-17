package middleware

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

var ErrBudgetExhausted = errors.New("budget_exhausted: concurrency limit reached")

type AdaptiveLimiter struct {
	limit      int64
	inflight   int64
	minLimit   int64
	maxLimit   int64
}

func NewAdaptiveLimiter(min, max int64) *AdaptiveLimiter {
	return &AdaptiveLimiter{limit: max, minLimit: min, maxLimit: max}
}

func (l *AdaptiveLimiter) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	if atomic.LoadInt64(&l.inflight) >= atomic.LoadInt64(&l.limit) {
		http.Error(w, ErrBudgetExhausted.Error(), http.StatusTooManyRequests)
		return
	}

	atomic.AddInt64(&l.inflight, 1)
	defer atomic.AddInt64(&l.inflight, -1)

	next(w, r)
}
