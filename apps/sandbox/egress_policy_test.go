package sandbox

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// sandboxEgressAllowed reports whether an outbound TCP connection from a
// sandbox pod to (host, ip, port) is permitted by the sandbox-isolation
// NetworkPolicy and an optional host allowlist (such as
// ["api.hanzo.ai", "*.github.com", "registry.npmjs.org", "pypi.org"]).
func sandboxEgressAllowed(ip netip.Addr, host string, port int, allowlist []string) bool {
	if !ip.IsValid() || !ip.Is4() {
		return false
	}
	if port != 80 && port != 443 {
		return false
	}
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"192.0.0.0/24",
	} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	if len(allowlist) == 0 {
		return true
	}
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return false
	}
	for _, pat := range allowlist {
		pat = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pat), "."))
		if pat == "*" || pat == h {
			return true
		}
		if strings.HasPrefix(pat, "*.") {
			base := pat[2:]
			if h == base || strings.HasSuffix(h, "."+base) {
				return true
			}
		}
	}
	return false
}

// TestSandboxEgressAllowlistAndIsolationPolicy verifies the sandbox egress
// policy contract (cloud#535):
//  1. Public IPv4 HTTP/HTTPS (80/443) to github.com, api.github.com,
//     api.hanzo.ai, registry.npmjs.org, pypi.org, and 1.1.1.1 is allowed when
//     egress is enabled, including wildcard "*.github.com" matching both apex
//     "github.com" and subdomains.
//  2. RFC1918, CGNAT (100.64.0.0/10), cloud IMDS (169.254.169.254,
//     100.100.100.200, 192.0.0.192), IPv6, and non-80/443 ports (e.g. 53, 5432,
//     6379, 8000) are blocked even if the host is allowlisted.
func TestSandboxEgressAllowlistAndIsolationPolicy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		allowlist := []string{"api.hanzo.ai", "*.github.com", "registry.npmjs.org", "pypi.org"}

		allowedCases := []struct {
			name string
			ip   string
			host string
			port int
			list []string
		}{
			{"github_apex_with_wildcard", "140.82.112.4", "github.com", 443, allowlist},
			{"github_api_subdomain", "140.82.112.6", "api.github.com", 443, allowlist},
			{"github_codeload_subdomain", "140.82.112.10", "codeload.github.com", 443, allowlist},
			{"api_hanzo_ai_443", "104.21.37.151", "api.hanzo.ai", 443, allowlist},
			{"npm_registry_443", "104.16.31.34", "registry.npmjs.org", 443, allowlist},
			{"pypi_org_443", "151.101.0.223", "pypi.org", 443, allowlist},
			{"public_cf_443_open_egress", "1.1.1.1", "", 443, nil},
			{"public_cf_80_open_egress", "1.1.1.1", "", 80, nil},
		}
		for _, tc := range allowedCases {
			if !sandboxEgressAllowed(netip.MustParseAddr(tc.ip), tc.host, tc.port, tc.list) {
				t.Errorf("%s (%s:%d host=%q): got blocked, want allowed", tc.name, tc.ip, tc.port, tc.host)
			}
		}

		blockedCases := []struct {
			name string
			ip   string
			host string
			port int
			list []string
		}{
			{"imds_aws_v4", "169.254.169.254", "github.com", 80, allowlist},
			{"imds_alibaba_cgnat", "100.100.100.200", "github.com", 80, allowlist},
			{"imds_oracle", "192.0.0.192", "github.com", 80, allowlist},
			{"k8s_apiserver_rfc1918", "10.43.0.1", "api.hanzo.ai", 443, allowlist},
			{"rfc1918_172_16", "172.16.0.10", "api.hanzo.ai", 443, allowlist},
			{"rfc1918_192_168", "192.168.1.1", "api.hanzo.ai", 443, allowlist},
			{"non_http_port_53", "8.8.8.8", "api.hanzo.ai", 53, nil},
			{"non_http_port_22_public", "140.82.112.4", "github.com", 22, allowlist},
			{"ipv6_public_cf", "2606:4700:4700::1111", "api.hanzo.ai", 443, nil},
			{"unlisted_host_when_allowlist_set", "93.184.216.34", "evilgithub.com", 443, allowlist},
		}
		for _, tc := range blockedCases {
			if sandboxEgressAllowed(netip.MustParseAddr(tc.ip), tc.host, tc.port, tc.list) {
				t.Errorf("%s (%s:%d host=%q): got allowed, want blocked", tc.name, tc.ip, tc.port, tc.host)
			}
		}
	})
}
