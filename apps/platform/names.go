package platform

// names.go — an app's name in the one public namespace.
//
// `<name>.<sitesHost>` is ONE namespace, shared by every org's apps and every
// project's site: the apex's certificate covers one label, so the name is the
// whole address. It lives in the projects store's site_hosts table, the
// first-come table a published site binds into, and it is reached over the plane
// because that store is another app's (client.SitesClaim). A name is claimed when
// an app takes its `<name>.<sitesHost>` host and released when the app is
// deleted. A name anyone else holds — another org, another app of this org, a
// site, or the platform itself (sites/reserved.go) — is not this app's: a deploy
// that named no host takes the org's own spelling of it instead (ownNames, #769),
// and one that named the host is refused 409.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// nameCall bounds one claim or release.
const nameCall = 10 * time.Second

// holder is what holds a name in the namespace: an app of the platform store by
// its id, a declared app by its name. A site holds its name as its own slug, so
// the three can never read as one another.
func holder(id string) string { return "app:" + id }

// claim takes name for holder in org, answering 409 when anyone else holds it.
// A deployment without the projects app has no namespace to claim in and stays
// inert, as every other call to that peer does (plane.ErrNoPeer).
func claim(s *cloud.Service[state], ctx context.Context, org, name, holder string) error {
	ctx, cancel := context.WithTimeout(plane.For(context.WithoutCancel(ctx), org), nameCall)
	defer cancel()
	out, err := projectpeer.SitesClaim(ctx, &client.NameIn{Name: name, Holder: holder})
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return nil
	case err != nil:
		return zip.Errorf(http.StatusServiceUnavailable, "the name %s could not be claimed: %v", name, err)
	case out.Taken:
		return zip.ErrConflict(name + "." + s.State.sitesHost + " is taken: app and site names are one namespace, unique across every organization")
	}
	return nil
}

// ownTries bounds the names one deploy asks the namespace for: its own, the
// org's spelling of it, and that numbered.
const ownTries = 6

// ownNames is the order a deploy asks for its name in when the caller named no
// host: name itself, then `<name>-<org>`, then that numbered from 2. Every one is
// a DNS label of at most 40 characters (slugRE): a long pair is cut, and a cut
// that ends on a dash drops it. The order is fixed, so the same deploy asked again
// reaches the name it holds before any it does not.
func ownNames(name, org string) []string {
	fit := func(s string, n int) string {
		if len(s) > n {
			s = s[:n]
		}
		return strings.TrimRight(s, "-")
	}
	base := fit(name+"-"+org, 40)
	out := []string{name, base}
	for i := 2; len(out) < ownTries; i++ {
		suffix := "-" + strconv.Itoa(i)
		out = append(out, fit(base, 40-len(suffix))+suffix)
	}
	return slices.DeleteFunc(out, func(n string) bool { return !slugRE.MatchString(n) })
}

// ownName claims the first of ownNames(name, org) that is free or already this
// org's app's, and answers it. The claim of each is the namespace's own answer,
// so two orgs racing for one spelling get one each, and a name this org's app
// holds is reached again before any it does not. Every spelling taken is the 409
// a single name was; a claim that fails for any other reason ends the search.
func ownName(s *cloud.Service[state], ctx context.Context, org, name string) (string, error) {
	var last error
	for _, n := range ownNames(name, org) {
		err := claim(s, ctx, org, n, holder(n))
		if err == nil {
			return n, nil
		}
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusConflict {
			return "", err
		}
		if last == nil {
			last = err
		}
	}
	return "", last
}

// renamedNotice says that the app is not called what was asked, and why.
func renamedNotice(s *cloud.Service[state], asked, name string) string {
	return asked + "." + s.State.sitesHost + " is taken (app and site names are one namespace, unique across every " +
		"organization), so this app is " + name + ", at " + name + "." + s.State.sitesHost + "."
}

// release gives name back, so it is free again — for this org or any other. A
// failure is said and not fatal: the app is gone either way.
func release(s *cloud.Service[state], ctx context.Context, org, name, holder string) {
	ctx, cancel := context.WithTimeout(plane.For(context.WithoutCancel(ctx), org), nameCall)
	defer cancel()
	if _, err := projectpeer.SitesRelease(ctx, &client.NameIn{Name: name, Holder: holder}); err != nil && !errors.Is(err, plane.ErrNoPeer) {
		s.Log.Warn("an app's name was not released (it stays held until released again)", "org", org, "name", name, "err", err)
	}
}
