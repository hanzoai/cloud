// Command reach answers: does the DEPLOYED api answer every address this
// document publishes?
//
// The document is a build-time compose of each app's own projection of its own
// router (plugin/embed.go). `check` proves that compose equals the source.
// Neither proves the thing a caller actually needs: that the address is
// reachable in production. Three things break that and nothing else checks any
// of them —
//
//	a stale subset      ONE build serves a renamed route under its new name
//	                    while still publishing the name it was renamed away from
//	a missing mount     manifest/apps.go is a third, hand-maintained source of
//	                    truth and must be a superset of what each app registers
//	an edge interceptor a worker in front of the origin answering /v1/models*
//	                    and /v1/pricing* that no repo in this fleet can see
//
// THE RATCHET. Every line in the ratchet file is `METHOD /path`: an address this
// document publishes that production does not route, each one a named defect
// owned by someone. The file may only SHRINK. A 404 that is not in it fails the
// release; a line in it that now answers is a line to delete. That is what keeps
// a defect being closed from blocking every release while it is open, without
// letting a new one in — and unlike an allowlist it names what is wrong instead
// of hiding that anything is.
//
// This was openapi/reach.py. It is Go now for one reason: cloud is a Go service
// — 2000+ .go files and no Python in the shipped image — and the only thing that
// needed a Python runtime was this gate. On Ubuntu 24.04 that turned into a
// dependency the runner refuses to install (PEP 668: `pip install` answers
// "externally-managed-environment"), so a gate about routing kept failing over
// package management. gopkg.in/yaml.v3 is already a direct dependency here, so
// this costs nothing to build and nothing to provision.
//
// Usage: reach <spec.yaml> <base-url> <ratchet-file>
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/shorten"
	"github.com/hanzoai/cloud/manifest"
	"gopkg.in/yaml.v3"
)

// staged reports whether path belongs to a capability the fleet has not taken to
// ga, in which case a 404 from an anonymous prober is the CONTRACT rather than a
// miss.
//
// cloud.Stage answers a staged capability with zip's ordinary not-found, chosen
// deliberately over 403 so the refusal is not an existence oracle — and asserted
// byte-for-byte against an unrouted path. So no response can tell the two apart;
// that is the point of it. The only thing that can is the manifest, which is why
// this asks there rather than trying to read the difference off the wire.
//
// Without this, `research` and `admission` answering exactly as designed read as
// five unrouted addresses and failed the release.
func staged(path string) bool {
	owner := manifest.OwnerOf(path)
	return owner != "" && manifest.StageOf(owner) != ""
}

const timeout = 20 * time.Second

// sentinel is substituted for every {param}. Deliberately unmistakable: whatever
// comes back, a reader can tell the id was the probe's and not a real one.
const sentinel = "zzz-reach-probe"

// routerMiss is zip/fiber's router-miss body, exactly. This one string is the
// whole reason a parameterised address can be probed at all.
const routerMiss = "404 page not found"

var paramRE = regexp.MustCompile(`\{[^}]+\}`)

type op struct{ method, published, probe string }

// probedOps returns every address worth asking about. Wildcard keys are
// dropped, not filled: a made-up wildcard cannot distinguish "unrouted" from
// "routed, and the value was nonsense".
func probedOps(doc map[string]any) []op {
	paths, _ := doc["paths"].(map[string]any)
	seen := map[op]bool{}
	for path, item := range paths {
		if !strings.HasPrefix(path, "/") || strings.Contains(path, "{wildcard") {
			continue
		}
		methods, _ := item.(map[string]any)
		for method := range methods {
			switch strings.ToLower(method) {
			case "get", "head":
				seen[op{strings.ToUpper(method), path, paramRE.ReplaceAllString(path, sentinel)}] = true
			}
		}
	}
	out := make([]op, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].method != out[j].method {
			return out[i].method < out[j].method
		}
		return out[i].published < out[j].published
	})
	return out
}

// probe returns (status, first 200 bytes of body, server header). A transport
// error — DNS, TLS, timeout — is status 0: not a routing answer.
func probe(client *http.Client, base, method, url string) (int, string, string) {
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+url, nil)
	if err != nil {
		return 0, err.Error(), ""
	}
	req.Header.Set("accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, truncate(err.Error()), ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return resp.StatusCode, string(body), resp.Header.Get("server")
}

func truncate(s string) string { return shorten.To(s, 200) }

// isDark reports whether this answer PROVED the address is unrouted.
//
// The asymmetry is deliberate: a literal address has no made-up value to blame,
// so any 404 condemns it — the rule that caught an edge worker whose 404 carried
// a JSON body.
func isDark(published string, code int, body string) bool {
	if code != 404 {
		return false
	}
	if !strings.Contains(published, "{") {
		return true
	}
	return strings.TrimSpace(body) == routerMiss
}

// checkInstrument proves the oracle before trusting it. It runs on every real
// run and costs nothing.
//
// The whole gate rests on one string, so the string gets a test — and it lives
// HERE rather than in a test file nobody runs, because a measuring instrument
// that is not checked at the moment of measuring is an instrument nobody checks.
// The case that matters most is row 2: an edge worker's 404 carried a JSON body,
// and a body-only rule would have called it healthy.
func checkInstrument() error {
	doc := map[string]any{"paths": map[string]any{
		"/v1/lit":           map[string]any{"get": map[string]any{}},
		"/v1/x/{wildcard1}": map[string]any{"get": map[string]any{}},
	}}
	got := probedOps(doc)
	if len(got) != 1 || got[0] != (op{"GET", "/v1/lit", "/v1/lit"}) {
		return fmt.Errorf("wildcards must drop, got %v", got)
	}

	const handler404 = `{"status":404,"error":"no such repository"}`
	for _, c := range []struct {
		published string
		code      int
		body      string
		want      bool
		why       string
	}{
		{"/v1/lit", 404, routerMiss, true, "literal, router miss"},
		{"/v1/lit", 404, handler404, true, "literal, ANY 404 is dark"},
		{"/v1/lit", 401, "", false, "401 proves routing reached a handler"},
		{"/v1/t/{id}", 404, routerMiss + "\n", true, "parameterised, router miss"},
		{"/v1/t/{id}", 404, handler404, false, "parameterised, the id was made up"},
	} {
		if got := isDark(c.published, c.code, c.body); got != c.want {
			return fmt.Errorf("isDark(%s) = %v, want %v", c.why, got, c.want)
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: reach <spec.yaml> <base-url> <ratchet-file>")
		os.Exit(2)
	}
	if err := checkInstrument(); err != nil {
		fmt.Fprintf(os.Stderr, "::error::reach's own oracle is wrong: %v\n", err)
		os.Exit(2)
	}
	specPath, base, ratchetPath := os.Args[1], os.Args[2], os.Args[3]

	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(2)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "::error::%s is not valid YAML: %v\n", specPath, err)
		os.Exit(2)
	}

	ratchet := map[string]bool{}
	if rb, err := os.ReadFile(ratchetPath); err == nil {
		for line := range strings.SplitSeq(string(rb), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				ratchet[line] = true
			}
		}
	}

	ops := probedOps(doc)
	client := &http.Client{
		Timeout: timeout,
		// A redirect is a routing answer about a DIFFERENT address; follow it as
		// a browser would so the status reflects what a caller finally gets.
	}

	var mu sync.Mutex
	dark := map[string]string{}
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, o := range ops {
		wg.Add(1)
		go func(o op) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if staged(o.published) {
				return // its 404 is the stage gate, not a miss
			}
			code, body, server := probe(client, base, o.method, o.probe)
			if isDark(o.published, code, body) {
				if server == "" {
					server = "?"
				}
				mu.Lock()
				dark[o.method+" "+o.published] = server
				mu.Unlock()
			}
		}(o)
	}
	wg.Wait()

	var newDark, healed []string
	for k := range dark {
		if !ratchet[k] {
			newDark = append(newDark, k)
		}
	}
	for k := range ratchet {
		if _, still := dark[k]; !still {
			healed = append(healed, k)
		}
	}
	sort.Strings(newDark)
	sort.Strings(healed)

	literal := 0
	for _, o := range ops {
		if !strings.Contains(o.published, "{") {
			literal++
		}
	}
	fmt.Printf("reach: %d addresses probed against %s (%d literal, %d parameterised)\n",
		len(ops), base, literal, len(ops)-literal)
	fmt.Printf("reach: %d dark, %d on the ratchet\n", len(dark), len(ratchet))

	for _, line := range healed {
		fmt.Printf("  HEALED  %s — delete this line from %s\n", line, ratchetPath)
	}
	for _, line := range newDark {
		fmt.Printf("  DARK    %s  (answered by %s)\n", line, dark[line])
	}

	if len(newDark) > 0 {
		fmt.Printf("\n::error::%d address(es) this document publishes are not routed by %s. "+
			"The document is the contract every SDK, the MCP tool list and the CLI are "+
			"generated from, so an address nothing answers is a method every client ships "+
			"and no caller can use. Fix the owner — a stale subset regenerates, a missing "+
			"prefix goes in manifest/apps.go, an edge interceptor gives the path back to "+
			"the origin that can describe it.\n", len(newDark), base)
		os.Exit(1)
	}
	if len(healed) > 0 {
		fmt.Printf("\n::error::%d ratchet line(s) now answer. The ratchet may only shrink, "+
			"and it shrinks by being edited: delete them from %s and commit. A ratchet "+
			"nobody prunes becomes an allowlist.\n", len(healed), ratchetPath)
		os.Exit(1)
	}
	fmt.Println("reach: every published address is routed, and nothing is dark that was not")
}
