# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package security

struct findingFilter {
    ScanID      text @0
    MinSeverity text @8
    Limit       i64  @16
}

struct findingList {
    Data list<bytes> @0
}

struct findingRef {
    ID text @0
}

struct findingView {
    ID          text @0
    ScanID      text @8
    RuleID      text @16
    RuleName    text @24
    Severity    text @32
    Path        text @40
    Line        i64  @48
    Preview     text @56
    Fingerprint text @64
    CreatedAt   i64  @72
}

struct ruleList {
    Data list<bytes> @0
}

struct ruleset {
    Status text @0
    Rules  i64  @8
}

struct scanDetail {
    Scan     bytes       @0
    Findings list<bytes> @8
}

struct scanList {
    Data list<bytes> @0
}

struct scanPage {
    Limit i64 @0
}

struct scanRef {
    ID text @0
}

struct scanView {
    ID        text @0
    Project   text @8
    Files     i64  @16
    Findings  i64  @24
    Critical  i64  @32
    High      i64  @40
    Medium    i64  @48
    Low       i64  @56
    CreatedAt i64  @64
}

struct submitReq {
    Project text        @0
    Files   list<bytes> @8
}

interface security {
    # Is the org's findings — rule, severity, path, line, masked preview
    # and fingerprint — newest first, across scans or within one.
    # A minSeverity outside critical|high|medium|low is refused rather than quietly
    # ignored, so a filter typo cannot read as "no findings". Strictly org-scoped, and
    # a caller with no validated org is refused.
    get_security_findings(req: findingFilter) returns (rep: findingList)
    # Returns a single finding: which rule fired, where (path and line),
    # the masked preview and the SHA-256 fingerprint of the secret — the raw secret is
    # not stored and cannot be read back.
    # Scoped to the caller's org, and a finding belonging to another org is the same
    # 404 as one that never existed.
    get_security_findings_by_id(req: findingRef) returns (rep: findingView)
    # Reports that the scanning subsystem is serving and how many
    # secret-detection rules the engine holds.
    # It has no external dependency — the answer is ok whenever the findings store
    # opened — so it measures this process rather than anything downstream. It reads
    # no tenant: a prober that sends no principal is answered, not refused.
    get_security_health() returns (rep: ruleset)
    # Is the secret-detection catalog the engine scans with.
    # It returns every rule a scan can fire — the id, name and severity a finding
    # cites — so a caller can render or triage results without hard-coding the
    # catalog. It is the same for everyone and discloses nothing tenant-specific, so
    # it carries no org scope.
    get_security_rules() returns (rep: ruleList)
    # Is the org's scan history, newest first, each as the same summary the
    # submission answered — files read, findings fired, tally by severity.
    # Strictly org-scoped: a caller only ever sees its own scans, and one with no
    # validated org is refused.
    get_security_scans(req: scanPage) returns (rep: scanList)
    # Returns one scan together with every finding on it, so the detail view
    # is one round-trip rather than a list call per scan. The findings carry masked
    # previews and fingerprints, never secrets.
    # Scoped to the caller's org: a scan id belonging to another org is the same 404
    # as an id that never existed, so a ruleset learns nothing about what exists
    # elsewhere. No validated org is refused.
    get_security_scans_by_id(req: scanRef) returns (rep: scanDetail)
    # Runs the detection engine over a batch of files and answers 201 with
    # the scan summary: how many files were read, how many findings fired, and the
    # tally by severity.
    # THE SUBMITTED CONTENT IS NEVER STORED. It is scanned in memory; what persists is
    # the finding — its rule, its path and line, a MASKED preview (first and last
    # characters kept, the middle starred) and the SHA-256 fingerprint of the raw
    # secret. The fingerprint is what makes the same secret recognisable across scans
    # and after rotation without the secret ever being written down.
    # It requires a validated org, which scopes the stored scan and every finding on
    # it; a caller with no org is refused. Bounded at 500 files and 8 MiB of total
    # content per submission — split a larger tree across scans. One scan is one
    # metered unit, and the scan is recorded in the audit log with its tally, never
    # with its findings.
    post_security_scans(req: submitReq) returns (rep: scanView)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   findingList.Data  security.findingView (list element)
#   ruleList.Data  detect.RuleView (list element)
#   scanDetail.Findings  security.findingView (list element)
#   scanDetail.Scan  security.scanView
#   scanList.Data  security.scanView (list element)
#   submitReq.Files  security.scan (list element)
