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
// GET /v1/plugins still exists, in apps/tools, and it answers the SAME question
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
		log:     deps.Logger.New("subsystem", "plugins"),
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
	// Host is the pod's stable id, and Addr where it was reached. Self is true
	// for the host that answered the request.
	Host string `json:"host"`
	Addr string `json:"addr,omitempty"`
	Self bool   `json:"self,omitempty"`
	// Err is set when a peer could not be reached. Its plugins are then
	// unknown, which is NOT the same as none, so the list stays empty and the
	// drift below refuses to conclude anything from it.
	Err     string       `json:"error,omitempty"`
	Plugins []zip.Status `json:"plugins"`
}

// Drift is one plugin's agreement across the fleet. Versions holds every
// distinct digest seen running; more than one means a rollout is incomplete or
// stuck, which is the single question this whole view exists to answer.
type Drift struct {
	Name     string   `json:"name"`
	Versions []string `json:"versions,omitempty"`
	Running  int      `json:"running"`
	Down     int      `json:"down"`
	Disabled int      `json:"disabled"`
	Drifted  bool     `json:"drifted"`
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
	Status string  `json:"status"`
	Msg    string  `json:"msg"`
	Data   []Host  `json:"data"`
	Drift  []Drift `json:"drift,omitempty"`
	Total  *int    `json:"total,omitempty"`
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
	// URL is the artifact directly, for an origin with no index. Sum is its hex
	// SHA-256 and is REQUIRED with it: zip refuses an unverified download, and
	// so does this.
	URL string `json:"url"`
	Sum string `json:"sum"`
	// Scope "host" applies here only. Default "fleet" rolls it out one host at
	// a time, halting on the first host that fails to come up.
	Scope string `json:"scope"`
}

// Result is one host's outcome for one action.
type Result struct {
	Host    string `json:"host"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Msg     string `json:"msg,omitempty"`
}

// ActionOut is the envelope every mutation answers with. Data is per host and
// in the order applied, so a halted rollout reads as the prefix that succeeded
// followed by the one that did not.
type ActionOut struct {
	Status string   `json:"status"`
	Msg    string   `json:"msg"`
	Data   []Result `json:"data"`
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
		var bad *zip.HTTPError
		if errors.As(err, &bad) {
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
