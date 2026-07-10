// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package cloud

import (
	"context"
	"testing"
)

// TestInstallTelemetry_DispatchesToRegisteredInstaller proves the seam Serve relies
// on: Serve calls installTelemetry ONCE before MountAll (on both the full enable=nil
// and single-service enable=[svc] paths — the call is unconditional, before the
// enable-gated mount loop), and installTelemetry dispatches to the installer
// clients/o11y registered. If this dispatch broke, every entrypoint would be
// telemetry-dark again.
func TestInstallTelemetry_DispatchesToRegisteredInstaller(t *testing.T) {
	prev := telemetryInstaller
	t.Cleanup(func() { telemetryInstaller = prev })

	called := false
	gotName := ""
	shutdownCalled := false
	RegisterTelemetryInstaller(func(_ context.Context, name string) func(context.Context) {
		called = true
		gotName = name
		return func(context.Context) { shutdownCalled = true }
	})

	shutdown := installTelemetry(context.Background(), "hanzo-cloud")
	if !called {
		t.Fatal("installTelemetry did not dispatch to the registered installer — Serve would be telemetry-dark")
	}
	if gotName != "hanzo-cloud" {
		t.Errorf("installer serviceName = %q, want hanzo-cloud", gotName)
	}
	if shutdown == nil {
		t.Fatal("installTelemetry returned a nil shutdown")
	}
	shutdown(context.Background())
	if !shutdownCalled {
		t.Error("installTelemetry did not return (and thus Serve would not run) the installer's shutdown")
	}
}

// TestInstallTelemetry_NoopWhenUnregistered confirms the safe default: with no
// installer registered (clients/o11y not linked), installTelemetry returns a non-nil
// no-op shutdown so Serve defers it unconditionally without a nil deref.
func TestInstallTelemetry_NoopWhenUnregistered(t *testing.T) {
	prev := telemetryInstaller
	t.Cleanup(func() { telemetryInstaller = prev })
	telemetryInstaller = nil

	shutdown := installTelemetry(context.Background(), "hanzo-cloud")
	if shutdown == nil {
		t.Fatal("installTelemetry must return a non-nil no-op shutdown when clients/o11y is not linked")
	}
	shutdown(context.Background()) // must not panic
}

// TestInstallTelemetry_NoopWhenInstallerReturnsNil guards the shutdown contract even
// if a future installer returns a nil shutdown.
func TestInstallTelemetry_NoopWhenInstallerReturnsNil(t *testing.T) {
	prev := telemetryInstaller
	t.Cleanup(func() { telemetryInstaller = prev })
	RegisterTelemetryInstaller(func(context.Context, string) func(context.Context) { return nil })

	shutdown := installTelemetry(context.Background(), "x")
	if shutdown == nil {
		t.Fatal("installTelemetry must never return nil even if the installer does")
	}
	shutdown(context.Background()) // must not panic
}
