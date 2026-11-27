package release

import (
	"testing"
	"time"
)

// TestConfigFromEnvDefaultsToTheFirstPartyConsole: an operator who sets nothing
// gets the console site this deployment actually publishes, and the ORG comes
// from the sites first-party setting rather than a second literal — a white-label
// deployment that moves its first-party org moves the console with it.
func TestConfigFromEnvDefaultsToTheFirstPartyConsole(t *testing.T) {
	cfg := ConfigFromEnv()
	if cfg.Slug != "hanzo-console" {
		t.Errorf("Slug = %q, want hanzo-console", cfg.Slug)
	}
	if cfg.Org != "hanzo" {
		t.Errorf("Org = %q, want the sites first-party org (hanzo)", cfg.Org)
	}
	if cfg.Poll != defaultPoll {
		t.Errorf("Poll = %v, want %v", cfg.Poll, defaultPoll)
	}

	// The org default is the SITES setting, not a copy of it.
	t.Setenv("CLOUD_SITES_FIRSTPARTY_ORG", "lux")
	if got := ConfigFromEnv().Org; got != "lux" {
		t.Errorf("Org = %q after moving the first-party org, want lux", got)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("CLOUD_CONSOLE_ORG", "zoo")
	t.Setenv("CLOUD_CONSOLE_SITE", "zoo-console")
	t.Setenv("CLOUD_CONSOLE_POLL", "5s")

	cfg := ConfigFromEnv()
	if cfg.Org != "zoo" || cfg.Slug != "zoo-console" || cfg.Poll != 5*time.Second {
		t.Errorf("ConfigFromEnv() = %+v, want zoo/zoo-console every 5s", cfg)
	}
}

// TestPollRefusesATypo: a knob nobody can read back is a knob that has to fail
// safe. An unparseable or sub-second interval is a typo, and the honest response
// to a typo is the documented default — not a process that spins on the app that
// owns the release pointer.
func TestPollRefusesATypo(t *testing.T) {
	for _, v := range []string{"soon", "30", "0", "-1s", "10ms"} {
		t.Setenv("CLOUD_CONSOLE_POLL", v)
		if got := pollFromEnv(); got != defaultPoll {
			t.Errorf("CLOUD_CONSOLE_POLL=%q → %v, want the %v default", v, got, defaultPoll)
		}
	}
}
