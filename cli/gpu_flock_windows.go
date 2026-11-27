//go:build windows

package cli

import "context"

func acquireGPULock(_ context.Context, _ ...string) (func(), error) {
	return func() {}, nil
}

func isAnyGPULockHeld(_ ...string) bool {
	return false
}
