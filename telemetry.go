// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloud

import "context"

// telemetryInstaller is the registered OTel tracer-provider bootstrap. clients/o11y
// installs it in init() (RegisterTelemetryInstaller); Serve invokes it once, before
// MountAll. The inversion keeps the o11y trace transport (clients/o11y, which imports
// THIS package) out of cloud's import graph — the SAME pattern as kmsClientFactory /
// RegisterPushBuilder in build.go. Exactly one registration.
var telemetryInstaller func(ctx context.Context, serviceName string) func(context.Context)

// RegisterTelemetryInstaller installs the tracer-provider bootstrap that wires this
// process's OTel global provider to the o11y in-process trace sink AND adopts it into
// the embedded ai module (so ai emits gen_ai spans). clients/o11y calls this from its
// init(); it is the ONE inversion point that lets cloud.Serve bootstrap telemetry with
// no cloud⇄clients/o11y import cycle. Exactly one registration.
func RegisterTelemetryInstaller(f func(ctx context.Context, serviceName string) func(context.Context)) {
	telemetryInstaller = f
}

// installTelemetry runs the registered tracer-provider bootstrap and returns its
// shutdown (ALWAYS non-nil, so callers defer it unconditionally). It is a clean no-op
// when clients/o11y is not linked. Serve calls this ONCE, BEFORE MountAll — so BOTH the
// fused cmd/cloud AND every `hanzo <svc>` single-service entrypoint install the provider
// and adopt it into ai identically, before ai mounts. This is the ONE telemetry
// bootstrap site; no entrypoint duplicates it (previously only cmd/cloud/main.go did,
// which left `hanzo <svc>` telemetry-dark).
func installTelemetry(ctx context.Context, serviceName string) func(context.Context) {
	if telemetryInstaller == nil {
		return func(context.Context) {}
	}
	if shutdown := telemetryInstaller(ctx, serviceName); shutdown != nil {
		return shutdown
	}
	return func(context.Context) {}
}
