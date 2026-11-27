// resolve.go — the ONE way an operation addressed by {name} finds its application.
//
// Three kinds answer to the word. A tenant's App CR (hanzo.ai/v1) lives in a
// namespace; a platform application is a Hanzo CD Application (apps.hanzo.ai/
// v1alpha1), which is what the estate actually runs; a site (`site-<slug>`) is
// published to the projects app and served by the sites edge (sites.go). Every
// operation under /v1/deploy/applications/{name} resolves through here, so get,
// resource-tree, syncwindows, revision metadata, the resource-tree stream and sync
// cannot disagree about which applications exist for a caller.

package deploy

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hanzoai/cloud/client"
)

// target is a resolved application: exactly one of cr, cd and site is set.
type target struct {
	ns   string                     // namespace of cr (App CR)
	cr   *unstructured.Unstructured // tenant App CR
	cd   *unstructured.Unstructured // Hanzo CD Application
	site *client.SiteApp            // a site, with its history
	org  string                     // the org a site belongs to
}

// resolve finds the application `name` (already normalized by appName) visible to
// sc. An App CR wins over a CD Application of the same name, and either over a
// site. An application that is not the caller's is a 404, never a 403, so no name
// is confirmed to exist.
func (o ops) resolve(ctx context.Context, sc scope, name string) (target, error) {
	ns, err := sc.findNamespace(o.s, ctx, name)
	if err != nil {
		obj, ok, cerr := o.cdObject(ctx, sc, name)
		if cerr != nil {
			return target{}, k8sErr(o.s, "get", cerr)
		}
		if ok {
			return target{cd: obj}, nil
		}
		// A site is its org's, so it is named only in an org: the caller's own, or
		// the one a SuperAdmin acts in.
		if slug, isSite := strings.CutPrefix(name, siteName); isSite && slug != "" && sc.home() != "" {
			st, serr := o.site(ctx, slug)
			if serr != nil {
				return target{}, serr
			}
			if st != nil {
				return target{site: st, org: sc.home()}, nil
			}
		}
		return target{}, err
	}
	cr, _, err := getAppCR(o.s, ctx, ns, name)
	if err != nil {
		return target{}, k8sErr(o.s, "get", err)
	}
	return target{ns: ns, cr: cr}, nil
}

// cdTree projects a CD Application's managed resources (status.resources) as the
// flat ApplicationTree: the objects CD applies, each with its health.
func cdTree(cd *unstructured.Unstructured) argoTree {
	out := argoTree{Nodes: []argoNode{}, OrphanedNodes: []argoNode{}, Hosts: []any{}}
	res, _, _ := unstructured.NestedSlice(cd.Object, "status", "resources")
	for _, r := range res {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { v, _ := m[k].(string); return v }
		health := "Unknown"
		if h, ok := m["health"].(map[string]any); ok {
			if v, _ := h["status"].(string); v != "" {
				health = v
			}
		}
		msg := ""
		if h, ok := m["health"].(map[string]any); ok {
			msg, _ = h["message"].(string)
		}
		out.Nodes = append(out.Nodes, argoNode{
			argoResourceRef: argoResourceRef{
				Group: str("group"), Version: str("version"), Kind: str("kind"),
				Namespace: str("namespace"), Name: str("name"),
			},
			Health: &argoHealth{Status: health, Message: msg},
		})
	}
	return out
}
