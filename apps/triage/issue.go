package triage

import (
	"cmp"
	"fmt"
	"strings"
	"time"
)

// maxReason bounds the reason an issue quotes.
const maxReason = 400

// clip is one line of s, bounded, with its backticks made quotes so a value can
// neither close the code span it is shown in nor open a fence.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "'")), " ")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

// issueText is the title and body of the issue that stands for a fault: what failed
// and where, how often and since when, the build, and a few request ids to look up.
// Everything in it came off the event plane already scrubbed.
func issueText(f Fault, cfg Config) (title, body string) {
	title = clip(f.Code+": "+cmp.Or(f.Op, f.Sample.Site.at(), f.Sample.Reason), 120)
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- triage:%s -->\n", f.FP)
	fmt.Fprintf(&b, "**%s** `%s` at `%s`\n\n", f.Class, f.Code, f.Op)
	fmt.Fprintf(&b, "- fingerprint `%s`\n", f.FP)
	fmt.Fprintf(&b, "- first seen %s, last seen %s\n", stamp(f.First), stamp(f.Last))
	fmt.Fprintf(&b, "- %d occurrences, %d in the current window\n", f.Total, f.Hits)
	if f.Release != "" {
		fmt.Fprintf(&b, "- build `%s`\n", clip(f.Release, 64))
	}
	if f.Sample.Service != "" {
		fmt.Fprintf(&b, "- service `%s`\n", clip(f.Sample.Service, 64))
	}
	if len(f.Sample.Requests) > 0 {
		fmt.Fprintf(&b, "- requests `%s`\n", strings.Join(f.Sample.Requests, "`, `"))
	}
	if at := f.Sample.Site.at(); at != "" {
		fmt.Fprintf(&b, "- crash site `%s`", clip(at, 160))
		if fn := f.Sample.Site.Function; fn != "" {
			fmt.Fprintf(&b, " in `%s`", clip(fn, 80))
		}
		if c := f.Sample.Site.Component; c != "" {
			fmt.Fprintf(&b, " (component `%s`)", clip(c, 80))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n> %s\n", clip(f.Sample.Reason, maxReason))
	if st := f.Sample.Site.Stack; st != "" {
		fmt.Fprintf(&b, "\n```\n%s\n```\n", strings.ReplaceAll(st, "```", "'''"))
	}
	if cfg.Label != "" {
		fmt.Fprintf(&b, "\nLabel `%s` hands this issue to a run.\n", cfg.Label)
	}
	return title, b.String()
}

func stamp(unix int64) string { return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04Z") }
