// Token provider: GitHub App installation tokens (preferred) with PAT
// fallback. Tokens are minted on demand and cached until 60s before
// expiry. One TokenProvider serves all orgs; per-org installation
// lookups are cached so we never re-call /orgs/<org>/installation on
// the hot path.
package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v52/github"
)

type TokenProvider struct {
	cfg *Config

	mu           sync.Mutex
	appTransport *ghinstallation.AppsTransport
	installCache map[string]int64       // org -> installation_id
	tokenCache   map[string]cachedToken // org -> {token, expiry}
	pats         []string
	patIdx       int
}

type cachedToken struct {
	token  string
	expiry time.Time
}

func NewTokenProvider(cfg *Config) (*TokenProvider, error) {
	tp := &TokenProvider{
		cfg:          cfg,
		installCache: make(map[string]int64),
		tokenCache:   make(map[string]cachedToken),
	}
	if cfg.AppID != 0 && cfg.AppKeyPath != "" {
		t, err := ghinstallation.NewAppsTransportKeyFromFile(http.DefaultTransport, cfg.AppID, cfg.AppKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load app key: %w", err)
		}
		t.BaseURL = cfg.GitHubAPIBase
		tp.appTransport = t
	}
	if cfg.PATFile != "" {
		b, err := os.ReadFile(cfg.PATFile)
		if err != nil {
			return nil, fmt.Errorf("read pat_file: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			tp.pats = append(tp.pats, line)
		}
	}
	if tp.appTransport == nil && len(tp.pats) == 0 {
		return nil, errors.New("no auth credentials available (need app or PATs)")
	}
	return tp, nil
}

// Token returns a token usable as `Authorization: Bearer <token>` for
// requests scoped to the given org. App installation tokens are cached
// until 60s before expiry; PATs are returned unmodified (no expiry
// tracking — admin rotates PAT file).
func (tp *TokenProvider) Token(ctx context.Context, org string) (string, error) {
	if tp.appTransport != nil {
		t, err := tp.appToken(ctx, org)
		if err == nil {
			return t, nil
		}
		if len(tp.pats) == 0 {
			return "", fmt.Errorf("app token for org %q: %w", org, err)
		}
	}
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if len(tp.pats) == 0 {
		return "", errors.New("no PATs configured")
	}
	t := tp.pats[tp.patIdx%len(tp.pats)]
	return t, nil
}

// RotatePAT advances to the next PAT in the list. Call when a request
// returns 401/403 to try the next token.
func (tp *TokenProvider) RotatePAT() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.patIdx++
}

func (tp *TokenProvider) appToken(ctx context.Context, org string) (string, error) {
	tp.mu.Lock()
	if c, ok := tp.tokenCache[org]; ok && time.Until(c.expiry) > 60*time.Second {
		tok := c.token
		tp.mu.Unlock()
		return tok, nil
	}
	tp.mu.Unlock()

	installID, err := tp.installationID(ctx, org)
	if err != nil {
		return "", err
	}
	itr := ghinstallation.NewFromAppsTransport(tp.appTransport, installID)
	itr.BaseURL = tp.cfg.GitHubAPIBase
	tok, err := itr.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("mint installation token: %w", err)
	}
	exp := time.Now().Add(50 * time.Minute) // installation tokens TTL=1h
	tp.mu.Lock()
	tp.tokenCache[org] = cachedToken{token: tok, expiry: exp}
	tp.mu.Unlock()
	return tok, nil
}

func (tp *TokenProvider) installationID(ctx context.Context, org string) (int64, error) {
	tp.mu.Lock()
	if id, ok := tp.installCache[org]; ok {
		tp.mu.Unlock()
		return id, nil
	}
	tp.mu.Unlock()

	client := gh.NewClient(&http.Client{Transport: tp.appTransport, Timeout: 30 * time.Second})
	inst, _, err := client.Apps.FindOrganizationInstallation(ctx, org)
	if err != nil {
		return 0, fmt.Errorf("find installation for org %q: %w", org, err)
	}
	id := inst.GetID()
	tp.mu.Lock()
	tp.installCache[org] = id
	tp.mu.Unlock()
	return id, nil
}
