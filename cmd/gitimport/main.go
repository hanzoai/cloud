package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/git/forgeimport"
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/forge"
	_ "github.com/hanzoai/cloud/internal/driver/postgres"
	_ "github.com/hanzoai/cloud/internal/driver/s3"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/object"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
)

func main() {
	var (
		host    = flag.String("forge", "https://git.hanzo.ai", "the forge")
		list    = flag.Bool("list", false, "list every repository the credential can see and register it")
		repos   = flag.String("repos", "", "owner/name,owner/name: register these (instead of -list)")
		workers = flag.Int("workers", 4, "repositories copied at once")
		maxPack = flag.Int64("max-pack-mb", 2048, "largest pack taken in one fetch, in MiB")
		rate    = flag.Int64("rate-mbps", 0, "bandwidth bound across all workers, in MiB/s (0 is none)")
		resync  = flag.Bool("resync", false, "compare repositories already done with the forge and fetch the difference")
		retry   = flag.Bool("retry", false, "put failed repositories back to pending")
		sample  = flag.Float64("sample", 0.05, "fraction of repositories over 64 MiB whose clone is checked")
		noClone = flag.Bool("no-clone-check", false, "check refs and counts only")
		bucket  = flag.String("bucket", "hanzo-git", "the pack bucket")
		dir     = flag.String("dir", "", "data directory when there is no CLOUD_SQL_DSN")
		status  = flag.Bool("status", false, "print the registry and exit")
	)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(ctx, log, *host, *list, *repos, *workers, *maxPack<<20, *rate<<20, *resync, *retry, *sample, *noClone, *bucket, *dir, *status); err != nil {
		fmt.Fprintln(os.Stderr, "gitimport:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, host string, list bool, repos string, workers int, maxPack, rate int64,
	resync, retry bool, sample float64, noClone bool, bucket, dir string, status bool) error {
	if dir == "" && locus.DSN() == "" {
		return fmt.Errorf("set CLOUD_SQL_DSN, or -dir for files")
	}
	regdb, err := locus.Open(namespace.System(), "gitimport", dir)
	if err != nil {
		return err
	}
	reg, err := forgeimport.OpenRegistry(ctx, regdb)
	if err != nil {
		return err
	}
	if status {
		return report(ctx, reg)
	}
	// The packs go to the deployment's shared store: an import onto this machine's
	// disk would be a copy nothing else reads.
	if !object.ConfiguredShared() {
		return fmt.Errorf("S3_ADMIN_ENDPOINT, S3_ADMIN_ACCESS_KEY and S3_ADMIN_SECRET_KEY name the pack store")
	}
	if err := object.Check(); err != nil {
		return fmt.Errorf("the pack store: %w", err)
	}
	blobs := objects.Bucket(object.Default(), bucket)
	var mu sync.Mutex
	stores := map[string]*objects.Store{}
	token := strings.TrimSpace(os.Getenv("FORGE_TOKEN"))
	base := strings.TrimSuffix(host, "/")
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	im, err := forgeimport.New(forgeimport.Config{
		Registry: reg,
		Stores: func(org string) (*objects.Store, error) {
			mu.Lock()
			defer mu.Unlock()
			if s, ok := stores[org]; ok {
				return s, nil
			}
			ns, err := principal.OrgNamespace(org, "")
			if err != nil {
				return nil, err
			}
			db, err := locus.Open(ns, "objects", dir)
			if err != nil {
				return nil, err
			}
			s, err := objects.Open(db, blobs, ns)
			if err != nil {
				return nil, err
			}
			stores[org] = s
			return s, nil
		},
		// The org whose store holds a namespace is the closed table's (hanzoai is
		// hanzo's); a namespace it does not name is its own org, which asserts nothing
		// about tenancy: this only copies.
		Org: func(owner string) string {
			if o, err := forge.Org(owner); err == nil {
				return o
			}
			return forgeimport.DefaultOrg(owner)
		},
		Remote: func(owner, repo string) *forgeimport.Remote {
			r := &forgeimport.Remote{URL: base + "/" + owner + "/" + repo + ".git", Throttle: throttle}
			if token != "" {
				r.User, r.Password = "hanzo", token
			}
			r.HTTP = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 5 * time.Minute, MaxIdleConnsPerHost: 16}}
			return r
		},
		Workers: workers,
		MaxPack: maxPack,
		Resync:  resync,
		Verify:  forgeimport.Verify{NoClone: noClone, Sample: sample},
		Holder:  holder(),
		Log:     log,
	})
	if err != nil {
		return err
	}
	throttle = forgeimport.NewThrottle(rate)

	var items []forgeimport.Item
	switch {
	case repos != "":
		for _, r := range strings.Split(repos, ",") {
			owner, name, ok := strings.Cut(strings.TrimSpace(r), "/")
			if !ok {
				return fmt.Errorf("-repos wants owner/name, got %q", r)
			}
			items = append(items, forgeimport.Item{Owner: owner, Repo: strings.TrimSuffix(name, ".git")})
		}
	case list:
		if items, err = forgeimport.Discover(ctx, nil, base, token); err != nil {
			return err
		}
		log.Info("listed", "repositories", len(items))
	}
	if err := im.Register(ctx, items); err != nil {
		return err
	}
	if retry {
		all, err := reg.All(ctx)
		if err != nil {
			return err
		}
		for _, x := range all {
			if x.State == forgeimport.Failed {
				_ = reg.Reset(ctx, x.Owner, x.Repo)
			}
		}
	}
	started := time.Now()
	sum, err := im.Run(ctx)
	log.Info("finished", "done", sum.Done, "failed", sum.Failed, "unchanged", sum.Unchanged,
		"bytes", sum.Bytes, "took", time.Since(started).Round(time.Second).String())
	if err != nil {
		return err
	}
	return report(ctx, reg)
}

// throttle is shared by every remote the config builds; it is set before the first run.
var throttle *forgeimport.Throttle

func holder() string {
	h, _ := os.Hostname()
	return fmt.Sprintf("%s/%d", h, os.Getpid())
}

func report(ctx context.Context, reg *forgeimport.Registry) error {
	all, err := reg.All(ctx)
	if err != nil {
		return err
	}
	by := map[string]int{}
	var bytes int64
	for _, x := range all {
		by[x.State]++
		bytes += x.Bytes
		if x.State == forgeimport.Failed {
			fmt.Printf("failed  %s/%s (attempt %d): %s\n", x.Owner, x.Repo, x.Attempts, x.Error)
		}
	}
	fmt.Printf("repositories %d: pending %d, running %d, done %d, failed %d; %d bytes stored\n",
		len(all), by[forgeimport.Pending], by[forgeimport.Running], by[forgeimport.Done], by[forgeimport.Failed], bytes)
	return nil
}
