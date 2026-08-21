// Package plugin is what each host is running, and how to change it: enable,
// disable, reload, or pin a service to a version.
//
// It is the control plane for the host's zip-native plugins, and a pin rolls
// back as readily as it rolls forward.
//
// A plugin here is a service that ships as its OWN binary and is composed in at
// run time by zip.Load, one child process per app on a private unix socket. The
// authoritative app->prefixes table is the hand-authored manifest.Apps; the
// authoritative VERSION is the artifact's SHA-256, because that is the only
// identifier that cannot drift from the bits actually serving. This package
// invents neither — it reports the first and moves the second.
//
// It used to be something else: a second plugin registry read from a
// CLOUD_PLUGINS JSON manifest, mounting wasm/goa modules and reverse proxies.
// Nothing in this repo, in universe, or in any chart ever set CLOUD_PLUGINS, so
// that lane mounted nothing in production while publishing an untyped
// GET /v1/plugins that reported the empty set. That lane is gone from here.
//
// GET /v1/tools/plugins still exists, in apps/tools, and it answers the SAME question
// from a different source: cloud.Subsystems(), the snapshot taken at boot. It
// therefore cannot see the effect of the enable/disable/reload below, which is
// why this surface — live, per host, keyed on the running artifact's digest — is
// the one to read when the answer has to be true right now.
//
// Every route below can take production down, so every one of them is
// SuperAdmin-gated and every mutation is written to the hash-chained audit
// trail BEFORE it is reported as done. A deployment with no audit store refuses
// to mutate at all, the same way a credit grant does.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/ha"
	"github.com/zap-proto/zip"
)

// ops holds what the control plane needs and nothing else: the app whose
// plugins these are, the audit chain, and the live peer set.
type ops struct {
	z       *zip.App
	audit   *audit.Recorder
	members func() []ha.Member
	self    string
	origin  string
	log     interface{ Error(string, ...any) }
}

// Mount registers the control plane. It needs the concrete *zip.App rather than
// the Router interface, because the plugin set is app state — Plugins, Reload
// and Unload are the app's, and no interface should widen to carry them.
func Mount(app cloud.Router, deps cloud.Deps) error {
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("plugin.Mount: router carries no typed-op registry")
	}
	o := &ops{
		z:       z,
		audit:   deps.Audit,
		members: liveMembers(deps),
		self:    self(),
		origin:  strings.TrimRight(os.Getenv(OriginEnv), "/"),
		log:     luxlog.Default().New("subsystem", "plugins"),
	}
	Routes(z, o)
	return nil
}

// Routes is separate from Mount so the surface can be registered against a bare
// app in a test without a full Deps.
func Routes(z *zip.App, o *ops) {
	zip.Get(z, "/v1/admin/plugins", o.list, zip.WithOperationID("adminPlugins"), zip.WithTags("plugins"))
	zip.Post(z, "/v1/admin/plugins/:name/reload", o.reload, zip.WithOperationID("adminReloadPlugin"), zip.WithTags("plugins"))
	zip.Post(z, "/v1/admin/plugins/:name/enable", o.enable, zip.WithOperationID("adminEnablePlugin"), zip.WithTags("plugins"))
	zip.Post(z, "/v1/admin/plugins/:name/disable", o.disable, zip.WithOperationID("adminDisablePlugin"), zip.WithTags("plugins"))
}

// --- the wire ------------------------------------------------------------

// Host is one host's own account of what it is running. Reported per host
// rather than merged, because during a rollout the hosts disagree BY DESIGN and
// a merged view hides exactly the state an operator is watching for.
type Host struct {
	// Host is the pod's stable id — the downward-API name for this host, the
	// membership id for a peer. Result names the same id when a change is
	// applied, so the board and a rollout join on it.
	Host string `json:"host"`
	// Addr is the host:port this row was read over, from the live membership.
	// Empty on a host-scoped read, which consults no membership at all.
	Addr string `json:"addr,omitempty"`
	// Self is true on the one row the answering host produced from its own
	// process instead of over the network. Every other row cost a hop.
	Self bool `json:"self,omitempty"`
	// Err is set when a peer could not be reached. Its plugins are then
	// unknown, which is NOT the same as none, so the list stays empty and the
	// drift below refuses to conclude anything from it.
	Err string `json:"error,omitempty"`
	// Plugins is every plugin this host has loaded, running or not, ordered by
	// name so two hosts diff cleanly. It carries nothing when Err is set, where
	// that means unknown rather than none.
	Plugins []zip.Status `json:"plugins"`
}

// Drift is one plugin's agreement across the fleet. Versions holds every
// distinct digest seen running; more than one means a rollout is incomplete or
// stuck, which is the single question this whole view exists to answer.
type Drift struct {
	// Name is the app as the manifest declares it — the same string the host
	// reports a plugin under and the same one the reload, enable and disable
	// routes take in their path.
	Name string `json:"name"`
	// Versions is every distinct artifact SHA-256 seen RUNNING, sorted. Only a
	// plugin installed from a URL carries a digest, so an empty list means
	// nothing running is digest-pinned, not that the fleet agrees.
	Versions []string `json:"versions,omitempty"`
	// Running counts the hosts serving it. A host that could not be reached is
	// counted nowhere — unknown is not down — so these three counts need not add
	// up to the fleet size.
	Running int `json:"running"`
	// Down counts the hosts where it is loaded and not serving without anyone
	// having asked for that: the child exited on its own, or a swap left nothing
	// listening.
	Down int `json:"down"`
	// Disabled counts the hosts where it was stopped deliberately. Down and
	// Disabled both answer 503 to a caller, which is exactly why they are counted
	// apart: one is an outage and one is a maintenance window.
	Disabled int `json:"disabled"`
	// Drifted is true when more than one digest is running at once, meaning the
	// rollout is incomplete or stuck. It is the one bit this view exists for.
	Drifted bool `json:"drifted"`
}

// ListIn is the GET /v1/admin/plugins query.
type ListIn struct {
	// Scope "host" answers for THIS host only. Default "fleet" fans out to every
	// live peer. A peer answers a host-scoped read, which is what stops the
	// fan-out recursing.
	Scope string `json:"scope"`
}

// ListOut is the fleet board.
type ListOut struct {
	// Status is "ok". This read refuses with an HTTP status rather than an error
	// envelope, so it takes no other value — an unreachable peer is a row in Data
	// carrying its own error, not a failed read.
	Status string `json:"status"`
	// Msg is the envelope's operator note. This read has none to make, so it is
	// always empty; the mutations are where it says something.
	Msg string `json:"msg"`
	// Data is one row per host, sorted by host id so two reads a minute apart
	// line up line for line. A host-scoped read answers with this host's row alone.
	Data []Host `json:"data"`
	// Drift folds Data into one row per plugin. Absent on a host-scoped read,
	// where one host has nothing to disagree with.
	Drift []Drift `json:"drift,omitempty"`
	// Total counts the HOSTS in Data, not the plugins on them.
	Total *int `json:"total,omitempty"`
}

// ReloadIn names an artifact to run. Exactly one of Version or URL+Sum, or
// neither to restart the artifact already loaded — which is how a wedged plugin
// is bounced without changing what it runs.
type ReloadIn struct {
	// Name is the app, from the path. It must be one the manifest declares.
	Name string `json:"name"`
	// Version is a release tag, resolved to a URL and digest through the
	// origin's binaries.json index — the same index CI publishes, so there is
	// no second table mapping versions to digests.
	Version string `json:"version"`
	// URL is the artifact directly, for an origin with no index.
	URL string `json:"url"`
	// Sum is that artifact's hex SHA-256 and is REQUIRED with URL: zip verifies
	// the download against it before the file is made executable, and refuses an
	// unverified one — so does this, one layer earlier. It is also the cache key,
	// which is why re-pinning a digest this host has already run costs no network.
	Sum string `json:"sum"`
	// Scope "host" applies here only. Default "fleet" rolls it out one host at
	// a time, halting on the first host that fails to come up.
	Scope string `json:"scope"`
}

// Result is one host's outcome for one action.
type Result struct {
	// Host is the pod the outcome belongs to, the same id the fleet board reports.
	Host string `json:"host"`
	// OK false is the halt. A fleet rollout stops at the first host that fails, so
	// at most one row carries it and the hosts after it are missing entirely
	// rather than reported as skipped.
	OK bool `json:"ok"`
	// Version is the artifact SHA-256 this host was moved to; on an ok row it is
	// what the host now runs. Empty for enable, disable and a bare restart, none
	// of which change the artifact.
	Version string `json:"version,omitempty"`
	// Msg is why the change failed here, verbatim from the host that refused it.
	// Empty when OK.
	Msg string `json:"msg,omitempty"`
}

// ActionOut is the envelope every mutation answers with. Data is per host and
// in the order applied, so a halted rollout reads as the prefix that succeeded
// followed by the one that did not.
type ActionOut struct {
	// Status is "ok" or "error". A halted rollout answers HTTP 200 with "error",
	// because part of the fleet HAS changed and a transport failure would throw
	// that away; so does a deployment refusing to mutate without an audit store,
	// where nothing changed at all. Data tells the two apart.
	Status string `json:"status"`
	// Msg names the outcome the way an operator would: "billing -> 9f2c…" for a
	// pin, "billing enabled", "billing disabled", "billing reloaded", or
	// "billing halted at cloud-1: <reason>" when a host refused.
	Msg string `json:"msg"`
	// Data is one row per host TOUCHED, in the order applied. Hosts the rollout
	// never reached are absent, so these rows are exactly the hosts whose state
	// may have moved.
	Data []Result `json:"data"`
}

// NameIn addresses one plugin by name, for the operations that take nothing else.
type NameIn struct {
	// Name is the app, from the path.
	Name string `json:"name"`
	// Scope "host" applies here only; default "fleet" applies everywhere.
	Scope string `json:"scope"`
}

// --- reads ---------------------------------------------------------------

// list reports what each host is actually running: every loaded plugin with its
// version, pid, uptime, reload and restart counts, and its measured CPU, RSS,
// thread and fd cost — read from the kernel, which is only answerable at all
// because a plugin is a process.
//
// Reading this from deployment config would answer what was INTENDED. Only the
// process knows what is TRUE, and during a rolling upgrade the two disagree on
// purpose.
//
// Example: {"scope":"fleet"}
// Response: {"status":"ok","msg":"","data":[{"host":"cloud-0","self":true,
// "plugins":[{"name":"billing","prefix":"/v1/billing","source":"url",
// "version":"9f2c…","running":true,"reloads":1,"restarts":0}]}],
// "drift":[{"name":"billing","versions":["9f2c…"],"running":1,"drifted":false}]}
func (o *ops) list(ctx context.Context, in *ListIn) (*ListOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	if in.Scope == scopeHost {
		return &ListOut{Status: core.OK, Data: []Host{o.here()}, Total: core.Total(1)}, nil
	}
	hosts := o.fleet(ctx, c)
	return &ListOut{Status: core.OK, Data: hosts, Drift: drift(hosts), Total: core.Total(len(hosts))}, nil
}

// here is this host's own account, the only one it can answer without a hop.
func (o *ops) here() Host {
	return Host{Host: o.self, Self: true, Plugins: o.z.Plugins()}
}

// drift folds the per-host truth into one row per plugin. A host that could not
// be reached contributes nothing rather than a zero, so an unreachable peer
// never reads as a plugin being down.
func drift(hosts []Host) []Drift {
	by := map[string]*Drift{}
	for _, h := range hosts {
		if h.Err != "" {
			continue
		}
		for _, p := range h.Plugins {
			d := by[p.Name]
			if d == nil {
				d = &Drift{Name: p.Name}
				by[p.Name] = d
			}
			switch {
			case p.Running:
				d.Running++
			case p.Disabled:
				d.Disabled++
			default:
				d.Down++
			}
			if p.Running && p.Version != "" && !slices.Contains(d.Versions, p.Version) {
				d.Versions = append(d.Versions, p.Version)
			}
		}
	}
	out := make([]Drift, 0, len(by))
	for _, d := range by {
		d.Drifted = len(d.Versions) > 1
		sort.Strings(d.Versions)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- mutations -----------------------------------------------------------

// reload swaps a plugin for another build without dropping a request. The
// replacement is started and proven to be LISTENING before any traffic moves to
// it, so a bad build leaves the old one serving and returns an error rather
// than a hole; the old process then drains before it is killed.
//
// With a version or url+sum it pins; naming a digest this host has run before is
// the rollback, and costs no network because the digest IS the cache key. With
// neither it restarts what is already loaded.
//
// Fleet scope applies it to one host at a time and STOPS at the first failure,
// so a build that cannot come up reaches exactly one host.
//
// Example: {"name":"billing","version":"v1.2.3"}
// Response: {"status":"ok","msg":"billing -> 9f2c…","data":[{"host":"cloud-0",
// "ok":true,"version":"9f2c…"}]}
func (o *ops) reload(ctx context.Context, in *ReloadIn) (*ActionOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.known(in.Name); err != nil {
		return nil, err
	}
	spec, err := o.artifact(ctx, in)
	if err != nil {
		// What the CALLER got wrong is an HTTP status; what the DEPLOYMENT got
		// wrong (no origin, unreachable index) is an outcome, so it stays in the
		// envelope beside the rollout results.
		if bad, ok := errors.AsType[*zip.HTTPError](err); ok {
			return nil, bad
		}
		return &ActionOut{Status: core.Err, Msg: err.Error()}, nil
	}
	return o.run(ctx, c, act{
		name: in.Name, action: "plugin.reload", scope: in.Scope, version: spec.Sum,
		body: map[string]string{"url": spec.URL, "sum": spec.Sum, "scope": scopeHost},
		here: func() error { return o.z.Reload(in.Name, spec) },
	})
}

// enable brings a stopped or disabled plugin back on the artifact it already
// has: the zero Plugin names no new artifact, so Reload reuses the loaded spec
// and clears the disabled flag. Named for what an operator means by it.
//
// Example: {"name":"billing"}
// Response: {"status":"ok","msg":"billing enabled","data":[{"host":"cloud-0","ok":true}]}
func (o *ops) enable(ctx context.Context, in *NameIn) (*ActionOut, error) {
	return o.simple(ctx, in, "plugin.enable", func() error { return o.z.Reload(in.Name, zip.Plugin{}) })
}

// disable stops the plugin. Its routes STAY REGISTERED and answer 503 — not 404.
//
// That is zip's choice and this keeps it. Removing the routes would mutate the
// route table, and re-adding them on enable would grow it without bound across
// repeated cycles, which is the invariant that makes reloads flat in memory. It
// is also the better answer: 404 says "no such API" and a client may cache it
// and stop retrying, while 503 says "this API exists and is down right now",
// which is true and retryable. Which of the two 503s this is — deliberate stop
// or crash — is what the status's disabled flag reports.
//
// Example: {"name":"billing"}
// Response: {"status":"ok","msg":"billing disabled","data":[{"host":"cloud-0","ok":true}]}
func (o *ops) disable(ctx context.Context, in *NameIn) (*ActionOut, error) {
	return o.simple(ctx, in, "plugin.disable", func() error { return o.z.Unload(in.Name) })
}

func (o *ops) simple(ctx context.Context, in *NameIn, action string, here func() error) (*ActionOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.known(in.Name); err != nil {
		return nil, err
	}
	return o.run(ctx, c, act{
		name: in.Name, action: action, scope: in.Scope,
		body: map[string]string{"scope": scopeHost},
		here: here,
	})
}

// known refuses a name the generated manifest does not declare. The manifest is
// the authority on which apps exist, so a typo is a 400 here rather than a
// confusing "no plugin named" from three layers down.
func (o *ops) known(name string) error {
	for _, a := range manifest.Apps {
		if a.Name == name {
			return nil
		}
	}
	return zip.ErrBadRequest(fmt.Sprintf("no app named %q in the manifest", name))
}

// self is this pod's stable id, from the same downward-API variables the shard
// router reads, so a host names itself the same way everywhere.
func self() string {
	for _, k := range []string{"CLOUD_POD_NAME", "POD_NAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	h, _ := os.Hostname()
	return h
}

// platform is the os/arch this host needs an artifact for. A control plane that
// pinned a version without it would happily install a darwin binary on a linux
// pod and report success until the process failed to exec.
func platform() (string, string) { return runtime.GOOS, runtime.GOARCH }
