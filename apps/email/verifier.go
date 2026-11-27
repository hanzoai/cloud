package email

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	emailverifier "github.com/AfterShip/email-verifier"
)

// lists is the engine's own data: free mail providers, disposable domains, role
// names. It is read, never written, so one is shared.
var lists = emailverifier.NewVerifier()

// Free reports whether domain belongs to a free mail provider, where an address
// is one person's and never speaks for a company.
func Free(domain string) bool {
	return lists.IsFreeDomain(strings.ToLower(strings.TrimSpace(domain)))
}

// Verifier executes composable, independent email verification checks.
type Verifier interface {
	Verify(ctx context.Context, in *VerificationInput) (*VerificationResult, error)
}

// CheckEngine implements Verifier. It has no external service or database dependencies.
type CheckEngine struct {
	resolver *net.Resolver
	timeout  time.Duration
}

// NewVerifier creates a new independent verification check engine.
func NewVerifier(resolver *net.Resolver, timeout time.Duration) *CheckEngine {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &CheckEngine{
		resolver: resolver,
		timeout:  timeout,
	}
}

// Verify evaluates the requested checks against the provided email address.
func (e *CheckEngine) Verify(ctx context.Context, in *VerificationInput) (*VerificationResult, error) {
	if in == nil || strings.TrimSpace(in.Email) == "" {
		return nil, errors.New("email is required")
	}

	rawEmail := strings.TrimSpace(in.Email)
	out := &VerificationResult{
		Email:  rawEmail,
		Checks: VerificationChecks{},
	}

	// Normalize requested checks list.
	// Default to syntax, domain, mx, classification if not explicitly specified.
	selected := make(map[CheckName]bool)
	if len(in.Checks) == 0 {
		selected[CheckSyntax] = true
		selected[CheckDomain] = true
		selected[CheckMX] = true
		selected[CheckClassification] = true
	} else {
		for _, c := range in.Checks {
			selected[CheckName(strings.ToLower(strings.TrimSpace(c)))] = true
		}
	}

	ev := emailverifier.NewVerifier()
	ev.ConnectTimeout(e.timeout)
	ev.OperationTimeout(e.timeout)
	if in.ProxyURI != "" {
		ev.Proxy(in.ProxyURI)
	}
	if in.HelloName != "" {
		ev.HelloName(in.HelloName)
	}
	if in.FromEmail != "" {
		ev.FromEmail(in.FromEmail)
	}

	// 1. Syntax Check: address structure
	var username, domain string
	syntaxPassed := false

	parsed := ev.ParseAddress(rawEmail)
	if parsed.Valid {
		syntaxPassed = true
		username = parsed.Username
		domain = strings.ToLower(parsed.Domain)
	} else {
		// Fallback address parsing to extract username and domain if present
		parts := strings.Split(rawEmail, "@")
		if len(parts) == 2 {
			username = parts[0]
			domain = strings.ToLower(parts[1])
		}
	}

	if selected[CheckSyntax] {
		if syntaxPassed {
			out.Checks.Syntax = &SyntaxCheck{
				Status:   StatusPass,
				Username: username,
				Domain:   domain,
			}
		} else {
			out.Checks.Syntax = &SyntaxCheck{
				Status: StatusFail,
				Reason: "invalid_syntax",
			}
		}
	}

	// If domain is empty or syntax failed fundamentally, mark downstream network checks as skipped or failed.
	if domain == "" || !syntaxPassed {
		if selected[CheckDomain] {
			out.Checks.Domain = &DomainCheck{
				Status: StatusSkipped,
				Reason: "invalid_syntax",
			}
		}
		if selected[CheckMX] {
			out.Checks.MX = &MXCheck{
				Status: StatusSkipped,
				Reason: "invalid_syntax",
			}
		}
		if selected[CheckClassification] {
			out.Checks.Classification = &ClassificationCheck{
				Status: StatusSkipped,
				Reason: "invalid_syntax",
			}
		}
		if selected[CheckSMTP] {
			out.Checks.SMTP = &SMTPCheck{
				Status: StatusSkipped,
				Reason: "invalid_syntax",
			}
		}
		return out, nil
	}

	// 2. Domain Check: mail infrastructure existence
	if selected[CheckDomain] {
		lookupCtx, cancel := context.WithTimeout(ctx, e.timeout)
		ips, err := e.resolver.LookupHost(lookupCtx, domain)
		cancel()

		if err == nil && len(ips) > 0 {
			out.Checks.Domain = &DomainCheck{
				Status: StatusPass,
			}
		} else if isNoSuchHost(err) {
			out.Checks.Domain = &DomainCheck{
				Status: StatusFail,
				Reason: "domain_not_found",
			}
		} else {
			out.Checks.Domain = &DomainCheck{
				Status: StatusUnknown,
				Reason: "dns_lookup_error",
			}
		}
	}

	// 3. MX Check: DNS mail exchange records
	var mxRecords []*net.MX
	if selected[CheckMX] || selected[CheckSMTP] {
		lookupCtx, cancel := context.WithTimeout(ctx, e.timeout)
		mxs, err := e.resolver.LookupMX(lookupCtx, domain)
		cancel()

		if selected[CheckMX] {
			if err == nil && len(mxs) > 0 {
				recNames := make([]string, 0, len(mxs))
				for _, rec := range mxs {
					recNames = append(recNames, rec.Host)
				}
				out.Checks.MX = &MXCheck{
					Status:  StatusPass,
					Records: recNames,
				}
				mxRecords = mxs
			} else if isNoSuchHost(err) || len(mxs) == 0 {
				out.Checks.MX = &MXCheck{
					Status: StatusFail,
					Reason: "no_mx_records",
				}
			} else {
				out.Checks.MX = &MXCheck{
					Status: StatusUnknown,
					Reason: "dns_lookup_error",
				}
			}
		} else if err == nil {
			mxRecords = mxs
		}
	}

	// 4. Classification Check: domain attributes (free/disposable/role)
	// These are attributes for business policy, not inherently invalid.
	if selected[CheckClassification] {
		isDisposable := ev.IsDisposable(domain)
		isFree := ev.IsFreeDomain(domain)
		isRole := ev.IsRoleAccount(username)

		out.Checks.Classification = &ClassificationCheck{
			Status:     StatusPass,
			Disposable: isDisposable,
			Free:       isFree,
			Role:       isRole,
		}
	}

	// 5. Mailbox Probing (SMTP): strictly explicit and opt-in
	if selected[CheckSMTP] {
		if len(mxRecords) == 0 && (out.Checks.MX != nil && out.Checks.MX.Status == StatusFail) {
			out.Checks.SMTP = &SMTPCheck{
				Status: StatusFail,
				Reason: "no_mx_records",
			}
		} else {
			smtpRes, err := ev.CheckSMTP(domain, username)
			if smtpRes == nil {
				reason := "smtp_probe_error"
				if err != nil {
					reason = err.Error()
				}
				out.Checks.SMTP = &SMTPCheck{
					Status: StatusUnknown,
					Reason: reason,
				}
			} else {
				chk := &SMTPCheck{
					HostExists:  smtpRes.HostExists,
					Deliverable: smtpRes.Deliverable,
					CatchAll:    smtpRes.CatchAll,
					Disabled:    smtpRes.Disabled,
					FullInbox:   smtpRes.FullInbox,
				}
				if !smtpRes.HostExists {
					chk.Status = StatusFail
					chk.Reason = "mail_host_unreachable"
				} else if smtpRes.Disabled {
					chk.Status = StatusFail
					chk.Reason = "mailbox_disabled"
				} else if smtpRes.FullInbox {
					chk.Status = StatusFail
					chk.Reason = "mailbox_full"
				} else if smtpRes.CatchAll {
					chk.Status = StatusPass
					chk.Reason = "catch_all_accepted"
				} else if smtpRes.Deliverable {
					chk.Status = StatusPass
				} else if err != nil && isTemporarySMTPError(err) {
					chk.Status = StatusUnknown
					chk.Reason = "temporary_failure"
				} else {
					chk.Status = StatusFail
					chk.Reason = "mailbox_not_found"
				}
				out.Checks.SMTP = chk
			}
		}
	}

	// 6. Suggestions: typo candidate generation without rewriting original address
	if in.Suggestions != nil && *in.Suggestions {
		suggestedDomain := ev.SuggestDomain(domain)
		if suggestedDomain != "" && suggestedDomain != domain {
			suggestedEmail := username + "@" + suggestedDomain
			out.Suggestions = []Suggestion{
				{
					Email:  suggestedEmail,
					Reason: "domain_typo",
				},
			}
		}
	}

	return out, nil
}

func isNoSuchHost(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such host") || strings.Contains(msg, "not found")
}

func isTemporarySMTPError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "greylist") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "temporar") ||
		strings.Contains(msg, "try again") ||
		strings.Contains(msg, "connection refused")
}
