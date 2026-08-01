// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package manifest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// appsAssign matches the ONE line an app's Makefile contributes: `APPS := name`,
// or a list. Everything else in those files is the shared include.
var appsAssign = regexp.MustCompile(`(?m)^APPS\s*[:?]?=\s*(.+)$`)

// externalAssign matches mk/fleet.mk's EXTERNAL — the apps whose source is
// another module, which therefore have no apps/<name>/Makefile to be found by
// location and are named by hand instead.
var externalAssign = regexp.MustCompile(`(?m)^EXTERNAL\s*[:?]?=\s*(.+)$`)

// TestTheDriftGateSeesEveryApp is the coverage proof for the ONLY gate that
// forces the published surface back to the routes.
//
// mk/fleet.mk builds its app set from `wildcard apps/*/Makefile` plus EXTERNAL,
// and mk/fleet.mk's own comment claims that glob "reads the same single source of
// truth the mains do — no second list to fall out of step". It IS a second list,
// and it had already fallen out of step: apps/zen had no Makefile, so surface-check
// — which regenerates every subset from source and fails on any diff — never saw
// zen at all. Not "saw it and skipped it": the glob simply did not return it, and
// a set that never contains a name cannot report the name missing.
//
// That is the worst shape a gate hole can take, because every OTHER protection
// here is a bijection with plugin/<app> (TestEveryPluginNameIsInTheManifest, the
// gen-app-cmds reverse check, the Dockerfile's per-app existence check) and zen
// passed all of them. It has a row, a main, a subset and a catalogue; the only
// thing it lacked was anything that would ever re-derive them.
//
// So this asserts the set the GATE iterates equals the set the FLEET is, in the
// gate's own terms — by location for an app with source here, by name for one
// whose source is another module. Skipping is a separate question and stays a
// separate list (OPENAPI_NEEDS_BROKER, CORESIDENT): a skip is a decision that
// prints itself, and this is about the names no decision was ever made about.
func TestTheDriftGateSeesEveryApp(t *testing.T) {
	makefiles, err := filepath.Glob("../apps/*/Makefile")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(makefiles) == 0 {
		t.Skip("app sources not present in this checkout")
	}

	// By LOCATION: each apps/<dir>/Makefile names the app(s) it backs. The name is
	// read from APPS rather than from the directory, because four packages are not
	// named after their app (eval → evals, auditlog → audit,
	// plugin → plugins) and mk/plugin.mk says so in as many words.
	covered := map[string]string{} // app -> where the gate finds it
	for _, f := range makefiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		m := appsAssign.FindSubmatch(b)
		if m == nil {
			t.Errorf("%s has no APPS assignment — mk/plugin.mk errors out without one, so this "+
				"directory is in the gate's glob and cannot be built by it", f)
			continue
		}
		for _, name := range strings.Fields(string(m[1])) {
			if prev, dup := covered[name]; dup {
				t.Errorf("app %q is claimed by both %s and %s — the gate would describe it twice, "+
					"and the second run's artifact would silently win", name, prev, f)
			}
			covered[name] = f
		}
	}

	// By NAME: the apps whose source is another module.
	fleet, err := os.ReadFile("../mk/fleet.mk")
	if err != nil {
		t.Fatalf("read ../mk/fleet.mk: %v", err)
	}
	m := externalAssign.FindSubmatch(fleet)
	if m == nil {
		t.Fatal("mk/fleet.mk has no EXTERNAL assignment — this test can no longer tell which apps " +
			"the gate reaches by name, so it would pass by not looking")
	}
	for _, name := range strings.Fields(string(m[1])) {
		covered[name] = "mk/fleet.mk EXTERNAL"
	}

	// Both directions. An app the gate never visits is surface nothing forces back
	// to source; a name the gate visits that is not an app is a loop iteration over
	// something the fleet does not serve.
	declared := map[string]bool{}
	for _, a := range Apps {
		declared[a.Name] = true
		if _, ok := covered[a.Name]; !ok {
			t.Errorf("app %q is in Apps but in NO apps/*/Makefile APPS and not in EXTERNAL — "+
				"mk/fleet.mk never regenerates plugin/%s, so its subset and MCP catalogue are "+
				"whatever was last committed by hand", a.Name, a.Name)
		}
	}
	for name, where := range covered {
		if !declared[name] {
			t.Errorf("%s names app %q, which has no row in Apps — the gate would build and describe "+
				"a subsystem the host never routes to", where, name)
		}
	}
}
