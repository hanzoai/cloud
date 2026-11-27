package tools

import (
	"math"
	"slices"
	"strings"
	"unicode"
)

// rank.go — how detect ranks an org's skills and tools for one message.
//
// Each candidate is read as the terms of its name and description; a message is
// read the same way. Both are read through one vocabulary (`concepts`):
// "phones", "mobile" and "responsive" are one idea, and so are "excel",
// "spreadsheet" and "xlsx", so a skill is found by what it does rather than by
// the words its author happened to use. A shared term counts by how rare it is
// across the candidates (a word every API descriptor carries says little), more
// when it is in the candidate's name, and a name word the message does not carry
// counts against it: the name says what a skill is for.
//
// `sure` turns that evidence into a probability. It must clear a floor (a
// message about nothing a skill does picks none), and a candidate well behind
// the best of its family is held under it (the no-skill margin). The constants
// were fitted on testdata/detect, where TestDetectRanking measures them against
// hanzo.app's bar of 0.8.
const (
	// rankFloor is the evidence (rarity-weighted terms) at which a candidate is
	// as likely as not to fit; rankSlope is how fast certainty grows past it.
	// 0.8 takes about 12.5.
	rankFloor = 9.7
	rankSlope = 0.5
	// rankBehind is what each unit behind the best of the family costs: how
	// close a runner-up must stay to be chosen beside it.
	rankBehind = 1.5
	// rankName is a word's weight when it is in the candidate's name.
	rankName = 1.5
	// rankMiss is what each word of the candidate's name that the message does
	// not carry costs, as a share of its rarity: "writing-tests" fits "write
	// unit tests" better than "extending-personinsights-test-harness" does.
	rankMiss = 0.5
)

// ranker holds the candidates' terms and how rare each term is among them.
type ranker struct {
	docs []rankDoc
	idf  map[string]float64
}

type rankDoc struct {
	name  map[string]bool
	terms map[string]bool
	// howTo is whether the candidate is a skill a person writes (a hyphenated
	// name), not an operation: a brand API descriptor (product_resource) or a
	// callable tool. Each family has its own best (see relevance).
	howTo bool
}

func newRanker(cands []Tool) *ranker {
	r := &ranker{docs: make([]rankDoc, len(cands)), idf: map[string]float64{}}
	df := map[string]int{}
	for i, c := range cands {
		name := strings.TrimPrefix(c.Name, "skill_")
		d := rankDoc{
			name:  setOf(termsOf(name)),
			terms: setOf(termsOf(name + " " + c.Description)),
			howTo: c.Source == SourceSkill && !strings.Contains(name, "_"),
		}
		for t := range d.terms {
			df[t]++
		}
		r.docs[i] = d
	}
	n := float64(len(cands))
	for t, k := range df {
		r.idf[t] = math.Log((n + 1) / (float64(k) + 0.5))
	}
	return r
}

// scores is each candidate's evidence for prompt, in candidate order.
func (r *ranker) scores(prompt string) []float64 {
	asked := termsOf(prompt)
	said := setOf(asked)
	out := make([]float64, len(r.docs))
	for i, d := range r.docs {
		for _, t := range asked {
			if !d.terms[t] {
				continue
			}
			w := r.idf[t]
			if d.name[t] && !strings.HasPrefix(t, "~") {
				w *= rankName
			}
			out[i] += w
		}
		for t := range d.name {
			if !said[t] && !strings.HasPrefix(t, "~") {
				out[i] -= rankMiss * r.idf[t]
			}
		}
	}
	return out
}

// sure turns a candidate's evidence into a probability, given the best of its
// family's.
func sure(score, best float64) float64 {
	if score <= 0 {
		return 0
	}
	return 1 / (1 + math.Exp(-(rankSlope*(score-rankFloor) - rankBehind*(best-score))))
}

// relevance is each candidate's probability for prompt, by name.
func relevance(prompt string, cands []Tool) map[string]float64 {
	r := newRanker(cands)
	s := r.scores(prompt)
	// The margin is kept within a family: a how-to skill and the platform
	// operation it would use can both fit one message, and neither makes the
	// other less likely.
	var best [2]float64
	for i, v := range s {
		f := family(r.docs[i])
		best[f] = max(best[f], v)
	}
	lower := strings.ToLower(prompt)
	review := asksForReview(lower)
	out := make(map[string]float64, len(cands))
	for i, c := range cands {
		p := sure(s[i], best[family(r.docs[i])])
		name := strings.TrimPrefix(strings.ToLower(c.Name), "skill_")
		// A message that names a skill or tool asks for it.
		if len(name) >= 2 && containsIdentifier(lower, name) {
			p = max(p, 0.92)
		}
		// A pull request or diff to review is code-review's, and dev's beside it.
		if review {
			switch name {
			case "code-review":
				p = max(p, 0.94)
			case "dev":
				p = max(p, 0.86)
			}
		}
		out[c.Name] = math.Round(p*1000) / 1000
	}
	return out
}

func family(d rankDoc) int {
	if d.howTo {
		return 1
	}
	return 0
}

// asksForReview is whether a message asks for a pull request, diff or patch to
// be reviewed: "list my open PRs" names one and asks for no review.
func asksForReview(lower string) bool {
	if !isPRReviewPrompt(lower) {
		return false
	}
	for _, kw := range []string{"review", "ponytail", "code judo", "check", "look over", "look at", "go through",
		"inspect", "audit", "feedback", "critique", "what could break", "before i merge", "approve"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// containsIdentifier is whether text names id as a whole word.
func containsIdentifier(text, id string) bool {
	idx := strings.Index(text, id)
	for idx != -1 {
		beforeOK := idx == 0 || !isIdentRune(rune(text[idx-1]))
		afterIdx := idx + len(id)
		afterOK := afterIdx == len(text) || !isIdentRune(rune(text[afterIdx]))
		if beforeOK && afterOK {
			return true
		}
		next := strings.Index(text[idx+1:], id)
		if next == -1 {
			break
		}
		idx += 1 + next
	}
	return false
}

func isIdentRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isPRReviewPrompt is whether a message names a pull request, a diff or a
// code review.
func isPRReviewPrompt(pLower string) bool {
	for _, kw := range []string{
		"pull request",
		"/pull/",
		"code-review",
		"code review",
		"ponytail",
		"code judo",
	} {
		if strings.Contains(pLower, kw) {
			return true
		}
	}
	if containsIdentifier(pLower, "pr") || containsIdentifier(pLower, "pony") || containsIdentifier(pLower, "diff") {
		return true
	}
	return false
}

func setOf(ts []string) map[string]bool {
	m := make(map[string]bool, len(ts))
	for _, t := range ts {
		m[t] = true
	}
	return m
}

// termsOf is text's distinct terms: its words, stemmed, less the ones that say
// nothing, and the concepts (`~name`) its words and phrases name.
func termsOf(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	stems := make([]string, len(words))
	for i, w := range words {
		stems[i] = stem(w)
	}
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for i, w := range words {
		if len(w) >= 2 && !rankStop[w] {
			add(stems[i])
		}
	}
	for i, st := range stems {
		for _, c := range conceptTriggers[st] {
			if i+len(c.words) <= len(stems) && slices.Equal(stems[i:i+len(c.words)], c.words) {
				add(c.concept)
			}
		}
	}
	return out
}

// stem folds the endings English adds to a word, so "tests", "testing" and
// "tested" are one term and "page" and "pages" are another.
func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		w = w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"):
		w = w[:len(w)-2]
	case len(w) > 5 && strings.HasSuffix(w, "ing"):
		w = w[:len(w)-3]
		if n := len(w); n > 2 && w[n-1] == w[n-2] && !strings.ContainsRune("aeiouls", rune(w[n-1])) {
			w = w[:n-1]
		}
	case len(w) > 4 && strings.HasSuffix(w, "ed"):
		w = w[:len(w)-2]
	case len(w) > 4 && strings.HasSuffix(w, "es"):
		w = w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		w = w[:len(w)-1]
	}
	if len(w) > 3 && strings.HasSuffix(w, "e") {
		w = w[:len(w)-1]
	}
	return w
}

// concepts is one vocabulary for how a person asks and how a skill or tool
// describes itself. Each concept lists the words and phrases that name it.
var concepts = map[string][]string{
	"web-ui": {"ui", "ux", "frontend", "front end", "css", "tailwind", "html", "layout", "styling", "style", "stylesheet",
		"landing page", "homepage", "home page", "web page", "webpage", "website", "web app", "web interface", "user interface",
		"component", "navbar", "sidebar", "hero section", "design system", "dark mode", "restyle", "redesign", "beautify",
		"typography", "font", "spacing", "padding", "viewport", "visual design", "look better", "looks cramped", "screen", "dropdown", "modal"},
	"mobile": {"mobile", "phone", "iphone", "android", "tablet", "responsive", "small screen", "breakpoint", "viewport"},
	"review": {"review", "reviewer", "code review", "diff", "patch", "changeset", "what could break"},
	"test":   {"test", "testing", "unit test", "spec", "jest", "vitest", "pytest", "mocha", "coverage", "test suite", "e2e", "end to end", "tdd", "flaky"},
	"security": {"security", "secure", "insecure", "vulnerability", "vulnerable", "vuln", "exploit", "xss", "csrf", "ssrf", "idor",
		"injection", "owasp", "pentest", "cve", "attacker", "access control", "security audit", "security review"},
	"auth":        {"login", "log in", "signin", "sign in", "password", "auth", "authentication", "oauth", "jwt", "session"},
	"perf":        {"slow", "sluggish", "faster", "speed", "speed up", "performance", "perf", "optimize", "optimise", "optimization", "latency", "laggy", "bottleneck", "load time", "bundle size", "re render", "rerender", "memory leak", "leak memory"},
	"database":    {"sql", "query", "database", "db", "postgres", "postgresql", "psql", "mysql", "sqlite", "index"},
	"spreadsheet": {"excel", "xlsx", "spreadsheet", "workbook", "google sheet", "financial model", "pivot table", "formula", "sensitivity table"},
	"slides":      {"slide", "deck", "presentation", "powerpoint", "pptx", "keynote", "pitch deck"},
	"word-doc":    {"docx", "word document", "word doc", "word file", "in word", "as a word", "ms word", "microsoft word", "redline", "nda", "msa", "tracked change", "track change", "statement of work", "contract"},
	"pdf":         {"pdf", "ocr", "scanned"},
	"debug":       {"bug", "buggy", "broken", "crash", "error", "exception", "traceback", "stack trace", "fix", "debug", "not working", "regression", "null pointer", "fail"},
	"ci":          {"ci", "continuous integration", "pipeline", "github action", "workflow", "build is red", "red build", "check run"},
	"deploy":      {"deploy", "deployment", "release", "production", "ship"},
	"container":   {"docker", "dockerfile", "container", "container image", "kubernetes", "k8s", "helm", "depot"},
	"git":         {"git", "github", "gh", "branch", "merge", "rebase", "conflict", "commit", "pull request"},
	"browser":     {"browser", "scrape", "scraping", "screenshot", "click", "fill out", "navigate", "crawl", "web page", "website"},
	"social":      {"tweet", "twitter", "linkedin", "social media", "instagram", "facebook", "threads", "post to", "schedule a post", "social post"},
	"copy":        {"copy", "copywriting", "wording", "reword", "microcopy", "tone", "error message", "label", "tooltip", "headline", "tagline", "empty state", "sound friendlier", "sounds robotic", "user facing"},
	"refactor":    {"refactor", "simplify", "simplification", "cleanup", "clean up", "restructure", "readable", "readability", "dead code", "duplication", "deslop", "easier to read"},
	"docs":        {"documentation", "docs", "readme", "comment", "docstring"},
	"react":       {"react", "jsx", "tsx", "hook", "next js", "nextjs", "re render", "rerender", "useeffect", "redux"},
	"migration":   {"migration", "schema change", "alter table", "backfill", "new column", "add column"},
	"code": {"code", "coding", "function", "module", "repo", "repository", "codebase", "implement", "script", "endpoint",
		"bug", "refactor", "server", "service", "helper", "component", "class", "method", "library", "program"},
}

type trigger struct {
	words   []string // stemmed
	concept string   // "~name"
}

// conceptTriggers is concepts as stemmed phrases, by their first word.
var conceptTriggers = func() map[string][]trigger {
	out := map[string][]trigger{}
	for c, phrases := range concepts {
		for _, p := range phrases {
			var ws []string
			for w := range strings.FieldsSeq(p) {
				ws = append(ws, stem(w))
			}
			out[ws[0]] = append(out[ws[0]], trigger{ws, "~" + c})
		}
	}
	for _, ts := range out {
		slices.SortFunc(ts, func(a, b trigger) int { return strings.Compare(a.concept, b.concept) })
	}
	return out
}()

// rankStop are the words a message or a description carries that say nothing
// about which skill fits it.
var rankStop = setOf(strings.Fields(`a about above after again all also am an and any are as at be because been before being
below between both but by can cannot could did do does doing done down during each else even every few for from further get
gets getting give go going got had has have having he her here hers him his how however i if in into is it its itself just
let lets like make makes making me more most much must my myself need needs no nor not now of off on once one only or other
our ours out over own please really same see she should so some such than thanks thank that the their theirs them then
there these they this those through to too under until up us use used uses using very want wants was way we well were what
when where which while who whom why will with within without would yes yet you your yours yourself hi hello hey ok okay sure
something anything thing things stuff lot lots bit better good great nice new old right now today tomorrow help know tell
show ask asked asks look looks find work works working write writing written add adding added create creating
created build building built run running ran set setting setup update updating updated manage managing generate generating
extend extending author authoring adopt adopting gate gating guide guides helps handle handling choose choosing pick picking
fixing fix fixes fixed`))
