package integrations

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
)

// authClient is the shared HTTP client for provider auth-protocol calls
// (openai.go / anthropic.go / copilot.go / keyverify.go). 30 s is the per-request
// budget both the OpenAI and GitHub device protocols specify.
//
// Redirects are NEVER followed: a 3xx is surfaced as the final response, so it
// lands in the non-2xx (fail-closed) branch of every verify. This closes two
// holes on a server-side auth call, which never has a legitimate redirect: a
// provider answering bad auth with 302→200 would otherwise verify (fail-open
// amplifier), and Go forwards CUSTOM credential headers (X-Shopify-Access-Token,
// Api-Key, X-Postmark-Server-Token) across a cross-host redirect (it strips only
// Authorization/Cookie/WWW-Authenticate).
var authClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// deviceInterval is the default poll cadence (seconds) when a provider start
// carries none. begin() is the SOLE interval normalizer — providers pass wire
// values through raw (one owner; a provider-side clamp would be drift).
const deviceInterval = 5

// begin starts a device authorization for (org,user,label): opportunistic GC
// of expired grants (best-effort, GCNonces-on-connect parity), provider Start,
// then persist the Grant under a server-minted 128-bit id — the :flow handle
// the client polls with. ds.Code is secret-adjacent: it lands only in the
// cek-encrypted grants table, never a response body or a log line.
func begin(ctx context.Context, s *cloud.Service[state], org, user, label string, p *Provider) (Grant, *DeviceStart, error) {
	if _, err := s.State.store.GCGrants(ctx, time.Now().Unix()); err != nil {
		s.Log.Warn("grant gc", "err", err)
	}
	ds, err := p.Device.Start(ctx)
	if err != nil {
		return Grant{}, nil, err
	}
	if ds.Interval < 1 {
		ds.Interval = deviceInterval
	}
	id, err := genToken()
	if err != nil {
		return Grant{}, nil, fmt.Errorf("rng: %w", err)
	}
	now := time.Now().Unix()
	g := Grant{
		ID:        id,
		Org:       org,
		User:      user,
		Provider:  p.ID,
		Label:     label,
		Code:      ds.Code,
		UserCode:  ds.UserCode,
		Interval:  ds.Interval,
		CreatedAt: now,
		ExpiresAt: ds.ExpiresAt,
	}
	if err := s.State.store.PutGrant(ctx, g); err != nil {
		return Grant{}, nil, fmt.Errorf("persist grant: %w", err)
	}
	return g, ds, nil
}

// poll advances one grant. Serialized per grant id via flight (two concurrent
// polls must not double-spend the provider device code); after acquiring the
// lock the grant is RE-READ — if a concurrent poll already finalized it, the
// persisted connector is ADOPTED, never re-polled. Upstream calls are
// throttled to the grant interval (RFC-8628 server obligation: the provider
// client_ids are shared across every tenant, so unthrottled polling risks a
// provider-side ban that breaks device sign-in globally). Terminal outcomes
// delete the grant. Returned errors are transport-class (retryable; grant
// intact) or saveUser's ready-made *zip.HTTPError values.
func poll(ctx context.Context, s *cloud.Service[state], p *Provider, g Grant) (*DevicePoll, Connector, error) {
	unlock := s.State.flight.lock("poll\x00" + g.ID)
	defer unlock()

	g2, found, err := s.State.store.GetGrant(ctx, g.ID, g.Org, g.User)
	if err != nil {
		return nil, Connector{}, err
	}
	if !found {
		// A concurrent poll may have finalized this grant between the caller's
		// read and our lock: adopt its persisted connector. NAMED tradeoff: a
		// pre-existing (provider,label) connector updated in the same second the
		// grant was minted — where the grant then TTL-expired — false-adopts as
		// "connected". It answers with a real connected row; accepted over adding
		// a grant→connector linkage column.
		conn, ok, _ := s.State.store.GetConnector(ctx, g.Org, g.User, g.Provider, g.Label)
		if ok && conn.UpdatedAt >= g.CreatedAt {
			return &DevicePoll{Status: pollDone}, conn, nil
		}
		return &DevicePoll{Status: pollExpired}, Connector{}, nil
	}

	now := time.Now().Unix()
	if now-g2.LastPollAt < g2.Interval {
		// Inside the cadence window: answer pending WITHOUT an upstream call.
		return &DevicePoll{Status: pollPending, Interval: g2.Interval}, Connector{}, nil
	}
	// Touch BEFORE the upstream call so even a failed provider call holds the
	// cadence. A touch failure means the DB is broken — nothing else would work.
	if err := s.State.store.TouchGrant(ctx, g.ID, now); err != nil {
		return nil, Connector{}, err
	}

	dp, err := p.Device.Poll(ctx, g2)
	if err != nil {
		return nil, Connector{}, err // retryable: grant intact, client retries after interval
	}
	// The engine is provider-blind; a buggy provider must be an error, not a panic.
	if dp == nil || (dp.Status == pollDone && dp.Result == nil) {
		return nil, Connector{}, fmt.Errorf("provider returned no poll result")
	}

	switch dp.Status {
	case pollPending:
		return dp, Connector{}, nil
	case pollSlow:
		if err := s.State.store.SetGrantInterval(ctx, g.ID, dp.Interval); err != nil {
			s.Log.Warn("grant interval update", "provider", p.ID, "err", err)
		}
		return dp, Connector{}, nil
	case pollDone:
		conn, err := saveUser(ctx, s, g.Org, g.User, g.Label, p, dp.Result)
		if err != nil {
			// Burn the grant FIRST: the device code is spent at the provider, so a
			// retry of this grant can never succeed and must not stay pollable.
			if derr := s.State.store.DeleteGrant(ctx, g.ID); derr != nil {
				s.Log.Warn("grant delete", "provider", p.ID, "err", derr)
			}
			return nil, Connector{}, err
		}
		if derr := s.State.store.DeleteGrant(ctx, g.ID); derr != nil {
			s.Log.Warn("grant delete", "provider", p.ID, "err", derr)
		}
		return dp, conn, nil
	case pollExpired, pollDenied:
		if derr := s.State.store.DeleteGrant(ctx, g.ID); derr != nil {
			s.Log.Warn("grant delete", "provider", p.ID, "err", derr)
		}
		return dp, Connector{}, nil
	default:
		return nil, Connector{}, fmt.Errorf("provider returned unknown poll status %q", dp.Status)
	}
}
