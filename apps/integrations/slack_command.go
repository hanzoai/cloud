package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plugin"
	"github.com/zap-proto/zip"
)

// slack_command.go is Slack's reading of the ONE command registry — the fifth
// projection of the route table (openapi/command.go), the same list the ⌘K bar
// and the CLI read. A slash body that NAMES an operation runs it; anything else
// is prose and goes to the agent brain exactly as it did before.
//
// Nothing here is a second registry. The list is zip.CommandsFromSpec over the
// fleet document, which is a pure function of the subsets the app binaries
// projected from their own routers — so a route registered this morning is a
// Slack command this afternoon, with nothing written down twice.
//
// TWO QUESTIONS, asked in order, and a body has to answer both:
//
//	NAMING   ([names]) do the first two tokens address a command? Exact first,
//	         whatever the method; failing that an unambiguous PREFIX of a GET's
//	         name, because a GET is safe and idempotent and browsing one costs
//	         nothing. A mutation is therefore reachable ONLY by its whole name —
//	         structurally, since the fuzzy branch never looks at one, not by a
//	         check someone can forget. An AMBIGUOUS abbreviation names nothing:
//	         guessing which of two operations was meant is worse than words.
//
//	INVOKING ([parses]) do the rest of the tokens FIT that command? "audit log for
//	         last week" names `audit log` and then hands it four words it has
//	         nowhere to put. Naming is not invoking, and the gap between them is
//	         most of the English language.
//
// A body that fails either is prose and goes to channelReply unchanged, which is
// what happened to every body before this file existed.
//
// AUTH is the LINKED PERSON's, never a service identity. The refresh token
// slack_link sealed in KMS mints a short-lived hanzo.id access token, and the
// call goes through the front door, so the command meets the same authorizer a
// REST client would. An unlinked user gets the prompt the channel already writes
// ([channelIdentity]); a spent one is told to link again rather than to wait. The
// workspace's own bot token is a REPLY sink and is never a credential.
//
// THE ORG IS THE WORKSPACE'S. It rides as X-Org-Id and the caller's membership is
// checked first ([acting]), because the front door DISCARDS a selection outside
// the signed set and continues in the caller's home org — so without both, a
// person who belongs to two orgs runs this workspace's mutations in the other one.
//
// THE ADDRESS IS THE COMMAND'S. A path argument is one segment ([segment]), so a
// value cannot become path structure and re-aim the request at an operation
// nobody named.
//
// The answer is EPHEMERAL. A command's result is raw org data addressed to the
// person who typed it; posting it to the whole channel is a disclosure nobody
// chose. The prose path keeps its in_channel answer — nothing about it changes.

// slashName prefixes the help and the echoed command line. The Slack app's slash
// command is `/hanzo`; this is display text only, so a workspace that installed
// it under another name reads a different prefix, never a different behaviour.
const slashName = "/hanzo"

// The reply budget. A chat message is READ AT A GLANCE, so the payload is capped
// by lines first and bytes second, and the overflow goes to the console rather
// than to a wall of JSON nobody scrolls.
const (
	briefLines = 12
	briefBytes = 1600
	headBytes  = 200
)

// ── the registry ────────────────────────────────────────────────────────────

// commandRegistry answers with the fleet's commands, built ONCE. The document is
// a function of bytes embedded at build time, so it cannot change while the
// process runs and weaving it per slash would be work whose answer is fixed.
//
// It is nothing's argument and no test repoints it: resolution takes the command
// list as a parameter, so a fixture is passed in rather than swapped for.
var commandRegistry = sync.OnceValues(fleetCommands)

// registryFailed keeps a broken weave to ONE log line per process. The failure
// is permanent (the document is a function of embedded bytes), so repeating it
// per slash would say nothing new.
var registryFailed sync.Once

// fleetCommands is the projection GET /v1/commands serves, resolved IN THIS
// PROCESS: the fleet document woven from every app's own build-time subset,
// handed to zip.CommandsFromSpec. Asking the front door for it over HTTP would
// make a Slack turn depend on the host answering a public GET about itself.
//
// It costs the embedded subsets (plugin, ~4.5 MB) in this binary. That is the
// price of reading the whole fleet's registry without a network hop, and it is
// paid once at link time rather than per request.
func fleetCommands() ([]zip.Command, error) {
	parts, err := openapi.Subsets(manifest.Names(), plugin.Spec)
	if err != nil {
		return nil, err
	}
	doc, err := openapi.Fleet(parts)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return zip.CommandsFromSpec(raw)
}

// commands is the registry, or nil when it could not be built — which is a
// SOFT failure on purpose: a slash body still reaches the agent, so a fleet that
// cannot weave its document loses commands, not Slack.
func commands(s *cloud.Service[state]) []zip.Command {
	cmds, err := commandRegistry()
	if err != nil {
		registryFailed.Do(func() {
			s.Log.Error("slack: the fleet's command registry could not be built; slash bodies go to the agent", "err", err)
		})
		return nil
	}
	return cmds
}

// ── resolution (pure) ───────────────────────────────────────────────────────

// resolve reads a slash body as a command INVOCATION. argv is what the runner
// takes — the resolved service and operation followed by the caller's own
// arguments — and ok is false for anything that is not one, which is the signal
// to answer in prose.
//
// It never picks between two commands. An abbreviation that two GETs answer to
// is not resolved at all, because running the wrong operation is worse than
// running none.
func resolve(cmds []zip.Command, text string) (argv []string, ok bool) {
	argv = words(unescapeSlack(text))
	if len(argv) < 2 {
		return nil, false
	}
	service, name := argv[0], argv[1]
	// A command whose service kebabs to nothing cannot be NAMED, and the fleet has
	// four: the internal `/_/` plane, whose first segment reduces to "". A quoted
	// empty token is the only way to type one, and it reached POST /_/commerce/tenants.
	// Unnameable is not the same as unreachable, so say it here.
	if service == "" || name == "" {
		return nil, false
	}
	if !names(cmds, argv) {
		return nil, false
	}
	// NAMING a command is not INVOKING one, and the difference is most of the
	// English language. "audit log for last week" names `audit log`, then hands it
	// four words it has nowhere to put; without this it became a CLI usage dump
	// instead of an answer. A sentence that merely opens with two matching tokens
	// is prose, so the runner's OWN parser decides — on the arity, the flag names
	// and the required flags the command declares — before any identity is
	// resolved and before anything is sent.
	return argv, parses(cmds, argv)
}

// names answers the NAMING question: do argv's first two tokens address a
// command? It rewrites argv[1] to the operation's whole name when an
// abbreviation resolved, so what follows reads one spelling.
//
// Exact first, whatever the method. Failing that, an unambiguous prefix of a
// GET — and ONLY a GET, which is what puts every mutation behind its own full
// name structurally rather than behind a check.
func names(cmds []zip.Command, argv []string) bool {
	service, name := argv[0], argv[1]
	for _, c := range cmds {
		if c.Service == service && c.Name == name {
			return true
		}
	}
	full, found := expand(cmds, service, name)
	if found {
		argv[1] = full
	}
	return found
}

// errProbe is what the parse probe's invoker answers with. It never leaves
// parses: reaching it IS the result.
var errProbe = errors.New("probe")

// parses reports whether argv is a COMPLETE invocation of the command it names.
//
// It asks by running zip's runner with an invoker that refuses, so the check is
// made by the ONE parser rather than by a second copy of its rules here, and
// nothing is sent and no credential is touched. Reaching the invoker means the
// arguments and flags fitted. A nil error without reaching it is the runner
// answering a help request, which is a command too — one that asked what it does.
func parses(cmds []zip.Command, argv []string) bool {
	reached := false
	cli := &zip.CLI{
		Name: slashName, Commands: cmds, Out: io.Discard,
		Invoke: func(context.Context, zip.Command, map[string]string, []byte) (any, error) {
			reached = true
			return nil, errProbe
		},
	}
	err := cli.Run(context.Background(), argv)
	return reached || err == nil
}

// expand completes an abbreviated GET operation. Method is the whole rule and it
// is already in the registry, so no second list says which commands are safe to
// reach loosely. A prefix claimed by two DIFFERENT names is not an answer.
func expand(cmds []zip.Command, service, prefix string) (name string, ok bool) {
	for _, c := range cmds {
		if c.Service != service || c.Method != http.MethodGet || !strings.HasPrefix(c.Name, prefix) {
			continue
		}
		if name != "" && name != c.Name {
			return "", false
		}
		name = c.Name
	}
	return name, name != ""
}

// words splits a command line into arguments. Slack has no shell, so this is
// the shell: whitespace separates, and a quoted run is one argument so a flag
// value can contain spaces. No escapes — a chat message is not a script, and a
// backslash there is far more likely to be a path than an escape.
func words(s string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	held := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, held = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if cur.Len() > 0 || held {
				out = append(out, cur.String())
				cur.Reset()
				held = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || held {
		out = append(out, cur.String())
	}
	return out
}

// unescapeSlack undoes the three entities Slack escapes in command text. It runs
// in the REVERSE order of the escape (& last), so a literal "&lt;" a user typed
// survives as itself instead of decaying into "<".
func unescapeSlack(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.ReplaceAll(s, "&amp;", "&")
}

// ── execution ───────────────────────────────────────────────────────────────

// slackCommandTurn runs one resolved command as the linked user and answers with
// the result — or with the sentence that says why it did not run. Every answer
// it produces belongs to the person who typed the command, which is why the
// caller delivers all of them ephemerally and this returns no say in it.
func slackCommandTurn(s *cloud.Service[state], ctx context.Context, org string, in Inbound, cmds []zip.Command, argv []string) string {
	link, say, _ := channelIdentity(s, org, in.Provider, in.ExternalID, in.User)
	if say != "" {
		return say
	}
	bearer, err := userBearer(s, ctx, org, in.User, link)
	if err != nil {
		s.Log.Warn("slack: minting the caller's access token", "org", org, "err", err) // never a token
		// A REFUSED credential and an unreachable IAM need OPPOSITE advice, and
		// only one of them is worth waiting for. A revoked, expired or
		// already-spent refresh token never comes back, so "try again shortly"
		// would be a loop with no exit — the exact time-waster lib/gateway.ts was
		// written against. Say the link is broken, and carry the way to fix it.
		if errors.Is(err, errLinkRejected) {
			return relink(s, in)
		}
		return "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly."
	}
	if !acting(bearer, org) {
		return "Your Hanzo account isn't a member of the org this Slack workspace belongs to, so I can't run that here. " +
			"Ask an admin of that org for an invite, or run it from an org you belong to."
	}
	// zip's own runner: it finds the command, refuses an ambiguous one by name,
	// parses the arguments and flags, and checks the required ones. Reproducing
	// any of that here would be a second parser for one command line.
	var result any
	out := &strings.Builder{}
	send := invoker("https://"+s.Domain, bearer, org)
	cli := &zip.CLI{
		Name:     slashName,
		Commands: cmds,
		Out:      out,
		Invoke: func(ctx context.Context, c zip.Command, path map[string]string, body []byte) (any, error) {
			v, err := send(ctx, c, path, body)
			result = v
			return v, err
		},
	}
	err = cli.Run(ctx, argv)
	return commandReply(argv, result, out.String(), err, s.State.consoleURL)
}

// invoker sends one command to the front door as the caller, in the workspace's
// org. The front door is where the credential is checked, so the command meets
// the SAME authorizer a REST client would — running it in this process would
// answer for operations this binary does not even link.
//
// X-Org-Id is the org that connected the WORKSPACE, not the caller's home org.
// The front door honours a selection it finds in the token's signed membership
// set and DISCARDS one it does not, so without it a person who belongs to two
// orgs runs this workspace's commands against whichever org their token calls
// home. Membership is checked before this is built ([acting]), because a
// discarded selection does not fail — it quietly acts somewhere else.
//
// It bounds the call itself. zip.Remote reads the context only before dialling
// (its transport owns the deadline from there, and the https transport declares
// none), so an unanswered request would hold a channel pool slot for as long as
// the socket stayed open. The turn's deadline is applied HERE, where the slot is
// held. The send itself is left to finish into a buffered channel nobody reads:
// it holds one goroutine and one socket until the transport gives up, which is
// the honest limit of what this side can bound.
func invoker(base, bearer, org string) zip.Invoker {
	remote := zip.Remote{Base: base, Header: map[string]string{
		"Authorization": "Bearer " + bearer,
		"X-Org-Id":      org,
	}}
	return func(ctx context.Context, c zip.Command, path map[string]string, body []byte) (any, error) {
		// A path parameter ADDRESSES a resource, so its value is one path segment.
		// zip percent-encodes it and fasthttp's SetRequestURI then decodes %2F and
		// resolves "..", so `platform apps-get ../../../v1/iam/users` arrives as
		// GET /v1/iam/users — the command that RUNS is not the command that was
		// NAMED, which is the rule this whole surface stands on. Refused here,
		// against the exact map the parser produced, and refused again in the
		// transport (zip 1.25.3 disables path normalising) so neither side alone
		// is load-bearing.
		for name, v := range path {
			if !segment(v) {
				return nil, fmt.Errorf("<%s> takes one path segment, and %q addresses somewhere else", name, v)
			}
		}
		type answer struct {
			value any
			err   error
		}
		done := make(chan answer, 1)
		go func() {
			v, err := remote.Invoke(ctx, c, path, body)
			done <- answer{v, err}
		}()
		select {
		case a := <-done:
			return a.value, a.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// segment reports whether v is a single bare path segment — what a resource id
// is.
//
// A DENYLIST of the runes that give a URL its structure, not an allowlist of the
// runes an id may hold: ids across this fleet are opaque and an allowlist would
// refuse legitimate ones, while the structural set is closed, short and the only
// thing that can retarget a request.
func segment(v string) bool {
	if v == "" || v == "." || strings.Contains(v, "..") {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\%?#`, r) {
			return false
		}
	}
	return true
}

// acting reports whether the caller may act in the workspace's org, asked
// through authz.Claims.EffectiveOrg — the same published predicate the front
// door decides with, so there is one reading of one claim.
//
// The front door DISCARDS a selection outside the signed membership set and
// continues in the caller's home org. For a browser that is right: a stale
// selection reads your own data and never someone else's. Here it is the wrong
// outcome — the person asked for something in THIS workspace, and running it
// against a different org they happen to belong to is an answer nobody wanted
// and a mutation nobody authorised. So it is refused, not redirected.
func acting(bearer, org string) bool {
	eff, _ := tokenClaims(bearer).EffectiveOrg(org)
	return eff == org && org != ""
}

// tokenClaims reads the membership set out of an access token THIS PROCESS just
// minted, over TLS, from IAM's own token endpoint against a client secret. There
// is no untrusted party between the issuer and this line, so the payload is read
// rather than re-verified — and what it is read FOR is a refusal, so a token that
// cannot be read refuses. A nil answer is a legal receiver: EffectiveOrg returns
// "" for it, which no org equals.
func tokenClaims(bearer string) *authz.Claims {
	parts := strings.Split(bearer, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var c authz.Claims
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return &c
}

// relink is what to say when the sealed credential is gone. Only linking again
// fixes it, so it carries the URL rather than advice to wait.
func relink(s *cloud.Service[state], in Inbound) string {
	u, err := linkURL(s, in.Provider, in.ExternalID, in.User)
	if err != nil {
		s.Log.Error("slack: link url", "provider", in.Provider, "err", err)
		return "Your Hanzo account link has expired. Connect it again to run commands."
	}
	return "Your Hanzo account link has expired. Connect it again: " + u
}

// userBearer mints a short-lived hanzo.id access token for a linked user from the
// refresh token the link flow sealed in KMS — the credential that flow exists to
// establish, spent here for the first time.
//
// A rotated refresh token is re-sealed, because IAM's rotated tokens are
// single-use: keeping the spent one would work exactly once and then lock the
// person out of every command until they linked again. A failure to re-seal is
// loud and does not fail the command in hand — the access token is good, and
// refusing it would trade a working command for a storage hiccup.
func userBearer(s *cloud.Service[state], ctx context.Context, org, slackUser string, link userLink) (string, error) {
	if link.Refresh == "" {
		return "", fmt.Errorf("slack: the account link carries no refresh token")
	}
	ts, err := slackOIDCToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {link.Refresh},
		"client_id":     {slackIAMClientID()},
		"client_secret": {slackIAMClientSecret()},
	})
	if err != nil {
		return "", err
	}
	if ts.Refresh != "" && ts.Refresh != link.Refresh {
		link.Refresh = ts.Refresh
		if perr := putUserLink(s, org, "slack", slackUser, link); perr != nil {
			s.Log.Error("slack: re-sealing the rotated refresh token", "org", org, "err", perr) // never the token
		}
	}
	return ts.Access, nil
}

// ── the answer, formatted for a chat message ────────────────────────────────

// commandReply renders one run: what was asked, and what came back. A refusal is
// reported IN THE WORDS THE SERVICE USED — the status and its sentence — never
// collapsed into "unavailable", because a revoked credential and an outage need
// different actions from the reader.
func commandReply(argv []string, result any, printed string, err error, console string) string {
	head := "`" + safe(truncate(slashName+" "+strings.Join(argv, " "), headBytes)) + "`"
	help := strings.TrimSpace(printed)
	if err != nil {
		reply := "✗ " + head + " — " + safe(truncate(said(err), briefBytes))
		if help != "" {
			reply += "\n" + fence(help)
		}
		return reply
	}
	// No result and something printed is `--help`: the runner answered the
	// question that was actually asked.
	if result == nil {
		if help != "" {
			return fence(help)
		}
		return "✓ " + head
	}
	body, cut := clip(plain(result))
	if body == "" {
		return "✓ " + head
	}
	reply := "✓ " + head + "\n" + fence(body)
	if cut {
		reply += "\nTruncated — the whole result is on the console: " + console
	}
	return reply
}

// fence wraps text in a mrkdwn code block.
func fence(s string) string {
	return "```\n" + safe(truncate(s, briefBytes)) + "\n```"
}

// safe is what any value derived from a payload, an error or a person's own text
// must pass through before it reaches Slack. slackEscape neutralises the three
// mrkdwn-meaningful runes, so an `<!channel>` in a result cannot broadcast to the
// workspace; the backtick is the fourth, because three of them in a row CLOSE the
// code block this reply puts values inside and everything after would render as
// markup. It becomes an apostrophe rather than disappearing — a rendering
// substitution in a summary that already shows containers as counts and already
// says where to read the whole thing.
func safe(s string) string {
	return strings.ReplaceAll(slackEscape(s), "`", "'")
}

// said is the service's own words about a refusal, with any JSON payload in them
// redacted exactly as a result is.
//
// The words matter: a 403 naming the missing grant and a 502 are different
// events, and collapsing them into "unavailable" is what sends someone to wait
// for a healthy service while their credential is simply gone. But the reason
// travels in the response body, and a body is a payload like any other — so it
// goes through the same denylist, and a body that will not parse is dropped to
// the marker rather than echoed.
func said(err error) string {
	msg := err.Error()
	i := strings.IndexAny(msg, "{[")
	if i < 0 {
		return msg
	}
	return msg[:i] + string(audit.Redact(json.RawMessage(msg[i:])))
}

// clip is the payload as the few lines a chat message holds, and whether
// anything was left out.
func clip(v any) (string, bool) {
	all := brief(v)
	cut := false
	if len(all) > briefLines {
		all, cut = all[:briefLines], true
	}
	text := strings.Join(all, "\n")
	if len(text) > briefBytes {
		text, cut = truncate(text, briefBytes), true
	}
	return text, cut
}

// brief renders a result as lines. ONE rule decides what is shown: a SCALAR is a
// value a reader takes in at a glance, so it is printed; a CONTAINER is not, so
// it is named by its size. That is a fact about the value's shape, so it holds
// for a payload nobody has seen — the alternative is a list of interesting field
// names, which would be a second registry and would be wrong for every operation
// added after it was written.
func brief(v any) []string {
	switch t := v.(type) {
	case map[string]any:
		out := make([]string, 0, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			out = append(out, k+": "+inline(t[k], 1))
		}
		return out
	case []any:
		out := make([]string, 0, len(t)+1)
		out = append(out, strconv.Itoa(len(t))+" items")
		for _, e := range t {
			out = append(out, inline(e, 0))
		}
		return out
	}
	return []string{inline(v, 0)}
}

// inline is one value on one line. depth 0 spells an object out as its own
// scalars — which is what makes a list of records readable — and deeper than
// that it is named by its size, because a nested object flattened into a line is
// noise wearing the shape of data.
func inline(v any, depth int) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return t
	case []any:
		return "[" + strconv.Itoa(len(t)) + "]"
	case map[string]any:
		if depth > 0 {
			return "{" + strconv.Itoa(len(t)) + "}"
		}
		parts := make([]string, 0, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			parts = append(parts, k+"="+inline(t[k], depth+1))
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprint(v)
}

// plain normalizes an invoker's answer into plain JSON values, REDACTED. A
// remote command answers with raw bytes, and the renderer reads shapes, not
// types.
//
// The redaction is the point of the detour. A payload posted into a chat has
// left Hanzo's custody — it is in the workspace's history, its exports and its
// search index — and some operations answer with provider config, so a field
// named api_key or client_secret would be written into Slack by a person who
// only asked what was configured. audit.Redact is the fleet's ONE
// credential-field denylist: a name that must never be recorded is by the same
// list a name that must never be chatted, and there is no second list to keep in
// step. It fails closed on anything it cannot parse.
//
// It is the ONLY thing standing there, and that is worth saying because the
// renderer looks like a second guard and is not. brief prints a top-level
// object's scalars and a top-level list's records, so `api_key` at the root and
// `access_token` on a row both reach the message; it is only DEEPER values that
// the depth rule reduces to a count. Depth is a legibility rule that happens to
// hide things, which is exactly the kind of accident that stops being true the
// day someone widens it.
func plain(v any) any {
	raw, ok := v.(json.RawMessage)
	if !ok {
		return v
	}
	safe := audit.Redact(raw)
	var out any
	if json.Unmarshal(safe, &out) != nil {
		return string(safe)
	}
	return out
}

// slackEscape neutralizes the three mrkdwn-meaningful characters (&, <, >) so
// agent- or user-derived content can never inject a link or a <!channel>
// broadcast. & first so the entities aren't double-escaped.
func slackEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
