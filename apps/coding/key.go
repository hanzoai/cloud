package coding

// The credential the agent inside a sandbox answers the gateway with.
//
// Without one the whole product stops one step short of working: the box leases,
// the repo clones, `dev` starts, hanzo-mcp answers — and then the model call
// returns `Missing environment variable: HANZO_API_KEY`, or, with a key that
// names nobody, the gateway's 402 `a billable tenant is required (no anonymous
// usage)`. Every part was built and nothing could finish a task.
//
// IT IS OUR OWN MACHINE IDENTITY, MINTED PER RUN, and it is minted the way the
// rest of the fleet mints: client_credentials against IAM, through the same
// golang.org/x/oauth2 config clients/aihttp.go uses for inference. There is no
// second notion of "who a sandbox is" — a run authenticates as the deployment
// that started it, which is the identity the gateway already prices and meters.
//
// A STATIC KEY WAS THE OTHER OPTION AND IS WORSE IN EVERY DIRECTION. A long-lived
// sk- key would have to be stored, rotated, and handed to a box that is about to
// execute a model's output; this token expires on its own, is scoped to one
// identity, and nothing has to remember to revoke it.
//
// The token URL is not resolved here. IAMBaseURL is the fleet's split-horizon
// policy — the public issuer is Cloudflare-fronted and 403s an in-cluster
// server-side POST (edge 1006), so an in-cluster address must win — and that
// policy having exactly one home is why the agent runner works at all.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// source is built once and reused: the oauth2 client_credentials source caches a
// token until it expires, so a burst of runs mints one token rather than one
// each. Nil when the deployment carries no machine identity, which is the honest
// answer for a dev box and reads as "no credential" at the call site rather than
// as an empty string that looks like one.
var source = sync.OnceValue(func() oauth2.TokenSource {
	id := strings.TrimSpace(os.Getenv("IAM_CLIENT_ID"))
	secret := strings.TrimSpace(os.Getenv("IAM_CLIENT_SECRET"))
	base := cloud.IAMBaseURL(cloud.IAMIssuer())
	if id == "" || secret == "" || base == "" {
		return nil
	}
	cc := &clientcredentials.Config{
		ClientID:     id,
		ClientSecret: secret,
		TokenURL:     base + "/v1/iam/oauth/token",
		// hanzo.id reads the credentials from the form body, not Basic auth —
		// the same style clients/aihttp.go proved against this issuer.
		AuthStyle: oauth2.AuthStyleInParams,
	}
	return cc.TokenSource(context.Background())
})

// key returns the bearer a run authenticates with, or "" when this deployment
// holds no machine identity. An error is returned only when an identity IS
// configured and minting failed, because that is a broken deployment rather than
// an unconfigured one and the difference is what a caller needs to report.
func key(ctx context.Context) (string, error) {
	ts := source()
	if ts == nil {
		return "", nil
	}
	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("mint sandbox credential: %w", err)
	}
	return tok.AccessToken, nil
}

// keyed wraps an agent's argv so the credential arrives on STDIN and becomes an
// environment variable inside the box, and is never an argument.
//
// ARGV IS PUBLIC AND STDIN IS NOT. Every process in the pod can read another's
// command line out of /proc, the argv is what a run echoes into its own audit
// line and its session narration, and the process we are handing it to is about
// to execute a language model's output. Stdin reaches this one process and
// nothing else, the exec stream already carries it, and the value never touches
// a file, a layer, or the pod spec.
//
// `IFS= read -r` takes exactly the first line and leaves the rest of stdin for
// the agent, so this does not consume input a tool might want. `exec "$@"`
// replaces the shell, so the agent keeps PID-of-the-command and signals and exit
// codes travel unchanged — the wrapper is gone by the time the agent runs.
func keyed(argv []string) []string {
	return append([]string{
		"sh", "-c",
		`IFS= read -r HANZO_API_KEY; export HANZO_API_KEY; exec "$@"`,
		"sh",
	}, argv...)
}
