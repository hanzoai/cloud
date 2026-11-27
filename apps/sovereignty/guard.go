// SPDX-License-Identifier: Apache-2.0 OR MIT

package sovereignty

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HostBlocker enforces and audits Hanzo-host network isolation at the real
// http.RoundTripper layer. It wraps a real net/http.Transport (no network mocks),
// tracks every call to Hanzo-operated endpoints vs independently administered
// community endpoints, and deterministically blackholes api.hanzo.ai, *.hanzo.ai,
// and every registered Hanzo loopback endpoint once the Kill Switch is active.
type HostBlocker struct {
	mu               sync.RWMutex
	hanzoHosts       map[string]struct{}
	hanzoSuffixes    []string
	killSwitchActive atomic.Bool

	hanzoCallsBeforeKill    atomic.Int64
	hanzoCallsAfterKill     atomic.Int64
	hanzoBlockedAttempts    atomic.Int64
	communityCallsBeforeKil atomic.Int64
	communityCallsAfterKill atomic.Int64

	base http.RoundTripper
}

// NewHostBlocker creates a HostBlocker pre-seeded with api.hanzo.ai and .hanzo.ai.
func NewHostBlocker() *HostBlocker {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   2 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     30 * time.Second,
	}
	return &HostBlocker{
		hanzoHosts: map[string]struct{}{
			"hanzo.ai":          {},
			"api.hanzo.ai":      {},
			"gateway.hanzo.ai":  {},
			"registry.hanzo.ai": {},
			"cloud.hanzo.ai":    {},
		},
		hanzoSuffixes: []string{".hanzo.ai"},
		base:          tr,
	}
}

// normalizeHost lowercases host, separates optional port, and strips trailing
// DNS root dots from the hostname so absolute FQDNs (e.g. "api.hanzo.ai.")
// normalize identically to "api.hanzo.ai".
func normalizeHost(host string) (normHostPort string, hostname string) {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return "", ""
	}
	if parsedHost, port, err := net.SplitHostPort(h); err == nil {
		cleanHost := strings.TrimRight(parsedHost, ".")
		return net.JoinHostPort(cleanHost, port), cleanHost
	}
	cleanHost := strings.TrimRight(h, ".")
	return cleanHost, cleanHost
}

// CloseIdleConnections releases pooled HTTP connections on the underlying transport.
func (hb *HostBlocker) CloseIdleConnections() {
	if tr, ok := hb.base.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

// RegisterHanzoEndpoint registers the exact host:port of a Hanzo-operated HTTP
// server so calls to it are counted as Hanzo calls and blocked during blackout.
func (hb *HostBlocker) RegisterHanzoEndpoint(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return
	}
	normHP, _ := normalizeHost(u.Host)
	if normHP == "" {
		return
	}
	hb.mu.Lock()
	hb.hanzoHosts[normHP] = struct{}{}
	hb.mu.Unlock()
}

// ActivateKillSwitch engages total Hanzo blackout mode.
func (hb *HostBlocker) ActivateKillSwitch() {
	hb.killSwitchActive.Store(true)
}

// KillSwitchActive returns true once the Hanzo Kill Switch has been tripped.
func (hb *HostBlocker) KillSwitchActive() bool {
	return hb.killSwitchActive.Load()
}

// IsHanzoHost returns true if host matches hanzo.ai, api.hanzo.ai, *.hanzo.ai
// (including trailing-dot absolute DNS spellings), or any registered Hanzo
// loopback endpoint.
func (hb *HostBlocker) IsHanzoHost(host string) bool {
	normHP, hostname := normalizeHost(host)
	if normHP == "" {
		return false
	}
	hb.mu.RLock()
	defer hb.mu.RUnlock()

	if _, ok := hb.hanzoHosts[normHP]; ok {
		return true
	}
	if _, ok := hb.hanzoHosts[hostname]; ok {
		return true
	}
	for _, suf := range hb.hanzoSuffixes {
		if strings.HasSuffix(hostname, suf) {
			return true
		}
	}
	return false
}

// RoundTrip implements http.RoundTripper. Any call to a Hanzo-operated host
// while KillSwitchActive is true is deterministically rejected with
// ErrHanzoHostBlocked before leaving the process.
func (hb *HostBlocker) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	isHanzo := hb.IsHanzoHost(host)
	killed := hb.killSwitchActive.Load()

	if isHanzo {
		if killed {
			hb.hanzoCallsAfterKill.Add(1)
			hb.hanzoBlockedAttempts.Add(1)
			return nil, fmt.Errorf("%w: %s", ErrHanzoHostBlocked, host)
		}
		hb.hanzoCallsBeforeKill.Add(1)
	} else {
		if killed {
			hb.communityCallsAfterKill.Add(1)
		} else {
			hb.communityCallsBeforeKil.Add(1)
		}
	}
	return hb.base.RoundTrip(req)
}

// NewHTTPClient returns a real *http.Client routed through this HostBlocker.
func (hb *HostBlocker) NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Transport: hb,
		Timeout:   timeout,
	}
}

// ProbeBlackoutDirect verifies through the HostBlocker rules (without inflating
// post-kill operational workflow counters) that a target host is blocked.
func (hb *HostBlocker) ProbeBlackoutDirect(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if hb.killSwitchActive.Load() && hb.IsHanzoHost(u.Host) {
		return fmt.Errorf("%w: %s", ErrHanzoHostBlocked, u.Host)
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := hb.base.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: HTTP %d", ErrHanzoPlaneOffline, resp.StatusCode)
	}
	return nil
}

// Metrics returns an atomic snapshot of all Hanzo and community call counters.
func (hb *HostBlocker) Metrics() HostGuardMetrics {
	return HostGuardMetrics{
		KillSwitchActive:        hb.killSwitchActive.Load(),
		HanzoCallsBeforeKill:    hb.hanzoCallsBeforeKill.Load(),
		HanzoCallsAfterKill:     hb.hanzoCallsAfterKill.Load(),
		HanzoBlockedAttempts:    hb.hanzoBlockedAttempts.Load(),
		CommunityCallsBeforeKil: hb.communityCallsBeforeKil.Load(),
		CommunityCallsAfterKill: hb.communityCallsAfterKill.Load(),
	}
}
