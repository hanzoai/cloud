package cloud_test

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// TestTyped_RecoversZipApp verifies cloud.Typed adapts a strongly-typed
// func(*zip.App, Deps) into the registry MountFunc: it hands the concrete
// *zip.App straight through to the wrapped mount.
func TestTyped_RecoversZipApp(t *testing.T) {
	app := zip.New(zip.Config{})
	var got *zip.App
	mf := cloud.Typed(func(a *zip.App, _ cloud.Deps) error {
		got = a
		return nil
	})
	if err := mf(app, cloud.Deps{}); err != nil {
		t.Fatalf("Typed mount returned error: %v", err)
	}
	if got != app {
		t.Fatalf("Typed did not pass the concrete *zip.App through (got %p, want %p)", got, app)
	}
}

// TestTyped_WrongTypeFailsClosed verifies cloud.Typed fails closed with a clear
// error — never a panic — when the registry passes a value that is not a
// *zip.App. This is the single, central replacement for the per-subsystem
// assertion boilerplate.
func TestTyped_WrongTypeFailsClosed(t *testing.T) {
	called := false
	mf := cloud.Typed(func(*zip.App, cloud.Deps) error {
		called = true
		return nil
	})
	err := mf("not-a-zip-app", cloud.Deps{})
	if err == nil {
		t.Fatal("Typed must return an error on a non-*zip.App value")
	}
	if called {
		t.Fatal("Typed must NOT invoke the wrapped mount on a type mismatch")
	}
	if !strings.Contains(err.Error(), "*zip.App") {
		t.Errorf("error should name the wanted type *zip.App, got: %v", err)
	}
}
