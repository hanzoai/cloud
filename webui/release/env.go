package release

import (
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/sites"
)

// Config names the site whose active release IS the console, and how often to
// look for a new one.
type Config struct {
	// Org owns the console site. Resolution is PINNED to it — never
	// unique-across-orgs — so a customer project of the same name can never be
	// served as our console.
	Org string
	// Slug is the console site's project slug.
	Slug string
	// Poll is how long a published release waits before it is live everywhere.
	Poll time.Duration
}

// The ONE resolution of the console-source configuration, for the same reason
// apps/sites owns its own (env.go there): TWO processes mount the console — the
// light router that owns the public port (cmd/cloud) and cloud.Listen in every
// per-app child — and a key spelled at each call site is a key the two can
// disagree about. It happened already with CLOUD_SITES_FIRSTPARTY_* against
// CLOUD_SITES_FIRST_PARTY_*, where which policy applied depended on which process
// a request reached.
//
//	CLOUD_CONSOLE_SITE   the console site's slug          (default hanzo-console)
//	CLOUD_CONSOLE_ORG    the org that owns it             (default: the sites
//	                     first-party org, CLOUD_SITES_FIRSTPARTY_ORG, itself
//	                     defaulting to hanzo)
//	CLOUD_CONSOLE_POLL   how often to re-read the active-release pointer
//	                     (default 30s; anything Go's time.ParseDuration accepts)
//
// The org DEFAULT is not a second literal "hanzo": the console is a first-party
// site, and which org holds our first-party sites is already stated once, in
// apps/sites. A white-label deployment that moves it moves the console with it.
func ConfigFromEnv() Config {
	return Config{
		Org:  env("CLOUD_CONSOLE_ORG", sites.ConfigFromEnv("").FirstPartyOrg),
		Slug: env("CLOUD_CONSOLE_SITE", "hanzo-console"),
		Poll: pollFromEnv(),
	}
}

// defaultPoll is how long a `hanzo sites publish` takes to reach every process.
// Fast enough that a release feels immediate, slow enough that the steady-state
// cost is one resolver call per process per 30s.
const defaultPoll = 30 * time.Second

// minPoll floors the interval. A poll tighter than this buys nothing a publish
// can perceive and turns a misconfiguration into load on the app that owns the
// pointer.
const minPoll = time.Second

func pollFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("CLOUD_CONSOLE_POLL"))
	if v == "" {
		return defaultPoll
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minPoll {
		// An unparseable or absurd interval is a typo, and the honest response to a
		// typo in a knob is the documented default — not a process that spins.
		return defaultPoll
	}
	return d
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
