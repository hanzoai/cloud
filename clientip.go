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

import (
	"net/http"

	"github.com/hanzoai/cloud/clientip"
	"github.com/zap-proto/zip"
)

// The caller's address lives in package clientip, and this is the door the rest of
// the fleet already comes through.
//
// IT MOVED SO THE HOST COULD REACH IT. cmd/cloud deliberately imports nothing from
// apps/, and the host-is-light gate enforces it — importing this package for one
// function pulled eight subsystems in behind it (measured: 0 leaks before, 8 after).
// The rule is right and the address is needed on both sides of the process boundary,
// so the definition sits in a leaf both may import: the host stamps with it, the
// children read through it, and neither grows a dependency on the other.
//
// These are re-exports and nothing more. The definition is single, in the leaf; this
// spelling stays because thirty call sites already say cloud.ClientIP and a rename is
// not what this change is about.

// TrustedProxiesEnv names the CIDR set this deployment treats as its own hops.
const TrustedProxiesEnv = clientip.TrustedProxiesEnv

// CountryHeader is the edge's country attestation.
const CountryHeader = clientip.CountryHeader

// ClientIP is the ONE answer to "which address is calling".
func ClientIP(c *zip.Ctx) string { return clientip.ClientIP(c) }

// ClientCountry is the edge-attested country, or empty when nothing attested one.
func ClientCountry(c *zip.Ctx) string { return clientip.ClientCountry(c) }

// TrustedProxy reports whether addr is one of this deployment's own hops.
func TrustedProxy(addr string) bool { return clientip.TrustedProxy(addr) }

// StampClientIP writes this host's answer onto a request bound for a child process.
// Register it BEFORE the children are included.
func StampClientIP(c *zip.Ctx) error { return clientip.StampClientIP(c) }

// ClientIPAcross reads that answer back inside a child.
func ClientIPAcross(r *http.Request) string { return clientip.ClientIPAcross(r) }
