package deploy

// One lookup for every /v1/deploy/applications/{name} operation, and a real sync.
//
// hanzo-git is a Hanzo CD Application, not an App CR. `get` found it through the
// CD fallback while resource-tree, syncwindows and the SuperAdmin writes only
// looked for App CRs and answered 404. These tests pin that every operation
// resolves a name the same way, and that sync requests the CD operation the
// cd.hanzo.ai button requests, for SuperAdmin only.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hanzoai/cloud/k8s"
)

const universe = "https://git.hanzo.ai/hanzo-inc/universe"

func hanzoGit() *unstructured.Unstructured {
	return cdApp("hanzo-git", universe, "672f1e2843", "OutOfSync", "Healthy", nil)
}

var (
	superHeaders  = map[string]string{"X-User-Id": "u-z", "X-User-IsAdmin": "true"}
	hanzoMember   = map[string]string{"X-User-Id": "u-z", "X-Org-Id": "hanzo"}
	acmeMember    = map[string]string{"X-User-Id": "u-a", "X-Org-Id": "acme"}
	superInHanzo  = map[string]string{"X-User-Id": "u-z", "X-User-IsAdmin": "true", "X-Org-Id": "hanzo"}
	readsOfAnApp  = []string{"", "/resource-tree", "/syncwindows", "/revisions/HEAD/metadata"}
	syncPathOfApp = "/v1/deploy/applications/hanzo-git/sync"
)

// Every read of an application answers for a CD Application exactly as `get` does,
// and refuses another tenant's identically.
func TestEveryOperationResolvesACDApplicationTheSameWay(t *testing.T) {
	s := fakeService(hanzoGit())
	for _, suffix := range readsOfAnApp {
		path := "/v1/deploy/applications/hanzo-git" + suffix
		for who, h := range map[string]map[string]string{"hanzo member": hanzoMember, "SuperAdmin": superHeaders, "SuperAdmin in hanzo": superInHanzo} {
			resp := getAs(t, s, path, h)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s as %s = %d, want 200", path, who, resp.StatusCode)
			}
		}
		resp := getAs(t, s, path, acmeMember)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s as another org = %d, want 404", path, resp.StatusCode)
		}
	}
}

// A SuperAdmin acting in an org resolves that org's bare name; a member does too.
func TestABareNameResolvesInTheActingOrg(t *testing.T) {
	s := fakeService(hanzoGit())
	for who, h := range map[string]map[string]string{"hanzo member": hanzoMember, "SuperAdmin in hanzo": superInHanzo} {
		resp := getAs(t, s, "/v1/deploy/applications/git/resource-tree", h)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("bare name as %s = %d, want 200", who, resp.StatusCode)
		}
	}
	resp := getAs(t, s, "/v1/deploy/applications/git/resource-tree", superHeaders)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bare name with no acting org = %d, want 404", resp.StatusCode)
	}
}

func TestSyncRequestsTheCDOperationForSuperAdminOnly(t *testing.T) {
	s := fakeService(hanzoGit())
	do := func(path string, h map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		for k, v := range h {
			req.Header.Set(k, v)
		}
		code, _ := probe(t, s, req)
		return code
	}
	operation := func() map[string]any {
		got, err := s.State.dyn.Resource(k8s.CDApplications).Namespace("hanzo-cd").Get(t.Context(), "hanzo-git", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		op, _, _ := unstructured.NestedMap(got.Object, "operation")
		return op
	}

	// hanzo/z is an org member, not SuperAdmin: refused, and nothing is written.
	if code := do(syncPathOfApp, hanzoMember); code != http.StatusForbidden {
		t.Fatalf("sync as hanzo member = %d, want 403", code)
	}
	if op := operation(); op != nil {
		t.Fatalf("a refused sync wrote an operation: %v", op)
	}

	// SuperAdmin acting in hanzo: the operation applies the compared commit, no prune.
	if code := do(syncPathOfApp, superInHanzo); code != http.StatusOK {
		t.Fatalf("sync as SuperAdmin = %d, want 200", code)
	}
	op := operation()
	sync, _ := op["sync"].(map[string]any)
	if sync["revision"] != "672f1e2843" || sync["prune"] != false {
		t.Fatalf("operation = %v, want sync of 672f1e2843 without prune", op)
	}
	if by, _ := op["initiatedBy"].(map[string]any); by["username"] != "u-z" {
		t.Fatalf("operation initiatedBy = %v, want the caller", op["initiatedBy"])
	}

	// A second request while the first is queued is a conflict, not a second write.
	if code := do(syncPathOfApp, superHeaders); code != http.StatusConflict {
		t.Fatalf("second sync = %d, want 409", code)
	}
	// An unknown application is a 404 for SuperAdmin.
	if code := do("/v1/deploy/applications/nope/sync", superHeaders); code != http.StatusNotFound {
		t.Fatalf("sync of unknown app = %d, want 404", code)
	}
}

// CD must have compared the Application with git before there is a commit to sync.
func TestSyncRefusesAnApplicationCDHasNotCompared(t *testing.T) {
	app := hanzoGit()
	unstructured.RemoveNestedField(app.Object, "status", "sync", "revision")
	s := fakeService(app)
	req := httptest.NewRequest(http.MethodPost, syncPathOfApp, nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("X-User-Id", "u-z")
	if code, body := probe(t, s, req); code != http.StatusConflict {
		t.Fatalf("sync = %d %s, want 409", code, body)
	}
}

// rollback was a mislabelled sync; it is gone, not renamed.
func TestRollbackIsNotServed(t *testing.T) {
	s := fakeService(hanzoGit())
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/applications/hanzo-git/rollback", nil)
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("X-User-Id", "u-z")
	if code, _ := probe(t, s, req); code != http.StatusNotFound {
		t.Fatalf("rollback = %d, want 404", code)
	}
}
