package sites

import "testing"

// A brand's own site that ships no discovery document yields its address to the
// agent door behind this edge; a customer's domain, or a site published under a
// brand's domain, keeps its honest 404 (hanzo-inc/cloud#420, #421, #422).
func TestAgentDocsYieldOnlyOnABrandsHosts(t *testing.T) {
	for _, p := range []string{
		"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource",
		"/.well-known/mcp/server-card.json", "/.well-known/mcp.json",
		"/.well-known/agent-card.json", "/.well-known/agent.json",
	} {
		if !agentDoc(p) {
			t.Errorf("%s is not yielded", p)
		}
	}
	for _, p := range []string{"/.well-known/security.txt", "/.well-known/api-catalog", "/index.html", "/.well-known/mcp.json/x"} {
		if agentDoc(p) {
			t.Errorf("%s is yielded; a site's own address must stay the site's", p)
		}
	}
	for host, want := range map[string]bool{
		"hanzo.ai": true, "cloud.hanzo.ai": true, "hanzo.bot": true, "hanzoskills.com": true,
		"yadota.tech": false, "maxpower.hanzo.app": false, "": false,
	} {
		if got := brandHost(host); got != want {
			t.Errorf("brandHost(%q) = %v, want %v", host, got, want)
		}
	}
}
