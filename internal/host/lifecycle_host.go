package host

import (
	"context"
	"github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/client"
)

// platformPush asks the platform app, over the plane, to build a landed push. It is
// the fallback OnGitPush takes when no builder is registered in this process.
func platformPush(ctx context.Context, ev cloud.GitPushEvent) (int, error) {
	out, err := cloud.Ask[client.PushIn, client.Built](cloud.For(ctx, ev.Org), "platform", client.PlatformPush, &client.PushIn{
		Project: ev.Project, Repo: ev.Repo, Ref: ev.Ref,
		Commit: ev.Commit, CloneURL: ev.CloneURL,
	})
	if err != nil {
		return 0, err
	}
	return out.Builds, nil
}

// platformRelease asks the platform app to patch a service's image.
func platformRelease(ctx context.Context, ev cloud.ServiceReleaseEvent) error {
	_, err := cloud.Ask[client.ReleaseIn, client.Released](ctx, "platform", client.PlatformRelease,
		&client.ReleaseIn{Service: ev.Service, Image: ev.Image, SHA: ev.SHA})
	return err
}

// platformJob asks the platform app to launch a runner for a queued job.
func platformJob(ctx context.Context, ev cloud.JobEvent) (string, error) {
	out, err := cloud.Ask[client.JobIn, client.Launched](cloud.For(ctx, ev.Org), "platform", client.PlatformJob, &client.JobIn{
		Provider: ev.Provider, Repo: ev.Repo, ID: ev.ID, Labels: ev.Labels, URL: ev.URL, Token: ev.Token, Try: ev.Try,
	})
	if err != nil {
		return "", err
	}
	return out.Job, nil
}
