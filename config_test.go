package cloud_test

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// A non-empty Enable is an allowlist: it names exactly what mounts, and every
// subsystem is governed by the same rule — there is no set that needs a second
// lever to reach.
func TestEnabled_ExplicitListIsAnAllowlist(t *testing.T) {
	c := &cloud.Config{Enable: []string{"iam", "ai"}}

	if !c.Enabled("iam") {
		t.Error("iam must be enabled once named explicitly in Enable")
	}
	if !c.Enabled("ai") {
		t.Error("ai must be enabled when named explicitly in Enable")
	}
	// An explicit list is an allowlist: an unnamed subsystem is disabled,
	// staged or not.
	if c.Enabled("commerce") {
		t.Error("commerce is not in the explicit Enable list; it must be disabled")
	}
}

// An empty Enable list mounts everything, with no subsystem held back. There used
// to be a staged set that an empty list skipped, on the rule that a subsystem was
// staged while its Mount could abort startup — and apps are their own processes
// now, so a Mount that fails takes down its own child and nothing else. The risk
// the exception existed for is structural, and the exception went with it.
func TestEnabled_EmptyListMountsEverything(t *testing.T) {
	c := &cloud.Config{}
	for _, name := range []string{"ai", "commerce", "billing", "kms", "iam", "ingress"} {
		if !c.Enabled(name) {
			t.Errorf("%s must be enabled when Enable is empty — there is no held-back set", name)
		}
	}
}

// The doors that are not the front door bind loopback unless someone says
// otherwise. Behind :9653 is the ZAP transport, which serves the IDENTICAL route
// surface as HTTP in plaintext — /v1/functions/{name}/invoke included, and that
// is arbitrary process execution. A bare ":9653" binds every interface, so an
// unconfigured process on a laptop offers it to the LAN with no credential; it
// has been reached that way from another host on the same subnet.
//
// A cluster overrides both, and says so: universe sets CLOUD_ZAP_LISTEN=:9653 on
// the cloud deployments, because svc/cloud publishes zap:9653 and superbase dials
// it. That declaration is what lets this default be the safe one — the guarded
// case is explicit, so the unattended case can stop being exposed.
//
// Asserted rather than remembered: a default is exactly the value nobody sets,
// which makes it the value nobody notices changing back.
func TestListenDefaults_AreLoopbackExceptTheFrontDoor(t *testing.T) {
	for _, k := range []string{"CLOUD_LISTEN", "CLOUD_ZAP_LISTEN", "CLOUD_HEALTH_LISTEN"} {
		t.Setenv(k, "")
	}
	cfg := cloud.LoadConfig()

	if got := cfg.ZAPListenAddr; got != "127.0.0.1:9653" {
		t.Errorf("ZAP door defaults to %q, want 127.0.0.1:9653 — a bare :9653 offers /v1/functions/{name}/invoke to the LAN", got)
	}
	if got := cfg.HealthListenAddr; got != "127.0.0.1:9090" {
		t.Errorf("health door defaults to %q, want 127.0.0.1:9090", got)
	}
}

// And the override still works, because the cluster depends on it: universe sets
// :9653 and svc/cloud publishes that port. A default that could not be overridden
// would cut superbase's transport instead of protecting a laptop.
func TestListenDefaults_ClusterCanStillBindEveryInterface(t *testing.T) {
	t.Setenv("CLOUD_ZAP_LISTEN", ":9653")
	t.Setenv("CLOUD_HEALTH_LISTEN", ":9090")
	cfg := cloud.LoadConfig()

	if got := cfg.ZAPListenAddr; got != ":9653" {
		t.Errorf("CLOUD_ZAP_LISTEN=:9653 produced %q — the cluster override must win", got)
	}
	if got := cfg.HealthListenAddr; got != ":9090" {
		t.Errorf("CLOUD_HEALTH_LISTEN=:9090 produced %q", got)
	}
}
