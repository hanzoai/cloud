# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package project

struct projectsBoundDomains {
    Bound   list<bytes> @0
    Domains list<text>  @8
    Org     text        @16
    Slug    text        @24
}

struct projectsBuildSite {
    Brief text @0
    Slug  text @8
    Name  text @16
    Model text @24
}

struct projectsComplete {
    Slug    text       @0
    ID      text       @8
    Status  text       @16
    Commit  text       @24
    LiveURL text       @32
    Message text       @40
    Files   i64        @48
    Bytes   i64        @56
    Keys    list<text> @64
}

struct projectsCreate {
    Name        text  @0
    Slug        text  @8
    Description text  @16
    Framework   text  @24
    Repo        bytes @32
    Analytics   bool  @40
    Visibility  text  @48
    Upstream    text  @56
    License     text  @64
    ForkedFrom  text  @72
}

struct projectsDeploySite {
    Slug  text        @0
    Name  text        @8
    Files list<bytes> @16
}

struct projectsDeployStart {
    Slug   text @0
    Commit text @8
}

struct projectsDeploymentRef {
    Slug text @0
    ID   text @8
}

struct projectsDomain {
    Host      text        @0
    Status    text        @8
    Verified  bool        @16
    URL       text        @24
    Records   list<bytes> @32
    Detail    text        @40
    CreatedAt i64         @48
}

struct projectsDomainRef {
    Slug text @0
    Host text @8
}

struct projectsDomains {
    Claims  list<bytes> @0
    Domains list<text>  @8
    Org     text        @16
    Slug    text        @24
}

struct projectsDomainsBind {
    Slug    text       @0
    Domains list<text> @8
}

struct projectsFork {
    Slug    text @0
    Name    text @8
    Variant text @16
    Target  text @24
}

struct projectsPublish {
    Slug   text @0
    Source text @8
}

struct projectsRef {
    Slug text @0
}

struct projectsRelease {
    ReleaseID text @0
    Slug      text @8
    Objects   i64  @16
    Bytes     i64  @24
    Source    text @32
    Active    bool @40
    URL       text @48
    CreatedAt i64  @56
}

struct projectsReleaseRef {
    Slug    text @0
    Release text @8
}

struct projectsSite {
    Slug      text @0
    URL       text @8
    Name      text @16
    Status    text @24
    UpdatedAt i64  @32
}

struct projectsSiteDeploy {
    DeploymentID text       @0
    Files        list<text> @8
    Name         text       @16
    Slug         text       @24
    Status       text       @32
    URL          text       @40
}

struct projectsStar {
    Starred bool @0
}

interface project {
    # Deletes a project and takes its site off the internet.
    # The metadata delete is authoritative and everything after it is best-effort,
    # in this order: the public `<slug>` subdomain binding is released so the slug is
    # free to reclaim, the release rows are dropped so a reclaimed slug never
    # inherits the previous owner's rollback menu, the git source is retired on
    # every copy it has so a reclaimed slug never adopts a repository left behind
    # (visibility.go), the S3 origin is purged under BOTH `<org>/<slug>/` and the
    # site's sibling release space, and the edge cache-tag is flushed. A failure in
    # any of those is logged and the delete still answers 204 — resurrecting a
    # project because a purge missed would be worse than a leaked prefix.
    # Scope: a validated principal is required (403 without one) and the project is
    # resolved within that principal's org, so another tenant's slug is a 404 and
    # nothing of theirs is touched.
    delete_project_by_slug(req: projectsRef)
    # Gives a custom hostname back, so the name is free to reuse.
    # A claim is FIRST-COME and global, so an add-only surface was not ownership but
    # a leak: a customer who mistyped a domain, or claimed one they later moved
    # elsewhere, could neither reuse it nor let anyone else. This is the third
    # writer that closes it. The release is scoped to (host, org, slug), so it can
    # only ever drop THIS tenant's own claim, and it is IDEMPOTENT: releasing a host
    # we do not hold is a clean 204, never a 404 that would let a caller probe which
    # hosts other tenants hold. The edge cache-tag is flushed, since the host stops
    # routing here.
    # Scope: a validated principal is required (403 without one) and the site is
    # resolved within that principal's org, so another tenant's slug is a 404.
    delete_project_by_slug_domains_by_host(req: projectsDomainRef)
    # Removes the caller's own bookmark from a project, and answers whether
    # it is starred afterwards.
    # It removes only YOUR star — the same one star wrote — so a project other
    # people have starred stays on their lists. Unstarring one you had not starred
    # is not an error; it leaves it unstarred.
    delete_project_by_slug_star(req: projectsRef) returns (rep: projectsStar)
    # Returns every project your org owns.
    # Each row carries the slug, name, framework, visibility, status and live URL —
    # the same rows console and the builder render, because there is only one store
    # behind both. It requires a validated principal (403 without one) and is keyed
    # by that principal's org, so it never contains another tenant's project.
    get_project()
    # Returns a project's deploy history, newest version first.
    # Every deploy of the project is a row — uploads, generated sites, and git/CI
    # builds alike — carrying its version, status, source, commit, live URL, file
    # count and byte count. The short-lived upload grant a queued git deployment was
    # handed is NOT replayed here: it exists only on the 202 that minted it, so a
    # grant cannot outlive its build by being fetched again.
    # Scope: a validated principal is required (403 without one) and the project is
    # resolved within that principal's org, so another tenant's slug is a 404.
    get_project_by_slug_deployments(req: projectsRef)
    # Returns every custom hostname this site holds: the live ones, plus
    # any pending claim with the DNS records it still owes.
    # `domains` is the routing answer — the hosts that are verified right now —
    # while `claims` is the full panel, one row per host, each saying whether it is
    # live or pending and, if pending, exactly what to publish.
    # Scope: a validated principal is required (403 without one) and the site is
    # resolved within that principal's org, so another tenant's slug is a 404.
    get_project_by_slug_domains(req: projectsRef) returns (rep: projectsDomains)
    # Returns a site's releases newest-first, marking the active one —
    # the rollback menu.
    # Each row carries the release id to activate, the source it was promoted from,
    # its object and byte counts, and the URL if it is the one serving. Retention
    # bounds the list, so it is the set that can actually still be rolled back to,
    # not a full history.
    # Scope: a validated principal is required (403 without one) and the site is
    # resolved within that principal's org, so another tenant's slug is a 404.
    get_project_by_slug_releases(req: projectsRef)
    # Returns the org's deployed sites at the pretty URLs they serve at.
    # It reads the SAME org-scoped store as /v1/project and keeps only the projects
    # that are actually `live`, so a draft or a failed build is not advertised as a
    # site.
    # Scope: a validated principal is required (403 without one) and the list is
    # keyed by that principal's org.
    get_project_sites()
    # Returns one site — the same row ListSites carries, for one slug.
    # Every sub-resource under a site already answered: deployments, releases,
    # publish. The site itself did not, and a route that is never registered
    # answers 404 for a LIVE site exactly as it does for one that was never
    # created. So the one call a client makes to ask "is it there yet?" could only
    # ever say no, and a CI lane watching for its own publish would wait forever on
    # a success it had already achieved.
    # The org is the caller's, never a path segment. A slug is unique within an org
    # and two orgs may both own `tel`; taking the org from the validated principal
    # instead of the URL means a caller cannot read another org's site by editing a
    # path, and it is the same scope ListProjects and ListSites already use.
    # A site that exists but is not live is NOT found here, matching ListSites,
    # which keeps only `live` rows so a draft or a failed build is never advertised
    # as a site. One definition of "is a site", used by both.
    get_project_sites_by_slug(req: projectsRef) returns (rep: projectsSite)
    # Attaches one or more CUSTOM public hostnames to this org's site.
    # Binding a host you do not own would let you shadow it at the edge, so which
    # outcome you get depends on whether ownership is already established: a SuperAdmin
    # vouches (the operator manages the customer's DNS, so its bind IS the proof) and
    # binds VERIFIED immediately; every other caller, INCLUDING an admin of the
    # deployment's own brand org, has the host CLAIMED as pending and gets the DNS
    # challenge back in `bound[].records`. A pending claim HOLDS the name so nobody
    # else can take it, but it does not route until POST .../domains/{host}/verify
    # proves control.
    # A hostname we operate is refused to a non-vouched caller (those are assigned
    # by the platform, never claimed), a host another site already holds is a 409,
    # and a name the platform holds is a 400 for EVERY caller — a vouch skips the
    # ownership proof, never the host table's own invariant. Claims and binds are
    # idempotent for the same
    # (org, slug), and re-claiming returns the SAME token rather than invalidating a
    # record the customer has already published. The edge cache-tag is flushed
    # afterwards so a newly-verified host serves the current build immediately.
    # Scope: a validated principal is required (403 without one) and the site is
    # resolved within that principal's org, so another tenant's slug is a 404.
    post_project_by_slug_domains(req: projectsDomainsBind) returns (rep: projectsBoundDomains)
    # Checks the DNS challenge for a pending custom hostname and, when
    # it passes, promotes the host so it begins routing at the edge.
    # It answers 200 either way, with the host's honest current state: verified once
    # the TXT record is found, still pending — with the records to publish and the
    # resolver's own explanation in `detail` — when it is not. A not-yet is not an
    # error: the check ran, DNS simply has not propagated, and the customer retries.
    # An already-verified host is returned unchanged without re-resolving. On a
    # successful promotion the edge cache-tag is flushed, since the host routes as
    # of that moment.
    # Scope: a validated principal is required (403 without one). Both the site and
    # the claim are resolved within that principal's org, so a host claimed by
    # another tenant is "not claimed by this site".
    post_project_by_slug_domains_by_host_verify(req: projectsDomainRef) returns (rep: projectsDomain)
    # Promotes a build output into a new release AND goes live with it —
    # create+activate in one call, which is the 99% path.
    # It is exactly the two halves in sequence with no extra semantics, so the
    # staged flow and the one-shot flow can never drift apart: `source` is promoted
    # under the same org-relative rule and the same guards CreateRelease applies,
    # then the site's pointer is flipped to it, the public host is claimed and the
    # edge is purged. Idempotent on unchanged bytes — same manifest, same release id,
    # no copy — and billed once, after the release exists.
    # Scope: a validated principal is required (403 without one) and the site is
    # resolved within that principal's org, so another tenant's slug is a 404.
    post_project_by_slug_publish(req: projectsPublish) returns (rep: projectsRelease)
    # Promotes a build output into a new immutable release WITHOUT
    # serving it — the staged half of publishing, for when you want to check a
    # release before it goes live. Answers 201.
    # `source` is a path RELATIVE to your org's own storage space: the org segment
    # is prepended server-side from the validated principal and the bucket is never
    # in the request at all, so a server-side copy can only ever reach bytes your
    # org already owns. The prefix is listed, content-addressed (SHA-256 over the
    # sorted manifest of key/size/etag), and copied into an immutable
    # `<org>/.releases/<slug>/<id>/` prefix; the row is written LAST, so a partial
    # copy is unreachable rather than merely unlikely. Re-publishing an unchanged
    # source is idempotent BY CONSTRUCTION — same bytes, same id, no copy at all.
    # The source must contain index.html at its root and stay under the same file
    # and byte caps an artifact deploy does (413 past them); a source that changes
    # mid-copy is a 409 and the release is abandoned. Each publish also reclaims
    # releases past the retention depth, so a site's release space stays bounded.
    # This is the billable half — the hosting gate runs before any copy, and the
    # debit lands once the release exists.
    # Scope: a validated principal is required (403 without one) and the site is
    # resolved within that principal's org, so another tenant's slug is a 404.
    post_project_by_slug_releases(req: projectsPublish) returns (rep: projectsRelease)
    # Points the site at an existing release — the go-live, and
    # equally the ROLLBACK.
    # Aim it at an older release and the site serves that one again: releases are
    # immutable and retained to the retention depth, so nothing is rebuilt or
    # re-copied and the flip is one atomic statement. Before the flip, two
    # conditions run in the order that gives each its own honest answer — the ROW
    # says whether this release exists for this tenant at all (404, with no signal
    # about a foreign id), and only then do the BYTES say whether it can still serve
    # (410 GONE when retention has reclaimed them; that rollback target is not
    # coming back, so publish again). Going live also claims the public host and
    # purges the edge, so the release is reachable and no cached predecessor is
    # served. NOT billed: no new content is produced, only a pointer moved.
    # Scope: a validated principal is required (403 without one) and the site is
    # resolved within that principal's org, so another tenant's slug is a 404.
    post_project_by_slug_releases_by_release_activate(req: projectsReleaseRef) returns (rep: projectsRelease)
    # Generates a self-contained, mobile-responsive static site from a
    # natural-language brief and deploys it live in one call.
    # One inference call turns `brief` (capped at 8 KiB) into a file manifest, which
    # then runs through the SAME validation, guards and viewport guarantee as a
    # hand-supplied manifest: index.html required at the root, absolute and
    # traversal paths rejected, per-file and total size capped, and a mobile
    # viewport meta tag injected into every HTML document that lacks one. The
    # generated site is fully inline — no CDNs, no remote fonts or images — so it is
    # CSP-safe. `slug` and `name` are optional: the model's own title is preferred,
    # and a slug is derived or minted when none is given.
    # It writes into the SAME org-scoped store as /v1/project — it ensures a
    # project (framework `static`) for the resolved slug and records a deployment —
    # so this is a second entry point to one publish pipeline, not a second copy of
    # project state. Ordering is the billing contract: the hosting gate runs BEFORE
    # any inference or upload, so a denied gate generates and uploads NOTHING, and
    # the debit lands once, only after the site is actually live. The tokens are
    # billed to the same ledger the hosting fee was reserved against.
    # Answers 503 when object storage or inference is unconfigured, and 400 when the
    # model's manifest cannot be parsed or fails the guards.
    # Scope: a validated principal is required (403 without one) and the site is
    # published into THAT principal's org.
    post_project_sites(req: projectsBuildSite) returns (rep: projectsSiteDeploy)
    # Deploys a caller-supplied file manifest — the deploy_site
    # capability an agent calls — and answers with where it went live.
    # `files` is a list of {path, content} pairs, the same shape the brief build
    # emits, and it runs through the SAME guards: index.html required at the root,
    # absolute and traversal paths rejected, per-file and total size capped, and a
    # mobile viewport meta tag injected into every HTML document that lacks one — so
    # a hand-built site is exactly as safe and as responsive as a generated one.
    # `slug` and `name` are optional; a slug is derived from the name or minted.
    # It writes into the SAME org-scoped store as /v1/project, ensuring a project
    # (framework `static`) for the resolved slug and recording a deployment. The
    # hosting gate runs before the upload and the debit lands once, after the site
    # is live — a failed upload is never billed. Answers 503 when object storage is
    # unconfigured.
    # Scope: a validated principal is required (403 without one) and the site is
    # published into THAT principal's org.
    post_project_sites_deploy(req: projectsDeploySite) returns (rep: projectsSiteDeploy)
    # Bookmarks a project for the person calling, and answers whether it is
    # starred afterwards.
    # The star is YOURS: it is keyed by you as well as by the project, so two people
    # see two answers for the same one and starring it says nothing about anybody
    # else's list. Starring a project you have already starred leaves it starred.
    put_project_by_slug_star(req: projectsRef) returns (rep: projectsStar)
}

# ---------------------------------------------------------------------
# 17 op(s) here. What follows is what this schema does not carry.
#
# blocked (10) — the op is absent; the field has no wire form:
#   get_project_by_slug  projectsProject.Tags  map[string]string  (map)
#   get_project_by_slug_deployments_by_id  projectsDeployment.Upload  projects.projectsUploadGrant  (reaches one)
#   get_project_edge  edgeState.Policy  map[string]string  (map)
#   patch_project_by_slug  projectsProject  projects.projectsProject  (reaches one)
#   patch_project_by_slug  projectsUpdate.Tags  map[string]string  (map)
#   post_project  projectsProject  projects.projectsProject  (reaches one)
#   post_project_by_slug_deployments  projectsDeployment  projects.projectsDeployment  (reaches one)
#   post_project_by_slug_deployments_by_id_complete  projectsDeployment  projects.projectsDeployment  (reaches one)
#   post_project_by_slug_purge  projectsProject  projects.projectsProject  (reaches one)
#   post_project_fork  projectsProject  projects.projectsProject  (reaches one)
#
# opaque (5) — crosses, arrives without its name:
#   projectsBoundDomains.Bound  projects.projectsDomain (list element)
#   projectsCreate.Repo  struct { URL string "json:\"url\""; Branch string "json:\"branch\"" }
#   projectsDeploySite.Files  projects.projectsFile (list element)
#   projectsDomain.Records  fqdn.Record (list element)
#   projectsDomains.Claims  projects.projectsDomain (list element)
