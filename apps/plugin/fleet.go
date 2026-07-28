package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/ha"
	"github.com/zap-proto/zip"
)

// OriginEnv names the base URL plugin artifacts are published under —
// registry.hanzo.ai's S3 store, or a releases URL. A version resolves to
// <origin>/<version>/binaries.json, which is the index CI already writes, so
// the mapping from a version to a digest has ONE author.
const OriginEnv = "CLOUD_PLUGIN_ORIGIN"

const (
	scopeHost  = "host"
	peerTimout = 30 * time.Second
)

// liveMembers closes over deps so the control plane reads the same membership
// the shard router does, and re-reads it per request — a rollout that cached the
// set at boot would roll onto pods that have since gone.
func liveMembers(deps cloud.Deps) func() []ha.Member {
	return func() []ha.Member { return cloud.Members(deps) }
}

// peers returns the fleet in a deterministic order, so a staged rollout visits
// hosts in the same sequence every time and a halted one is reproducible.
//
// This host is always in the set, even when membership has already dropped it —
// a pod that is terminating leaves the live set while it is still answering, and
// a host that answered a request while claiming not to be part of the fleet
// would be both wrong and impossible to act on.
func (o *ops) peers() []ha.Member {
	m := append([]ha.Member(nil), o.members()...)
	found := false
	for _, x := range m {
		if x.ID == o.self {
			found = true
			break
		}
	}
	if !found && len(m) > 0 {
		m = append(m, ha.Member{ID: o.self})
	}
	sort.Slice(m, func(i, j int) bool { return m[i].ID < m[j].ID })
	return m
}

// fleet asks every peer for its own account and adds this host's. A peer that
// cannot be reached is reported as an error row rather than omitted: a host
// missing from a fleet view reads as "not deployed", which is a different and
// much more alarming claim than "did not answer".
func (o *ops) fleet(ctx context.Context, c *zip.Ctx) []Host {
	peers := o.peers()
	if len(peers) == 0 {
		return []Host{o.here()}
	}
	out := make([]Host, 0, len(peers))
	for _, m := range peers {
		if m.ID == o.self {
			h := o.here()
			h.Addr = m.Addr
			out = append(out, h)
			continue
		}
		h := Host{Host: m.ID, Addr: m.Addr}
		var got ListOut
		if err := o.ask(ctx, c, m, http.MethodGet, "/v1/admin/plugins?scope=host", nil, &got); err != nil {
			h.Err = err.Error()
		} else if len(got.Data) == 1 {
			h.Plugins = got.Data[0].Plugins
		}
		out = append(out, h)
	}
	return out
}

// ask calls one peer, REPLAYING the caller's own credential rather than using a
// service identity. The peer then applies the same SuperAdmin gate to the same
// principal, so a fan-out cannot become a privilege escalation: there is no
// credential in the fleet that is stronger than the operator who asked.
func (o *ops) ask(ctx context.Context, c *zip.Ctx, m ha.Member, method, path string, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, peerTimout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+m.Addr+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	creds := core.CallerCreds(c)
	if creds.Auth != "" {
		req.Header.Set("Authorization", creds.Auth)
	}
	if creds.Cookie != "" {
		req.Header.Set("Cookie", creds.Cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("peer %s: %s", m.ID, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// act is one lifecycle operation, in the two forms it has to exist in: what to
// run HERE, and what to send a peer so it runs the same thing there.
type act struct {
	name    string
	action  string // the audit action
	scope   string
	version string
	body    map[string]string // sent to a peer; always carries scope=host
	here    func() error
}

// run applies a, locally or across the fleet, and records it.
//
// Fleet rollouts are SEQUENTIAL and halt on the first failure. That is not
// timidity, it is the only way the halt means anything: zip refuses to move
// traffic onto a replacement that is not listening, so a failing build shows up
// as an error from the FIRST host — and a parallel rollout would already have
// started it everywhere else by the time that error came back. One at a time,
// the blast radius of a bad build is one host, which then keeps serving the old
// version because the swap never happened.
func (o *ops) run(ctx context.Context, c *zip.Ctx, a act) (*ActionOut, error) {
	// A lifecycle change can take production down, so it must not happen on a
	// deployment that cannot durably record it (AU-5, the same refusal a credit
	// grant makes before it moves money).
	if o.audit == nil {
		return &ActionOut{Status: core.Err, Msg: "refused: no durable audit store is configured on this deployment; a plugin lifecycle change must be recorded before it is made"}, nil
	}

	var results []Result
	if a.scope == scopeHost {
		results = []Result{o.local(a)}
	} else {
		results = o.rollout(ctx, c, a)
	}

	failed := ""
	for _, r := range results {
		if !r.OK {
			failed = r.Host + ": " + r.Msg
			break
		}
	}
	o.record(ctx, c, a, results, failed)
	if failed != "" {
		return &ActionOut{Status: core.Err, Msg: a.name + " halted at " + failed, Data: results}, nil
	}
	return &ActionOut{Status: core.OK, Msg: msg(a), Data: results}, nil
}

func msg(a act) string {
	switch {
	case a.version != "":
		return a.name + " -> " + a.version
	case a.action == "plugin.enable":
		return a.name + " enabled"
	case a.action == "plugin.disable":
		return a.name + " disabled"
	}
	return a.name + " reloaded"
}

// local applies the operation to this host.
func (o *ops) local(a act) Result {
	r := Result{Host: o.self, OK: true, Version: a.version}
	if err := a.here(); err != nil {
		r.OK, r.Msg = false, err.Error()
	}
	return r
}

// rollout walks the fleet one host at a time, stopping the moment one fails.
// Hosts never visited are simply absent from the results — the operator sees the
// prefix that changed and the host that refused, which is exactly the state the
// fleet is in.
func (o *ops) rollout(ctx context.Context, c *zip.Ctx, a act) []Result {
	peers := o.peers()
	if len(peers) == 0 {
		return []Result{o.local(a)}
	}
	out := make([]Result, 0, len(peers))
	for _, m := range peers {
		var r Result
		if m.ID == o.self {
			r = o.local(a)
		} else {
			r = Result{Host: m.ID, OK: true, Version: a.version}
			var got ActionOut
			path := "/v1/admin/plugins/" + a.name + "/" + verb(a.action)
			if err := o.ask(ctx, c, m, http.MethodPost, path, a.body, &got); err != nil {
				r.OK, r.Msg = false, err.Error()
			} else if got.Status != core.OK {
				r.OK, r.Msg = false, got.Msg
			}
		}
		out = append(out, r)
		if !r.OK {
			break // halt: a build that cannot come up reaches exactly one host
		}
	}
	return out
}

// verb maps an audit action back to its route segment. The two are deliberately
// the same word, so this stays a suffix rather than a table to keep in sync.
func verb(action string) string { return action[len("plugin."):] }

// record appends the operation to the hash-chained audit trail. The chain is
// per host, so a fleet rollout leaves the coordinator's record of the whole
// operation here and each peer's record of its own swap on that peer — which is
// what makes "who changed what is running on THIS pod" answerable on the pod.
func (o *ops) record(ctx context.Context, c *zip.Ctx, a act, results []Result, failed string) {
	org, _ := principal.Org(c)
	outcome := audit.Outcome{Result: "success", Status: http.StatusOK}
	if failed != "" {
		outcome = audit.Outcome{Result: "error", Status: http.StatusOK, Reason: failed}
	}
	after, _ := json.Marshal(map[string]any{
		"scope": scopeOf(a), "version": a.version, "results": results,
	})
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    a.action,
		Resource:  audit.Resource{Type: "plugin", ID: a.name},
		Auth:      audit.AuthContext{Method: "jwt", IsAdmin: c.IsAdmin()},
		Outcome:   outcome,
		SourceIP:  c.Header("X-Forwarded-For"),
		UserAgent: c.Header("User-Agent"),
		RequestID: c.RequestID(),
		Method:    c.Method(),
		Path:      c.Path(),
		After:     audit.Redact(after),
	}
	if _, err := o.audit.Append(ctx, rec); err != nil {
		// The request-level AuditTrail middleware fails the response closed when
		// it cannot record a /v1/admin mutation, so this detail record being lost
		// cannot leave the operation entirely unrecorded.
		o.log.Error("plugin: audit append failed", "action", a.action, "plugin", a.name, "err", err)
	}
}

func scopeOf(a act) string {
	if a.scope == scopeHost {
		return scopeHost
	}
	return "fleet"
}

// --- artifact resolution -------------------------------------------------

// index is the binaries.json CI publishes beside the artifacts.
type index struct {
	Repo     string `json:"repo"`
	Tag      string `json:"tag"`
	Binaries []struct {
		Name   string `json:"name"`
		OS     string `json:"os"`
		Arch   string `json:"arch"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"binaries"`
}

// artifact turns the request into the zip.Plugin to run. An empty result means
// "restart what is loaded", which is a legitimate and common request.
//
// The digest is never computed here and never trusted from a caller's say-so
// alone: it is handed to zip, which verifies the download against it BEFORE the
// file is made executable or given its final name, and uses it as the cache key
// so a version this host has already run needs no network at all.
func (o *ops) artifact(ctx context.Context, in *ReloadIn) (zip.Plugin, error) {
	switch {
	case in.Version != "" && in.URL != "":
		return zip.Plugin{}, zip.ErrBadRequest("give a version or a url, not both")

	case in.URL != "":
		if in.Sum == "" {
			return zip.Plugin{}, zip.ErrBadRequest("url requires sum — refusing to run an unverified download")
		}
		return zip.Plugin{URL: in.URL, Sum: in.Sum}, nil

	case in.Version != "":
		if o.origin == "" {
			return zip.Plugin{}, fmt.Errorf("cannot resolve a version: " + OriginEnv + " is unset")
		}
		return o.resolve(ctx, in.Name, in.Version)
	}
	return zip.Plugin{}, nil
}

// resolve reads the origin's index for a version and finds the artifact for
// this host's platform.
func (o *ops) resolve(ctx context.Context, name, version string) (zip.Plugin, error) {
	url := o.origin + "/" + version + "/binaries.json"
	ctx, cancel := context.WithTimeout(ctx, peerTimout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zip.Plugin{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return zip.Plugin{}, fmt.Errorf("index %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zip.Plugin{}, fmt.Errorf("index %s: %s", url, resp.Status)
	}
	var idx index
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return zip.Plugin{}, fmt.Errorf("index %s: %w", url, err)
	}
	goos, arch := platform()
	for _, b := range idx.Binaries {
		if b.Name == name && b.OS == goos && b.Arch == arch {
			if b.SHA256 == "" {
				return zip.Plugin{}, fmt.Errorf("index %s: %s has no sha256", url, name)
			}
			return zip.Plugin{URL: b.URL, Sum: b.SHA256}, nil
		}
	}
	return zip.Plugin{}, fmt.Errorf("%s %s has no %s/%s artifact in %s", name, version, goos, arch, url)
}
