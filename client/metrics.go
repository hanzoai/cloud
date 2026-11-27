package client

// metrics.go is the wire of the estate's open source collector (#456): the
// platform's own daily read of GitHub and the package registries, which git runs
// and provider answers GitHub for. Both ops are the platform's: the plane states
// the caller, and any caller that is not the reserved admin org is refused.

const (
	// GitMetricsCollect reads the estate's open source figures once and writes
	// them to the forge's metrics store: every public repository's stars, forks,
	// open issues and watchers, and the downloads of the packages those
	// repositories publish. The cron app asks it by name as the platform.
	GitMetricsCollect = "git_metrics_collect"

	// IntegrationsGitHubFigures answers what GitHub says of up to 100
	// repositories: their counts and the manifests at their roots. The App key
	// stays in provider and no credential crosses; what leaves is facts about
	// public repositories.
	IntegrationsGitHubFigures = "integrations_github_figures"
)

// MetricsCollected is what one collection did.
type MetricsCollected struct {
	// Repos is how many repositories GitHub answered for.
	Repos int `json:"repos"`
	// Packages is how many package registrations were written.
	Packages int `json:"packages"`
	// Figures is how many figures were written.
	Figures int `json:"figures"`
	// Failed names each source that failed or ran out of time, and why. What the
	// other sources read is written regardless.
	Failed []string `json:"failed,omitempty"`
}

// GitHubFiguresIn names the repositories asked about, as owner/name. The org
// is not a field: the plane's caller decides whether the platform is asking.
type GitHubFiguresIn struct {
	Repos []string `json:"repos" validate:"required"`
}

// GitHubFile is one file's text at the root of a repository's default branch.
type GitHubFile struct {
	// Path is the file's name at the root: package.json, pyproject.toml,
	// setup.cfg or Cargo.toml.
	Path string `json:"path"`
	// Text is the file as GitHub holds it. A file GitHub truncates is not sent.
	Text string `json:"text"`
}

// GitHubFigure is what GitHub says of one public repository.
type GitHubFigure struct {
	// Repo is owner/name as GitHub spells it now.
	Repo string `json:"repo"`
	// Stars, Forks, Issues (open) and Watchers are GitHub's counts.
	Stars    int64 `json:"stars"`
	Forks    int64 `json:"forks"`
	Issues   int64 `json:"issues"`
	Watchers int64 `json:"watchers"`
	// Files are the manifests the repository holds at its root.
	Files []GitHubFile `json:"files,omitempty"`
}

// GitHubFigures answers GitHubFiguresIn.
type GitHubFigures struct {
	// Repos are the public repositories GitHub answered for.
	Repos []GitHubFigure `json:"repos"`
	// Missing are the repositories asked about that GitHub does not publish: gone,
	// renamed away or private.
	Missing []string `json:"missing,omitempty"`
}
