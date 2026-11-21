// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build !linux

package tetragonenforcement

// RunPayload dispatches a payload by name on non-Linux builds. Only the
// portable payloads exist here; Linux-only escape payloads report
// unsupported so the runner records them as skipped rather than faking a
// result.
func RunPayload(name string) int {
	switch name {
	case "benign":
		return benignPayload()
	case "egress":
		return egressPayload()
	case "sensitive-file":
		return sensitiveFilePayload()
	default:
		return ExitUnsupported
	}
}
