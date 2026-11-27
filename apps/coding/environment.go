package coding

// environment.go is a run's half of its codebase's environment. The environment
// app owns the record (apps/environment): the install script, the start command,
// and the secrets set on a codebase. A sandbox run asks it over the plane what to
// carry, reads each secret's value from KMS itself, and applies the lot after the
// clone and before its harness (sandboxrunner.go prepare). A setup run's answer
// goes back the same way, as the codebase's proposal.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/client"
	environmentpeer "github.com/hanzoai/cloud/client/environment"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/secret"
)

// Prepared is what one sandbox run carries of its codebase's environment. A setup
// run carries the secrets and none of the scripts, because the scripts are what it
// is there to find.
type Prepared struct {
	Install, Start string
	// Env is each set secret's value, by its name.
	Env map[string]string
	// Missing are names the codebase lists whose value KMS no longer holds.
	Missing []string
}

// installTimeout bounds the install step in seconds, so a script that hangs holds
// the run for this long rather than for the whole lease.
const installTimeout = 20 * 60

// defaultGoPrivate lists Hanzo's private Go module paths so `go test` and
// `go build` bypass proxy.golang.org and sum.golang.org by default in coding
// sandboxes without requiring per-codebase configuration.
const defaultGoPrivate = "github.com/hanzoai/*,github.com/hanzo-inc/*,github.com/hanzo-io/*,github.com/hanzo-js/*,github.com/hanzo-studio/*,github.com/zos-apps/*,github.com/zap-proto/*,github.com/luxfi/*,github.com/zenlm/*,github.com/luxexchange/*,github.com/luxfhe/*,git.hanzo.ai/*,forge.hanzo.ai/*"

// goPrivateEnv resolves GOPRIVATE and GONOSUMDB for a sandbox run, honoring any
// explicit values in the codebase's environment and falling back to defaultGoPrivate.
func goPrivateEnv(env map[string]string) (goprivate, gonosumdb string) {
	goprivate = strings.TrimSpace(env["GOPRIVATE"])
	if goprivate == "" {
		goprivate = defaultGoPrivate
	}
	gonosumdb = strings.TrimSpace(env["GONOSUMDB"])
	if gonosumdb == "" {
		gonosumdb = goprivate
	}
	return goprivate, gonosumdb
}

// prepared resolves a codebase's environment for one sandbox run, on the run's own
// context — which acts for org, and is the only authority KMS opens a tenant's
// value to.
//
// A deployment that does not serve environments runs every codebase bare: there is
// no environment to apply. One that serves them and cannot answer fails the run,
// as does a value KMS cannot open — an agent started without the install or the
// credential its codebase was set up to have would report failures that are not
// its own. A value KMS no longer holds is named in Missing, and the run goes on
// without it.
func prepared(ctx context.Context, repo string, setup bool) (Prepared, error) {
	c, cancel := bounded(ctx)
	defer cancel()
	env, err := environmentpeer.EnvironmentPrepare(c, &client.Codebase{Repo: repo, Setup: setup})
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return Prepared{}, nil
	case err != nil:
		return Prepared{}, fmt.Errorf("the codebase's environment could not be read: %w", err)
	}
	p := Prepared{Install: env.Install, Start: env.Start}
	for _, s := range env.Secrets {
		v, err := cloud.KMSPeer{}.GetSecret(c, s.Ref)
		switch {
		case errors.Is(err, secret.ErrNone):
			p.Missing = append(p.Missing, s.Name)
		case err != nil:
			return Prepared{}, fmt.Errorf("the environment's secret %s could not be read", s.Name)
		default:
			if p.Env == nil {
				p.Env = map[string]string{}
			}
			p.Env[s.Name] = string(v)
		}
	}
	return p, nil
}

// suggested sends a setup run's answer to the environment app, which keeps what
// of it holds as the codebase's proposal and answers that.
func suggested(ctx context.Context, repo, session, answer string) (*client.Suggestion, error) {
	s, ok := suggestionOf(answer)
	if !ok {
		return nil, errors.New("the answer ended with no environment block")
	}
	s.Repo, s.Session = repo, session
	c, cancel := bounded(ctx)
	defer cancel()
	return environmentpeer.EnvironmentSuggest(c, &s)
}

// suggestionOf reads the environment a setup run's answer ends with: the LAST
// fenced block tagged `environment`, holding {"install", "start", "secrets"}. The
// prose before it is the note. An answer with no such block, or one that does not
// parse, suggests nothing — the answer still stands as the run's result.
func suggestionOf(answer string) (client.Suggestion, bool) {
	const fence = "```environment"
	i := strings.LastIndex(answer, fence)
	if i < 0 {
		return client.Suggestion{}, false
	}
	body := answer[i+len(fence):]
	j := strings.Index(body, "```")
	if j < 0 {
		return client.Suggestion{}, false
	}
	var raw struct {
		Install string   `json:"install"`
		Start   string   `json:"start"`
		Secrets []string `json:"secrets"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body[:j])), &raw); err != nil {
		return client.Suggestion{}, false
	}
	return client.Suggestion{Install: raw.Install, Start: raw.Start, Secrets: raw.Secrets, Note: strings.TrimSpace(answer[:i])}, true
}
