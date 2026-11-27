# Review

Failure here is a green run that did not test, or a red run that blocked a change nobody read. Style is not a finding.

- Cap nits at zero. Do not post style, diagnostics, or cleanup.
- A finding names a file and a line in the diff. A guess from a name is not a finding.
- Tag every finding Important, Nit, or Pre-existing. A bug this change did not introduce is Pre-existing.
- The summary starts with a count and says when there is nothing to fix.
- Re-review only the new diff. Do not repeat a finding already posted. After three rounds, stop and reconsider the approach.
- An agent's approval is not a person's. The person who prompted the change is the author.
- Risk comes from the diff.
  - Low: docs and copy. No further person once defect review and security review are green.
  - Medium: ordinary changes. One person, the author. The commit messages carry Testing, Risk, and How to revert.
  - High: iam, kms, auth, authz, billing, pay, release, installer, and any workflow file. Those three lines are required before release.

This release carries an explicit Testing/Risk/How-to-revert sign-off in its commit messages.

Shopify connector actions are gated by the forge CI run on this commit range.

Network invite intake is gated by the forge run on this commit range.

Retrying image build after forge git 503.
