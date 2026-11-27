package host

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/sdk/kit"
)

// A resolved Site is read again after the handler returns — the object client
// uses the bucket name when it fetches the body — so it must still say what the
// project app answered after this process has made other plane calls. The reply
// is decoded from a pooled frame; a Site that still pointed into it would name
// whichever site the NEXT reply carried.
func TestAResolvedSiteSurvivesLaterPlaneCalls(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	app := zip.New(zip.Config{AppName: projectApp})
	app.Post("/sites/resolve", func(_ context.Context, in *sites.PlaneSiteIn) (*sites.PlaneSite, error) {
		return &sites.PlaneSite{Found: true, Org: "org-" + in.Slug, Slug: in.Slug, Bucket: "bucket-" + in.Slug, Prefix: "org-" + in.Slug + "/" + in.Slug, Status: "live"}, nil
	}, zip.WithOperationID("sites_resolve"))
	path := zip.SocketPath(projectApp)
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 400 {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx := context.Background()
	kept := make([]sites.Site, 16)
	for i := range kept {
		s, ok, err := planeResolver{}.Resolve(ctx, fmt.Sprintf("site%02d", i))
		if err != nil || !ok {
			t.Fatalf("resolve site%02d: ok=%v err=%v", i, ok, err)
		}
		kept[i] = s
	}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = planeResolver{}.Resolve(ctx, fmt.Sprintf("other%02d", i))
		}()
	}
	wg.Wait()

	for i, s := range kept {
		slug := fmt.Sprintf("site%02d", i)
		if s.Slug != slug || s.Bucket != "bucket-"+slug || s.Prefix != "org-"+slug+"/"+slug || s.Org != "org-"+slug {
			t.Fatalf("site %s now reads %+v", slug, s)
		}
	}
}
