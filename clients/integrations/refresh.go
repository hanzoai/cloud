package integrations

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
)

// fresh returns a connector's live access material, refreshing first when the
// access token expires within refreshSkew (60 s) or force is set.
// SINGLE-FLIGHT per connection: the flight key is the KMS path; after
// acquiring, the row is RE-READ and a rotation another flight completed is
// ADOPTED instead of refreshed again — providers invalidate the previous
// refresh token on rotation, so a second refresh call would kill the
// credential (the refresh_token-reuse storm). Errors are token-free end to
// end; nothing secret is logged.
func fresh(ctx context.Context, s *cloud.Service[state], p *Provider, conn Connector, force bool) (Connector, []byte, error) {
	path, err := userPath(conn.Org, conn.User, conn.Provider, conn.Label)
	if err != nil {
		return Connector{}, nil, err
	}
	stale := conn.ExpiresAt != 0 && time.Until(time.Unix(conn.ExpiresAt, 0)) <= refreshSkew
	if p.Refresh == nil || (!force && !stale) {
		// Static credentials (anthropic/copilot) and still-fresh tokens degenerate
		// to a plain custody read — one endpoint shape for every provider.
		tok, err := kmsGet(s, path, p.Secrets[0])
		if err != nil || len(tok) == 0 {
			return Connector{}, nil, fmt.Errorf("stored credential unavailable")
		}
		return conn, tok, nil
	}

	unlock := s.State.flight.lock("refresh\x00" + path)
	defer unlock()

	cur, found, err := s.State.store.GetConnector(ctx, conn.Org, conn.User, conn.Provider, conn.Label)
	if err != nil {
		return Connector{}, nil, err
	}
	if !found {
		return Connector{}, nil, fmt.Errorf("connector gone")
	}
	if !force && cur.ExpiresAt > conn.ExpiresAt && time.Until(time.Unix(cur.ExpiresAt, 0)) > refreshSkew {
		// A concurrent flight already rotated while we waited for the lock: adopt
		// its result — a second provider call would spend the rotated refresh token.
		tok, err := kmsGet(s, path, p.Secrets[0])
		if err != nil || len(tok) == 0 {
			return Connector{}, nil, fmt.Errorf("stored credential unavailable")
		}
		return cur, tok, nil
	}

	ref, err := kmsGet(s, path, refreshSecret)
	if err != nil || len(ref) == 0 {
		// Fail closed — never guess or fall back to the (possibly dead) access token.
		return Connector{}, nil, fmt.Errorf("no refresh token in custody")
	}
	res, err := p.Refresh(ctx, string(ref))
	if err != nil || res == nil {
		// Provider errors are token-free by the Provider.Refresh contract.
		s.Log.Warn("token refresh failed", "provider", p.ID, "org", cur.Org, "user", cur.User, "label", cur.Label, "err", err)
		return Connector{}, nil, fmt.Errorf("token refresh failed")
	}
	// Fallback metadata BEFORE admission: a refresh response may omit identity.
	if res.ExternalID == "" {
		res.ExternalID = cur.ExternalID
	}
	if res.AccountLabel == "" {
		res.AccountLabel = cur.AccountLabel
	}
	if res.Scopes == nil {
		res.Scopes = cur.Scopes
	}
	// saveUser reseals refresh-first (deterministic) BEFORE the row update, so a
	// partial-seal crash leaves {new refresh, old access} and the next fresh()
	// self-heals by rotating from the new refresh; a crash between seal and
	// upsert leaves valid tokens + a stale row expiry = one harmless extra
	// refresh that adopts under this lock.
	next, err := saveUser(ctx, s, cur.Org, cur.User, cur.Label, p, res)
	if err != nil {
		return Connector{}, nil, err
	}
	tok := res.Tokens[p.Secrets[0]]
	if tok == "" {
		return Connector{}, nil, fmt.Errorf("provider returned no access token")
	}
	return next, []byte(tok), nil
}
