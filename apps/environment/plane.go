package environment

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// The coding run's side of this app, on the internal plane.
//
// A run is in the agents process and this store is here, so the run asks. Both
// ops take the org from the CALLER — the tenant the run's context states, which
// is the org the run was admitted for — and never from an argument: an op that
// let a caller name the org would be a cross-tenant read over a socket that
// carries no principal.

// expose publishes the two ops a coding run asks. Use calls it.
func expose(o ops) {
	p := cloud.Plane()
	p.Post("/environment/prepare", o.prepare,
		zip.WithOperationID(client.EnvironmentPrepare),
		zip.WithSummary("What a run on a codebase carries of its environment"))
	p.Post("/environment/suggest", o.suggest,
		zip.WithOperationID(client.EnvironmentSuggest),
		zip.WithSummary("Keep a setup run's answer as its codebase's proposed environment"))
}

// tenant is the org a plane call acts for, and its store.
func (o ops) tenant(ctx context.Context) (string, *Store, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden("environment: the call acts for no org")
	}
	s, err := o.storeFor(org)
	if err != nil {
		return "", nil, err
	}
	return org, s, nil
}

// prepare answers what a sandbox run on a codebase carries of its environment:
// the install script, the start command, and each secret's name with the KMS ref
// its value lives at. A setup run is answered the secrets and none of the scripts,
// because finding the scripts is its work. A codebase with no environment answers
// empty, and the run starts bare.
//
// It answers refs and never values. The run reads each value from KMS itself, at
// the moment of use and acting for its own org, which is the one authority KMS
// opens a tenant's value to — so a ref handed out here names nothing the run
// could not already read.
func (o ops) prepare(ctx context.Context, in *client.Codebase) (*client.Prepared, error) {
	org, s, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := repoOf(in.Repo)
	if err != nil {
		return nil, err
	}
	r, ok, err := s.get(ctx, repo)
	if err != nil || !ok {
		return &client.Prepared{}, err
	}
	out := &client.Prepared{}
	if !in.Setup {
		out.Install, out.Start = r.install, r.start
	}
	for _, name := range r.secrets {
		out.Secrets = append(out.Secrets, client.Carried{Name: name, Ref: ref(org, repo, name)})
	}
	return out, nil
}

// suggest keeps a setup run's answer as its codebase's proposal, beside whatever
// is saved, and answers the proposal as kept. What does not hold is dropped rather
// than refused — a secret name the run could not export, a script over its cap —
// because the answer is an agent's reading of a repository, and the part of it
// that holds is still worth a person's review.
func (o ops) suggest(ctx context.Context, in *client.Suggestion) (*client.Suggestion, error) {
	_, s, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := repoOf(in.Repo)
	if err != nil {
		return nil, err
	}
	p := Proposal{Install: strings.TrimSpace(in.Install), Start: strings.TrimSpace(in.Start), Secrets: []string{}}
	if len(p.Install) > scriptCap {
		p.Install = ""
	}
	if len(p.Start) > scriptCap {
		p.Start = ""
	}
	for _, n := range in.Secrets {
		n = strings.TrimSpace(n)
		if Name(n) == nil && !slices.Contains(p.Secrets, n) && len(p.Secrets) < secretsCap {
			p.Secrets = append(p.Secrets, n)
		}
	}
	p.Note = strings.TrimSpace(in.Note)
	if len(p.Note) > noteCap {
		p.Note = "…" + strings.ToValidUTF8(p.Note[len(p.Note)-noteCap:], "")
	}
	session := strings.TrimSpace(in.Session)
	if _, err := s.change(ctx, repo, time.Now().Unix(), func(r *row) {
		r.proposal, r.session = &p, session
	}); err != nil {
		return nil, err
	}
	return &client.Suggestion{Repo: repo, Session: session, Install: p.Install, Start: p.Start, Secrets: p.Secrets, Note: p.Note}, nil
}
