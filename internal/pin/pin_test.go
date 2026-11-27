// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package pin

import (
	"strings"
	"testing"
)

const sum = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func stanza(tag, line string) string {
	return "dns hanzoai/dns " + tag + "\n  " + line + "\n"
}

func TestParse_ReadsAStanza(t *testing.T) {
	f, err := Parse(strings.NewReader("# c\n" + stanza("v1.2.3", "linux/amd64 https://s3.hanzo.ai/p/dns "+sum) + "  linux/arm64 https://s3.hanzo.ai/p/dns2 " + sum + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := f.Find("dns")
	if !ok || e.Repo != "hanzoai/dns" || e.Tag != "v1.2.3" || len(e.Binaries) != 2 {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
	if b := e.Binaries["linux/amd64"]; b.URL != "https://s3.hanzo.ai/p/dns" || b.Sum != strings.TrimPrefix(sum, "sha256:") {
		t.Fatalf("binary = %+v", b)
	}
	if !f.Repos("HanzoAI/DNS") || f.Repos("hanzoai/other") {
		t.Fatal("Repos must match a pinned repository case-insensitively and nothing else")
	}
}

func TestParse_RefusesWhatCouldRunUnverified(t *testing.T) {
	cases := map[string]string{
		"no digest":       stanza("v1.2.3", "linux/amd64 https://s3.hanzo.ai/p/dns"),
		"short digest":    stanza("v1.2.3", "linux/amd64 https://s3.hanzo.ai/p/dns sha256:abc"),
		"bare digest":     stanza("v1.2.3", "linux/amd64 https://s3.hanzo.ai/p/dns "+strings.TrimPrefix(sum, "sha256:")),
		"upper digest":    stanza("v1.2.3", "linux/amd64 https://s3.hanzo.ai/p/dns "+strings.ToUpper(sum)),
		"floating tag":    stanza("main", "linux/amd64 https://s3.hanzo.ai/p/dns "+sum),
		"range tag":       stanza("v1", "linux/amd64 https://s3.hanzo.ai/p/dns "+sum),
		"prerelease tag":  stanza("v1.2.3-rc1", "linux/amd64 https://s3.hanzo.ai/p/dns "+sum),
		"plain http":      stanza("v1.2.3", "linux/amd64 http://s3.hanzo.ai/p/dns "+sum),
		"no platform":     "dns hanzoai/dns v1.2.3\n",
		"orphan platform": "  linux/amd64 https://s3.hanzo.ai/p/dns " + sum + "\n",
		"twice":           stanza("v1.2.3", "linux/amd64 https://s3.hanzo.ai/p/dns "+sum) + stanza("v1.2.4", "linux/amd64 https://s3.hanzo.ai/p/dns "+sum),
		"platform twice":  stanza("v1.2.3", "linux/amd64 https://s3.hanzo.ai/p/dns "+sum) + "  linux/amd64 https://s3.hanzo.ai/p/x " + sum + "\n",
		"bad repo":        "dns dns v1.2.3\n  linux/amd64 https://s3.hanzo.ai/p/dns " + sum + "\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(body)); err == nil {
				t.Fatalf("parsed:\n%s", body)
			}
		})
	}
}

// The lock compiled into the host is the one the gate checks: a bump that does
// not parse fails here, before it can ship.
func TestCurrent_TheCommittedLockParses(t *testing.T) {
	if _, err := Current(); err != nil {
		t.Fatal(err)
	}
}

// The committed lock is in canonical form, so a bump is a minimal diff.
func TestCurrent_TheCommittedLockIsCanonical(t *testing.T) {
	f, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if embedded != f.Format() {
		t.Fatalf("plugins.lock is not canonical; it should read:\n%s", f.Format())
	}
}

const index = `{"repo":"hanzoai/dns","tag":"v1.2.3","binaries":[
 {"name":"dns","os":"linux","arch":"arm64","url":"https://s3.hanzo.ai/plugins/hanzoai/dns/v1.2.3/dns-linux-arm64","sha256":"` + "11111111111111111111111111111111111111111111111111111111111111aa" + `"},
 {"name":"dns","os":"linux","arch":"amd64","url":"https://s3.hanzo.ai/plugins/hanzoai/dns/v1.2.3/dns-linux-amd64","sha256":"` + "22222222222222222222222222222222222222222222222222222222222222bb" + `"},
 {"name":"notes","os":"any","arch":"any","url":"https://s3.hanzo.ai/x","sha256":"cc"}
]}`

func TestFromIndex_ThenWithThenFormatRoundTrips(t *testing.T) {
	es, err := FromIndex(strings.NewReader(index))
	if err != nil || len(es) != 1 {
		t.Fatalf("entries = %v, err = %v", es, err)
	}
	f, err := File{}.With(es[0])
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(strings.NewReader(f.Format()))
	if err != nil {
		t.Fatal(err)
	}
	if again.Format() != f.Format() {
		t.Fatal("Format is not stable through Parse")
	}
	if !strings.Contains(f.Format(), "dns hanzoai/dns v1.2.3\n  linux/amd64 ") {
		t.Fatalf("platforms are not sorted:\n%s", f.Format())
	}
	// A new tag replaces the stanza whole; the other plugins stay.
	other, _ := FromIndex(strings.NewReader(strings.ReplaceAll(strings.ReplaceAll(index, `"dns"`, `"books"`), "hanzoai/dns", "hanzoai/books")))
	f, _ = f.With(other[0])
	next, _ := FromIndex(strings.NewReader(strings.ReplaceAll(index, "v1.2.3", "v1.2.4")))
	f, err = f.With(next[0])
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := f.Find("dns"); e.Tag != "v1.2.4" || len(f.Entries) != 2 {
		t.Fatalf("after a bump: %+v", f.Entries)
	}
}

func TestFromIndex_RefusesWhatTheLockWould(t *testing.T) {
	for name, body := range map[string]string{
		"no digest":    strings.Replace(index, `"sha256":"22222222222222222222222222222222222222222222222222222222222222bb"`, `"sha256":""`, 1),
		"floating tag": strings.ReplaceAll(index, "v1.2.3", "main"),
		"no binary":    `{"repo":"hanzoai/dns","tag":"v1.2.3","binaries":[{"name":"x","os":"any","arch":"any","url":"https://x","sha256":"aa"}]}`,
		"not json":     `<html>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FromIndex(strings.NewReader(body)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
