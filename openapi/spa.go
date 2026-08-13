// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package openapi

import (
	"fmt"
	"strings"
)

// DescribeSPA declares the prose for the two addresses an embedded single-page
// app serves: the prefix itself and everything under it.
//
// Every app that embeds a UI (spa.Handler) binds exactly those two with All(),
// which publishes each of them under EVERY method this generator knows — ten
// operations that the fleet's prose gate requires a sentence for, and that no
// handler doc comment can supply because a static bundle has no typed op to lift
// one from. Written per app it was ~40 lines of near-identical text three times,
// and text repeated three times drifts: tasks still promised that a missing asset
// answers "200 with HTML rather than 404" for a while after the handler had begun
// answering 404, so the published document described behaviour the binary no
// longer had.
//
// So the sentences are derived from the ONE fact that differs — which app, at
// which prefix — and the serving policy they describe is spa.Handler's, stated in
// the one place that policy is implemented.
//
// prefix is the mount ("/tasks"), and its API subtree is "/v1"+prefix by
// construction — the same-origin shape every embed uses. name is what the product
// IS to the person reading the document ("tasks console", "tracker board"), and
// reads directly after "The ".
func DescribeSPA(prefix, name string) {
	api := "/v1" + prefix
	identify(prefix)

	// The sentence the ten operations share. It states the policy spa.Handler
	// implements, so a caller reading any one of them learns the whole contract.
	shared := "\n\nThis is the " + name + " itself — HTML and hashed assets, not an API. Only GET " +
		"and HEAD are served; every other method is refused 405. Hashed assets are returned " +
		"immutable and cached for a year, while the shell is always revalidated, so a new " +
		"deployment replaces a stale one on the next request."

	for _, m := range Methods() {
		if !serves(m) {
			continue
		}
		Describe(prefix, m,
			"The "+name,
			"Serves the application shell on GET, which is the entry point a browser loads "+
				"before it calls anything under "+api+"/."+shared)
		Describe(prefix+"/*", m,
			"The "+name+"'s assets and client-side routes",
			"Serves the static assets on GET, and returns the application shell for any path "+
				"that is not a file — client-side routing means a deep link is a shell load, "+
				"not a 404.\n\n"+
				"The one exception is "+prefix+"/assets/, which holds only content-addressed "+
				"build output: a name that is not there is a purged chunk, never a route, and "+
				"answers 404. Everywhere else a path that looks like a missing file answers "+
				"200 with the shell, so read the content type rather than the status when a "+
				"resource seems to be missing.\n\n"+
				"A bundle that was never built answers 503 under its own name on every path, "+
				"which is a failed deploy rather than a missing page."+shared)
	}

	// The methods left over. Both addresses are bound with All(), so they publish
	// every method this generator knows and the ones above are only the ones that
	// DO something — asked for rather than listed, so a method added to the
	// generator is covered the day it appears.
	for _, p := range []string{prefix, prefix + "/*"} {
		DescribeRest(p,
			"Not served by the "+name,
			"Published because this address accepts every method, but a static bundle has no "+
				"writes: the request is refused 405 and nothing is read or changed.")
	}
}

// serves reports whether a method reaches the bundle at all. spa.Handler answers
// GET and HEAD and refuses everything else 405, so those two carry the prose that
// describes serving and the rest fall to the refusal above.
func serves(method string) bool { return method == "GET" || method == "HEAD" }

// identify names the UI's two addresses, because they are the one case where a
// derived id is not unique.
//
// An embedded UI is served at the bare prefix and its API at "/v1"+prefix, and
// zip.ID drops a leading version — it says nothing that every address carries.
// That holds until something else claims the unversioned path, and here something
// does: GET /tasks and GET /v1/tasks both derive "get_tasks", which is not a
// document that can be generated. It stopped every app that embeds a UI from
// describing itself at all.
//
// The UI is what moves, and the API keeps the plain name a caller expects,
// because the API is what an SDK is cut from and a console is not. The suffix is
// `ui` — what the handler is called at every one of these mounts, and true of a
// console, a board and a call client alike.
func identify(prefix string) {
	stem := strings.ReplaceAll(strings.Trim(prefix, "/"), "/", "_") + "_ui"
	for _, m := range Methods() {
		setID(prefix, m, strings.ToLower(m)+"_"+stem)
		setID(prefix+"/*", m, strings.ToLower(m)+"_"+stem+"_assets")
	}
}

// setID declares the operation id for one route. Additive metadata on a route
// the router carries, exactly like Describe: it cannot add an operation, and an
// id whose route is not registered renders nowhere.
func setID(path, method, id string) {
	key := opKey{method: strings.ToUpper(method), path: path}
	regMu.Lock()
	defer regMu.Unlock()
	reg := registry[key]
	if reg.id != "" && reg.id != id {
		panic(fmt.Sprintf("openapi: %s %s is already named %q, cannot also be %q",
			key.method, key.path, reg.id, id))
	}
	reg.id = id
	registry[key] = reg
}
