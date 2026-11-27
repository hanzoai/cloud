package environment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the only way that prose reaches the published document
// and the MCP tool list.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Environment is one codebase's environment as the API answers it.
type Environment struct {
	// Repo is the codebase: a repository's name in the caller's org.
	Repo string `json:"repo"`
	// Install is the shell a run executes in its fresh checkout before the agent
	// starts, from the repository root. Empty installs nothing.
	Install string `json:"install"`
	// Start is the one command a run leaves running in the background once the
	// install has finished — a dev server, a database. Empty starts nothing.
	Start string `json:"start"`
	// Secrets are the names set on this codebase, each exported to the run's
	// commands under that name. Values are never answered.
	Secrets []string `json:"secrets"`
	// State is `none` when nothing is saved or proposed, `proposed` when a setup
	// run's answer is waiting to be reviewed, and `ready` otherwise.
	State string `json:"state"`
	// Session is the setup run that produced the proposal, when there is one.
	Session string `json:"session,omitempty"`
	// Proposal is what the last setup run found, until it is saved or replaced.
	Proposal *Proposal `json:"proposal"`
	// UpdatedAt is when this environment last changed, RFC 3339. Absent for none.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// Proposal is the environment a setup run's agent would save.
type Proposal struct {
	// Install is the install script the agent checked.
	Install string `json:"install"`
	// Start is the start command the agent checked, or empty.
	Start string `json:"start"`
	// Secrets are the environment variables the agent found the codebase reads.
	// Some may not be set yet; setting them is a person's act, not the agent's.
	Secrets []string `json:"secrets"`
	// Note is what the agent found and checked, in its own words.
	Note string `json:"note"`
}

const (
	stateNone     = "none"
	stateProposed = "proposed"
	stateReady    = "ready"

	// scriptCap bounds a script. It is a program a run executes, not a file store.
	scriptCap = 16 << 10
	// valueCap bounds one secret's value: a key, a URL, a certificate chain.
	valueCap = 64 << 10
	// secretsCap bounds how many secrets one codebase carries.
	secretsCap = 64
	// noteCap bounds a proposal's note as this store keeps it.
	noteCap = 8 << 10
)

// repoRE is a repository name as the forge and the coding run spell it
// (apps/coding RepoRE): a name in the org, never owner/name.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// nameRE is an environment variable name as the run's shell exports it.
var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// reserved are names a secret may not take. HANZO_ is the run's own namespace —
// its gateway credential and its ask ticket are exported there — and the rest are
// what the shell itself runs on: a secret named PATH would decide which programs
// run.
var reserved = []string{"PATH", "HOME", "USER", "SHELL", "PWD", "HOSTNAME", "IFS", "ENV", "BASH_ENV"}

// Name refuses a secret name the run could not export, or must not.
func Name(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%q is not an environment variable name", name)
	}
	if up := strings.ToUpper(name); strings.HasPrefix(up, "HANZO_") || slices.Contains(reserved, up) {
		return fmt.Errorf("%s is reserved for the run itself", name)
	}
	return nil
}

// ref is where one secret's value lives in KMS. Injective in (org, repo, name):
// the org is the validated tenant, and neither a repo name nor a secret name can
// hold a '/'.
func ref(org, repo, name string) string {
	return "orgs/" + org + "/environment/" + repo + "/" + name
}

// state is the app's handles: the per-org stores and the KMS client that holds
// secret values.
type state struct {
	stores *cloud.OrgStore[*Store]
	kms    secret.Store
}

// mounted is the one service this process serves, for Shutdown.
var mounted *cloud.Service[state]

// Use mounts /v1/environment and publishes the plane ops a coding run asks.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("environment: nil app")
	}
	if cloud.DataDir() == "" {
		return errors.New("environment.Use: empty DataDir")
	}
	b := cloud.NewBase(deps, "environment")
	s := &cloud.Service[state]{Base: b, State: state{stores: cloud.NewOrgStore(b, "environment", openStore), kms: deps.KMS}}
	mounted = s
	o := ops{s: s}
	routes(app, o)
	expose(o)
	b.Log.Info("environment surface mounted", "prefix", "/v1/environment")
	return nil
}

// Shutdown closes every org's file.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}

// ops binds the service so each op is a named method zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, o ops) {
	g := app.Group("/v1/environment")
	g.Get("", o.listEnvironments)
	g.Get("/:repo", o.readEnvironment)
	g.Put("/:repo", o.saveEnvironment)
	g.Delete("/:repo", o.forgetEnvironment, zip.WithStatus(http.StatusNoContent))
	g.Put("/:repo/secrets/:name", o.setSecret)
	g.Delete("/:repo/secrets/:name", o.removeSecret)
}

// storeFor is the ONE way this package reaches a store: the validated org,
// through orgns.OrgNamespace.
func (o ops) storeFor(org string) (*Store, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return o.s.State.stores.For(ns)
}

// caller is the org a request acts in, admitted at need, and its store.
func (o ops) caller(ctx context.Context, need cloud.Scope) (string, *Store, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", nil, err
	}
	// Acting refused a caller with no validated org, which is all Member asks.
	if need != cloud.Member && !need.Admits(cloud.AuthorityIn(ctx)) {
		return "", nil, need.Refusal()
	}
	s, err := o.storeFor(org)
	if err != nil {
		return "", nil, err
	}
	return org, s, nil
}

func repoOf(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if !repoRE.MatchString(repo) {
		return "", zip.ErrBadRequest(fmt.Sprintf("%q is not a repository name in this organization", repo))
	}
	return repo, nil
}

func (r row) view() *Environment {
	e := &Environment{Repo: r.repo, Install: r.install, Start: r.start, Secrets: names(r.secrets), Session: r.session, Proposal: r.proposal}
	switch {
	case r.proposal != nil:
		e.State = stateProposed
	case r.install != "" || r.start != "" || len(r.secrets) > 0:
		e.State = stateReady
	default:
		e.State = stateNone
	}
	if r.updated > 0 {
		e.UpdatedAt = time.Unix(r.updated, 0).UTC().Format(time.RFC3339)
	}
	return e
}

// envList is the org's environments.
type envList struct {
	// Data is one entry per codebase that has an environment, by name.
	Data []*Environment `json:"data"`
}

// envRef names one codebase.
type envRef struct {
	// Repo is the repository's name in the caller's org — a name, never owner/name.
	Repo string `json:"repo"`
}

// envSave is the scripts to save on one codebase.
type envSave struct {
	// Repo is the repository's name in the caller's org.
	Repo string `json:"repo"`
	// Install is run in every fresh checkout before the agent starts, from the
	// repository root, and must be non-interactive. Empty installs nothing.
	Install string `json:"install"`
	// Start is left running in the background after the install. Empty starts
	// nothing.
	Start string `json:"start"`
}

// envSecret is one secret to set on a codebase.
type envSecret struct {
	// Repo is the repository's name in the caller's org.
	Repo string `json:"repo"`
	// Name is the environment variable the run exports the value under.
	Name string `json:"name"`
	// Value is sealed in KMS and never answered, logged or echoed. It is never
	// read from a query string, because a URL is logged in more places than a
	// body is.
	Value string `json:"value" url:"-"`
}

// envSecretRef names one secret on a codebase.
type envSecretRef struct {
	// Repo is the repository's name in the caller's org.
	Repo string `json:"repo"`
	// Name is the secret's environment variable name.
	Name string `json:"name"`
}

// listEnvironments lists every codebase in your org that has an environment.
//
// An environment is what a coding run does to its checkout before its agent
// starts: the install script, the start command and the secrets it exports.
// Secret values are never answered — only which names are set. A codebase with
// nothing saved and nothing proposed is not listed.
func (o ops) listEnvironments(ctx context.Context, _ *cloud.Unit) (*envList, error) {
	_, s, err := o.caller(ctx, cloud.Member)
	if err != nil {
		return nil, err
	}
	rows, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	out := &envList{Data: []*Environment{}}
	for _, r := range rows {
		out.Data = append(out.Data, r.view())
	}
	return out, nil
}

// readEnvironment reads one codebase's environment.
//
// A codebase with none answers 200 with state `none` and empty scripts, so a
// surface can always draw the editor. `proposed` means a setup run's answer is
// waiting in `proposal`; nothing in it runs until it is saved.
func (o ops) readEnvironment(ctx context.Context, in *envRef) (*Environment, error) {
	_, s, err := o.caller(ctx, cloud.Member)
	if err != nil {
		return nil, err
	}
	repo, err := repoOf(in.Repo)
	if err != nil {
		return nil, err
	}
	r, _, err := s.get(ctx, repo)
	if err != nil {
		return nil, err
	}
	return r.view(), nil
}

// saveEnvironment saves one codebase's install and start scripts.
//
// Every later sandbox run on this codebase runs the install script in its fresh
// checkout before the agent starts, and leaves the start command running in the
// background. Saving clears any pending proposal: saving is the review. Secrets
// are set one at a time on their own address and are left as they are. Org admin
// only.
func (o ops) saveEnvironment(ctx context.Context, in *envSave) (*Environment, error) {
	_, s, err := o.caller(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	repo, err := repoOf(in.Repo)
	if err != nil {
		return nil, err
	}
	install, start := strings.TrimSpace(in.Install), strings.TrimSpace(in.Start)
	if len(install) > scriptCap || len(start) > scriptCap {
		return nil, zip.ErrBadRequest(fmt.Sprintf("a script is at most %d bytes", scriptCap))
	}
	r, err := s.change(ctx, repo, time.Now().Unix(), func(r *row) {
		r.install, r.start, r.proposal, r.session = install, start, nil, ""
	})
	if err != nil {
		return nil, err
	}
	return r.view(), nil
}

// forgetEnvironment forgets one codebase's environment, and its secrets with it.
//
// Later runs on the codebase start bare. Each secret's value is removed from KMS
// first; one that will not go stops the forget, so no value outlives the record
// that names it. Org admin only.
func (o ops) forgetEnvironment(ctx context.Context, in *envRef) (*cloud.Unit, error) {
	org, s, err := o.caller(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	repo, err := repoOf(in.Repo)
	if err != nil {
		return nil, err
	}
	r, ok, err := s.get(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrNotFound("this codebase has no environment")
	}
	for _, name := range r.secrets {
		if err := o.unseal(ctx, org, repo, name); err != nil {
			return nil, err
		}
	}
	if err := s.forget(ctx, repo); err != nil {
		return nil, err
	}
	return &cloud.Unit{}, nil
}

// setSecret sets one secret on a codebase.
//
// The value is sealed in KMS and exported to every later sandbox run on this
// codebase under its name, a setup run included. It is blinded out of every
// stream the run publishes, and no route answers it back. Setting a name that is
// already set replaces its value. Org admin only.
func (o ops) setSecret(ctx context.Context, in *envSecret) (*Environment, error) {
	org, s, err := o.caller(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	repo, err := repoOf(in.Repo)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if err := Name(name); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if in.Value == "" || len(in.Value) > valueCap {
		return nil, zip.ErrBadRequest(fmt.Sprintf("a secret's value is 1 to %d bytes", valueCap))
	}
	r, _, err := s.get(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(r.secrets, name) && len(r.secrets) >= secretsCap {
		return nil, zip.ErrBadRequest(fmt.Sprintf("a codebase carries at most %d secrets", secretsCap))
	}
	if o.s.State.kms == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "secrets are not available on this deployment")
	}
	// Sealed BEFORE the name is listed, so the store never names a value KMS does
	// not hold.
	if err := o.s.State.kms.PutSecret(ctx, ref(org, repo, name), []byte(in.Value)); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the secret was not stored")
	}
	r, err = s.change(ctx, repo, time.Now().Unix(), func(r *row) {
		if !slices.Contains(r.secrets, name) {
			r.secrets = append(r.secrets, name)
			slices.Sort(r.secrets)
		}
	})
	if err != nil {
		return nil, err
	}
	return r.view(), nil
}

// removeSecret removes one secret from a codebase.
//
// The value is removed from KMS, then the name from the environment; later runs
// no longer export it. Removing a name that is not set is a 404. Org admin only.
func (o ops) removeSecret(ctx context.Context, in *envSecretRef) (*Environment, error) {
	org, s, err := o.caller(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	repo, err := repoOf(in.Repo)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	r, _, err := s.get(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(r.secrets, name) {
		return nil, zip.ErrNotFound(name + " is not set on this codebase")
	}
	if err := o.unseal(ctx, org, repo, name); err != nil {
		return nil, err
	}
	r, err = s.change(ctx, repo, time.Now().Unix(), func(r *row) {
		r.secrets = slices.DeleteFunc(r.secrets, func(n string) bool { return n == name })
	})
	if err != nil {
		return nil, err
	}
	return r.view(), nil
}

// unseal removes one value from KMS. A value already gone is the goal reached.
func (o ops) unseal(ctx context.Context, org, repo, name string) error {
	if o.s.State.kms == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "secrets are not available on this deployment")
	}
	if err := o.s.State.kms.DeleteSecret(ctx, ref(org, repo, name)); err != nil && !errors.Is(err, secret.ErrNone) {
		return zip.Errorf(http.StatusBadGateway, "%s was not removed", name)
	}
	return nil
}
