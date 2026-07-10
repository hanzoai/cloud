package delay

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestFunc(t *testing.T) {
	var called bool
	var mu sync.Mutex
	var wg sync.WaitGroup

	testFunc := func(ctx context.Context, val string) error {
		mu.Lock()
		called = true
		mu.Unlock()
		wg.Done()
		return nil
	}

	f := Func("test-func", testFunc)
	if f.err != nil {
		t.Fatalf("Func registration failed: %v", f.err)
	}

	wg.Add(1)
	err := f.Call(context.Background(), "test-value")
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	// Wait for the goroutine to execute
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for delayed function to execute")
	}

	mu.Lock()
	if !called {
		t.Fatal("Expected function to be called")
	}
	mu.Unlock()
}

func TestFuncInvalidArgs(t *testing.T) {
	testFunc := func(ctx context.Context, val string) error {
		return nil
	}

	f := Func("test-func-invalid", testFunc)

	// Too few arguments
	_, err := f.Task()
	if err == nil {
		t.Fatal("Expected error for too few arguments")
	}

	// Too many arguments
	_, err = f.Task("one", "two")
	if err == nil {
		t.Fatal("Expected error for too many arguments")
	}
}

func TestFuncNoContext(t *testing.T) {
	// Function without context as first argument should fail
	badFunc := func(val string) error {
		return nil
	}

	f := Func("test-func-no-ctx", badFunc)
	if f.err == nil {
		t.Fatal("Expected error for function without context")
	}
}

func TestLater(t *testing.T) {
	var called bool
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(1)
	err := Later(context.Background(), 10*time.Millisecond, func(ctx context.Context) error {
		mu.Lock()
		called = true
		mu.Unlock()
		wg.Done()
		return nil
	})
	if err != nil {
		t.Fatalf("Later failed: %v", err)
	}

	// Should not be called immediately
	mu.Lock()
	if called {
		t.Fatal("Function should not be called immediately")
	}
	mu.Unlock()

	// Wait for the goroutine to execute
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Later function to execute")
	}

	mu.Lock()
	if !called {
		t.Fatal("Expected function to be called after delay")
	}
	mu.Unlock()
}

func TestNow(t *testing.T) {
	var called bool
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(1)
	err := Now(context.Background(), func(ctx context.Context) error {
		mu.Lock()
		called = true
		mu.Unlock()
		wg.Done()
		return nil
	})
	if err != nil {
		t.Fatalf("Now failed: %v", err)
	}

	// Wait for the goroutine to execute
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Now function to execute")
	}

	mu.Lock()
	if !called {
		t.Fatal("Expected function to be called")
	}
	mu.Unlock()
}

func TestQueue(t *testing.T) {
	testFunc := func(ctx context.Context) error {
		return nil
	}

	f := Func("test-func-queue", testFunc)
	f2 := f.Queue("custom-queue")

	if f2.queue != "custom-queue" {
		t.Fatalf("Expected queue to be 'custom-queue', got '%s'", f2.queue)
	}

	// Original should be unchanged
	if f.queue != "" {
		t.Fatalf("Original queue should be empty, got '%s'", f.queue)
	}
}

func TestFuncByKey(t *testing.T) {
	testFunc := func(ctx context.Context) error {
		return nil
	}

	Func("test-func-by-key", testFunc)

	f := FuncByKey("test-func-by-key")
	if f == nil {
		t.Fatal("Expected to find function by key")
	}

	// Test panic for missing key
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Expected panic for missing key")
		}
	}()

	FuncByKey("non-existent-key")
}

func TestIsContext(t *testing.T) {
	var ctx context.Context
	ctxType := reflect.TypeOf(&ctx).Elem()

	if !isContext(ctxType) {
		t.Fatal("Expected context.Context to be recognized as context")
	}
}
