package host

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/sdk"
	"strings"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	flagpeer "github.com/hanzoai/cloud/client/flag"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// The host's crossings are the plane ops of the apps that own each registry, in their own
// request and reply types.
func init() {
	cloud.SetPeers(cloud.Peers{
		UpsertIssue: func(ctx context.Context, in cloud.IssueUpsert) (cloud.IssueUpsertResult, error) {
			out, err := cloud.Ask[client.IssueIn, client.IssueUpserted](cloud.For(ctx, in.Org), "task", client.TaskUpsert,
				&client.IssueIn{
					Project: in.Project, Key: in.ProjectKey, TeamName: in.ProjectName,
					Repo: in.Repo, ExtRef: in.ExtRef, Kind: in.Kind, Source: in.Source,
					Title: in.Title, Description: in.Description, State: in.State,
					Assignee: in.Assignee, Labels: in.Labels, Channel: in.Channel,
				})
			if err != nil {
				return cloud.IssueUpsertResult{}, err
			}
			return cloud.IssueUpsertResult{Created: out.Created, Number: out.Number, Identifier: out.Identifier, Channel: out.Channel}, nil
		},
		Sync: func(ctx context.Context, ev cloud.SyncEvent) (cloud.SyncResult, error) {
			out, err := cloud.Ask[client.SyncIn, client.SyncRan](cloud.For(ctx, ev.Org), "sync", client.SyncRun, &client.SyncIn{
				Kind: ev.Kind, Provider: ev.Provider, Locator: ev.Locator, Repo: ev.Repo,
				Ref: ev.Ref, Before: ev.Before, After: ev.After, Actor: ev.Actor,
				Token: ev.Token, Manual: ev.Manual, Hop: ev.Hop,
			})
			if err != nil {
				return cloud.SyncResult{}, err
			}
			return cloud.SyncResult{Ran: out.Ran, Skipped: out.Skipped}, nil
		},
		EnsureGitMirror: func(ctx context.Context, org, project, repo, url string, enabled bool) error {
			_, err := cloud.Ask[client.MirrorIn, client.Mirrored](cloud.For(ctx, org), "sync", client.GitMirror,
				&client.MirrorIn{Project: project, Repo: repo, URL: url, Enabled: enabled})
			return err
		},
		ImportGitRepo: func(ctx context.Context, req cloud.GitImportReq) error {
			// The org is not sent: the callee reads it from the plane identity, so an
			// argument can never widen the tenant an import lands in.
			_, err := cloud.Ask[client.ImportIn, client.Imported](ctx, "sync", client.GitImport, &client.ImportIn{
				Repo:      req.Repo,
				Project:   req.Project,
				CloneURL:  req.CloneURL,
				Token:     req.Token,
				MirrorURL: req.MirrorURL,
			})
			return err
		},
		InboundGitSync: func(ctx context.Context, req cloud.GitInboundReq) (cloud.GitSyncResult, error) {
			out, err := cloud.Ask[client.InboundIn, client.Synced](ctx, "sync", client.GitInbound, &client.InboundIn{
				Project: req.Project, Repo: req.Repo, Ref: req.Ref,
				CloneURL: req.CloneURL, Token: req.Token, Origin: req.Origin,
			})
			if err != nil {
				return cloud.GitSyncResult{}, err
			}
			return cloud.GitSyncResult{
				Applied: out.Applied, NoOp: out.NoOp, Conflict: out.Conflict,
				Detail: out.Detail, Before: out.Before, After: out.After,
			}, nil
		},
		GitRepoStatuses: func(ctx context.Context, org, project string, names []string) (map[string]cloud.GitRepoStatus, error) {
			out, err := cloud.Ask[client.StatusIn, client.Statuses](cloud.For(ctx, org), "sync", client.GitStatus,
				&client.StatusIn{Project: project, Names: names})
			if err != nil {
				return nil, err
			}
			st := make(map[string]cloud.GitRepoStatus, len(out.Rows))
			for _, r := range out.Rows {
				st[r.Name] = cloud.GitRepoStatus{
					Imported:     r.Imported,
					Conflict:     r.Conflict,
					LastSyncedAt: r.LastSyncedAt,
				}
			}
			return st, nil
		},
		ProjectOwnership: func(ctx context.Context, org, idOrSlug string) (bool, bool, error) {
			// The org is STATED rather than forwarded: this may run on a detached
			// context, and the question is "does THIS org own it".
			out, err := cloud.Ask[client.OwnerIn, client.Ownership](cloud.For(ctx, org), "project",
				client.ProjectsOwnership, &client.OwnerIn{IDOrSlug: idOrSlug})
			if err != nil {
				return false, false, err
			}
			if out == nil {
				return false, false, errors.New("cloud: projects answered nothing about project ownership")
			}
			return out.Mine, out.Other, nil
		},
		TierOf: func(ctx context.Context, subject string) (string, error) {
			out, err := commercepeer.BillingTier(ctx, &client.TierIn{Subject: subject})
			if err != nil {
				return "", err
			}
			if out == nil {
				return "", errors.New("commerce answered nothing")
			}
			return out.Tier.Name, nil
		},
		PlanOf: func(ctx context.Context, subject string) (string, error) {
			out, err := commercepeer.BillingTier(ctx, &client.TierIn{Subject: subject})
			if err != nil {
				return "", err
			}
			if out == nil {
				return "", errors.New("commerce answered nothing")
			}
			return out.Plan, nil
		},
		PlanHolderOf: func(ctx context.Context, subject string) (string, error) {
			out, err := commercepeer.BillingTier(ctx, &client.TierIn{Subject: subject})
			if err != nil {
				return "", err
			}
			if out == nil {
				return "", errors.New("commerce answered nothing")
			}
			return out.PlanHolder, nil
		},
		OwnerOf: func(ctx context.Context, person, org string) (bool, error) {
			out, err := iampeer.IAMRoles(zip.WithCaller(cloud.For(ctx, org), zip.Caller{Org: org, User: person}))
			if err != nil {
				return false, err
			}
			return out != nil && out.Owner, nil
		},
		RateOf: func(ctx context.Context, product, meter string) (int64, bool, error) {
			out, err := commercepeer.BillingRate(ctx, &client.RateIn{Product: product, Meter: meter})
			if err != nil {
				return 0, false, err
			}
			if out == nil || !out.Found {
				return 0, false, nil
			}
			return out.Nano, true, nil
		},
		ModelPriceOf: func(ctx context.Context, model string) (cloud.ModelPrice, bool, error) {
			out, err := plane.Call[client.RatesIn, client.Rates](ctx, "ai", client.AIRates, &client.RatesIn{Models: []string{model}})
			if err != nil {
				return cloud.ModelPrice{}, false, err
			}
			if out == nil {
				return cloud.ModelPrice{}, false, errors.New("cloud: ai_rates answered nothing")
			}
			for _, q := range out.Quotes {
				if strings.EqualFold(q.Model, model) {
					return cloud.ModelPrice{Input: q.Price.Input, Output: q.Price.Output, CacheRead: q.Price.CacheRead, CacheWrite: q.Price.CacheWrite}, true, nil
				}
			}
			return cloud.ModelPrice{}, false, nil
		},
		PlanCovers: func(ctx context.Context, subject, org, actor, model string, fast bool) (bool, error) {
			out, err := plane.Call[client.PlanCoversIn, client.PlanCoversOut](
				cloud.For(ctx, org), "ai", client.AIPlanCovers,
				&client.PlanCoversIn{Subject: subject, Org: org, Actor: actor, Model: model, Fast: fast},
			)
			if err != nil {
				return false, err
			}
			return out != nil && out.Covered, nil
		},
		FlagHeld: func(ctx context.Context, key string) (bool, error) {
			held, err := flagpeer.FlagsHold(ctx, &client.FlagIn{Key: key})
			if err != nil {
				return false, err
			}
			return held.On, nil
		},
		BetaOn: func(ctx context.Context) (bool, error) {
			on, err := flagpeer.FlagsOn(ctx, &client.FlagIn{Key: client.Beta})
			if err != nil {
				return false, err
			}
			return on.On, nil
		},
		KMS: func(ctx context.Context, op cloud.KMSOp, ref string, value []byte) ([]byte, error) {
			name := map[cloud.KMSOp]string{cloud.KMSGet: client.KMSGet, cloud.KMSPut: client.KMSPut, cloud.KMSDel: client.KMSDel, cloud.KMSSign: client.KMSSign}[op]
			out, err := cloud.Ask[client.SecretIn, client.Secret](ctx, "kms", name, &client.SecretIn{Ref: ref, Value: value})
			if err != nil || out == nil {
				return nil, err
			}
			return out.Value, nil
		},
	})
}
