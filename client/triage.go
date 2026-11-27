package client

// TriageGitHub tells triage one verified GitHub delivery, as GitHub signed it:
// an issue labelled or opened, a pull request opened, moved or closed, a status or
// check suite that changed. Triage decides whether the delivery is about work it
// handed to a run; one that is not is answered, never failed.
const TriageGitHub = "triage_github"

// The provider reads and writes triage makes on a change it owns. All three act as
// the App on the caller's org.
const (
	// IntegrationsGitHubPull reads one pull request: its head, its files and what
	// every status and check on the head says.
	IntegrationsGitHubPull = "integrations_github_pull"
	// IntegrationsGitHubMerge merges one pull request at the head the caller read.
	IntegrationsGitHubMerge = "integrations_github_merge"
	// IntegrationsGitHubContains says whether a ref's history holds a commit.
	IntegrationsGitHubContains = "integrations_github_contains"
)

// TriageTold is what triage made of a delivery.
type TriageTold struct {
	// Acted names what it did: "queued", "proposed", "merged", "refused", "closed",
	// or "" for a delivery that was no work of its own.
	Acted string `json:"acted,omitempty"`
}

// GitHubPullIn names one pull request.
type GitHubPullIn struct {
	Owner  string `json:"owner" validate:"required"`
	Repo   string `json:"repo" validate:"required"`
	Number int    `json:"number" validate:"required"`
}

// GitHubCheck is what one status or check run says about a head.
type GitHubCheck struct {
	// Name is the status context or the check run's name.
	Name string `json:"name"`
	// State is success, failure or pending; a check run that is not complete is
	// pending, and one that ended neutral or skipped is success.
	State string `json:"state"`
}

// GitHubPull is a pull request as GitHub holds it.
type GitHubPull struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// State is open, closed or merged.
	State string `json:"state"`
	Draft bool   `json:"draft,omitempty"`
	// Head is the commit the branch is at; Branch and Base name the two refs.
	Head   string `json:"head"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	// Changed is how many files the pull request changes, and Files names the ones
	// that were read: fewer than Changed means the list is not the whole change.
	Changed int           `json:"changed"`
	Files   []string      `json:"files"`
	Checks  []GitHubCheck `json:"checks"`
}

// GitHubMergeIn merges a pull request, and only at Sha: a branch that moved since
// it was read is refused.
type GitHubMergeIn struct {
	Owner  string `json:"owner" validate:"required"`
	Repo   string `json:"repo" validate:"required"`
	Number int    `json:"number" validate:"required"`
	Sha    string `json:"sha" validate:"required"`
}

// GitHubMerged is the commit a merge made.
type GitHubMerged struct {
	Sha string `json:"sha"`
}

// GitHubContainsIn asks whether Ref's history holds Sha.
type GitHubContainsIn struct {
	Owner string `json:"owner" validate:"required"`
	Repo  string `json:"repo" validate:"required"`
	Sha   string `json:"sha" validate:"required"`
	Ref   string `json:"ref" validate:"required"`
}

// GitHubContains answers it. A ref GitHub does not know holds nothing.
type GitHubContains struct {
	Contains bool `json:"contains"`
}
