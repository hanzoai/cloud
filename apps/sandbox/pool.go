// The warm pool: where a box comes from.
//
// The expensive part of a sandbox is not starting a container, it is POPULATING
// one — the image pull, the clone, the install. Everything here attacks
// populate, not boot:
//
//	1. Pods for each class already exist, idle, running `sleep infinity` (the
//	   CMD the existing Dockerfile.sandbox already has). Claiming one is a LABEL
//	   PATCH — not a create, not a pull, not a scheduler cycle.
//	2. The project volume carries the checkout AND the caches (node_modules, the
//	   pnpm store, cargo, go mod), so a resume is `git fetch && checkout` and
//	   nothing else. Caches live on the volume rather than in the image, so an
//	   image bump does not cost a cold install.
//
// There is no os/exec in this file and there must never be. It creates and
// patches Kubernetes objects; the boxes run the code.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/apps/k8s"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Labels a box pod carries. `state` is what a claim patches; the rest is how an
// operator finds a tenant's box without reading a database.
const (
	labClass   = "hanzo.ai/box-class"
	labState   = "hanzo.ai/box-state" // warm | bound
	labOrg     = "hanzo.ai/box-org"
	labBox     = "hanzo.ai/box-id"
	labProject = "hanzo.ai/box-project"
)

type pool struct {
	ns      string
	image   string // registry.hanzo.ai/hanzoai/box, without a tag
	tag     string
	port    string
	dyn     dynamic.Interface
	initErr string
	mu      sync.Mutex
}

func newPool() *pool {
	p := &pool{
		// Boxes do NOT live in `hanzo`. code-exec.yaml's policy admits ingress
		// from namespace hanzo, and a box must not sit beside the datastores it
		// is forbidden to reach.
		ns:    envOr("BOX_NAMESPACE", "hanzo-boxes"),
		image: envOr("BOX_IMAGE_REPO", "registry.hanzo.ai/hanzoai/box"),
		tag:   envOr("BOX_IMAGE_TAG", ""),
		port:  envOr("BOX_PORT", "8000"),
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// KUBECONFIG fallback for local/dev — identical to apps/platform's
		// newK8sClient. One way to reach the cluster, not two.
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		if cfg, err = cc.ClientConfig(); err != nil {
			p.initErr = fmt.Sprintf("no in-cluster config and no kubeconfig: %v", err)
			return p
		}
	}
	cfg.UserAgent = "hanzo-cloud/sandbox"
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.initErr = fmt.Sprintf("dynamic client: %v", err)
		return p
	}
	p.dyn = dyn
	return p
}

func (p *pool) ready() error {
	if p == nil || p.dyn == nil {
		if p != nil && p.initErr != "" {
			return fmt.Errorf("%s", p.initErr)
		}
		return fmt.Errorf("kubernetes client not configured")
	}
	return nil
}

// imageFor is the tag chain: base → dev → desktop, one image, three tags. The
// deployment pins the version; nothing here resolves `latest`, because an image
// decided by WHEN the pod started rather than by what was shipped is the bug
// cloud's own Dockerfile already documents at length.
func (p *pool) imageFor(class string) string {
	tag := p.tag
	if tag == "" {
		tag = envOr("BOX_IMAGE_TAG_"+strings.ToUpper(class), "")
	}
	if tag == "" {
		return p.image + ":" + class
	}
	return p.image + ":" + class + "-" + tag
}

// claim binds a warm pod to this box, and is the fast path: a label patch.
//
// A claim is racy by nature — two requests can see the same warm pod. The patch
// is the arbitration: it is conditional on the pod STILL carrying
// box-state=warm, so exactly one of the two wins and the loser tries the next
// candidate. That is why this is a patch with a precondition and not a
// read-then-write.
func (p *pool) claim(ctx context.Context, b Box) (string, error) {
	if err := p.ready(); err != nil {
		return "", err
	}
	pods := p.dyn.Resource(k8s.Pods).Namespace(p.ns)

	sel := fmt.Sprintf("%s=%s,%s=warm", labClass, b.Class, labState)
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: sel, Limit: 20})
	if err != nil {
		return "", fmt.Errorf("list warm pods: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, item := range list.Items {
		name := item.GetName()
		ip, _, _ := unstructuredString(item.Object, "status", "podIP")
		if ip == "" {
			continue // scheduled but not networked yet; not claimable
		}
		patch := fmt.Sprintf(
			`{"metadata":{"labels":{%q:"bound",%q:%q,%q:%q,%q:%q}}}`,
			labState, labOrg, sanitize(b.Org), labBox, b.ID, labProject, b.Project)
		if _, err := pods.Patch(ctx, name, k8stypes.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
				continue // someone else claimed it; take the next
			}
			return "", fmt.Errorf("claim %s: %w", name, err)
		}
		return "http://" + ip + ":" + p.port, nil
	}
	// An empty pool is a real, actionable condition — the Deployment's replica
	// count is too low, or every pod is bound. It is NOT silently papered over
	// with a cold create here: a create needs an image pull and a scheduler
	// cycle, and pretending that is a claim makes the pool's sizing invisible.
	return "", fmt.Errorf("no warm %s box available in %s (pool exhausted or not deployed)", b.Class, p.ns)
}

// release returns a pod to the pool by DELETING it. The Deployment behind the
// warm pool replaces it with a fresh one, which is the only honest reset: a pod
// that ran submitted code is not returned to the pool for the next tenant.
func (p *pool) release(ctx context.Context, b Box) error {
	if err := p.ready(); err != nil {
		return err
	}
	pods := p.dyn.Resource(k8s.Pods).Namespace(p.ns)
	sel := fmt.Sprintf("%s=%s", labBox, b.ID)
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return err
	}
	for _, item := range list.Items {
		if err := pods.Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// purge deletes the project VOLUME. Separate from release, and opt-in, because
// the volume holds the only copy of the checkout and the caches.
func (p *pool) purge(ctx context.Context, b Box) error {
	if err := p.ready(); err != nil {
		return err
	}
	if b.PVC == "" {
		return nil
	}
	err := p.dyn.Resource(k8s.Volumes).Namespace(p.ns).Delete(ctx, b.PVC, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func unstructuredString(obj map[string]any, path ...string) (string, bool, error) {
	cur := any(obj)
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur, ok = m[k]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
