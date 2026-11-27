package platform

// The extra tags a build writes beside its image.
//
// A caller that publishes a version under several names — the immutable
// `sha-<short>`, `v1.2.3`, `1.2.3`, `latest` — used to push one and copy the rest
// onto whatever digest it later found in the registry, holding a runner while it
// waited. Written by the door in the same export, the names are one digest by
// construction and the caller can go on the 202.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	tagSHA   = "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2"
	tagImage = "ghcr.io/hanzoai/app:sha-0097126"
)

// argvOutput is the value of the argv's `--output` flag.
func argvOutput(cmd []any) string {
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] == "--output" {
			s, _ := cmd[i+1].(string)
			return s
		}
	}
	return ""
}

// exportedNames reads an --output value the way buildctl does — one CSV record of
// key=value fields — and returns the image exporter's names, split on the commas
// the exporter splits them on. Any field the build did not mean to write fails the
// test, because that is what an injected exporter attribute looks like.
func exportedNames(t *testing.T, output string) []string {
	t.Helper()
	fields, err := csv.NewReader(strings.NewReader(output)).Read()
	if err != nil {
		t.Fatalf("buildctl would refuse --output %q: %v", output, err)
	}
	var names []string
	for _, f := range fields {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "name":
			names = strings.Split(v, ",")
		case "type", "push", "compression", "force-compression", "oci-mediatypes":
		default:
			t.Fatalf("--output %q carries an attribute the build did not write: %q", output, f)
		}
	}
	return names
}

// jobCommand is the argv of one of a build's Jobs.
func jobCommand(t *testing.T, k *k8sClient, name string) []any {
	t.Helper()
	job, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Job %s: %v", name, err)
	}
	return podSpec(job.Object)["containers"].([]any)[0].(map[string]any)["command"].([]any)
}

func decodeBuild(t *testing.T, body []byte) runnerBuildResp {
	t.Helper()
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	return resp
}

func TestTagsRideTheOneExport(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app, store := runnerAppOn(t, k)
		code, body := postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/app", "sha": tagSHA, "image": tagImage,
			"platforms": []string{"linux/amd64"},
			"tags":      []string{"v1.2.3", "1.2.3", "latest", "v1.2.3", "sha-0097126"}})
		if code != http.StatusAccepted {
			t.Fatalf("want 202, got %d (%s)", code, body)
		}
		resp := decodeBuild(t, body)

		// The echo is the promise the caller acts on, so it is exactly what will be
		// written: the repeat and the image's own tag are gone, the order kept.
		want := []string{"v1.2.3", "1.2.3", "latest"}
		if !slices.Equal(resp.Tags, want) {
			t.Fatalf("echoed tags %v, want %v", resp.Tags, want)
		}

		cmd := jobCommand(t, k, buildJobName(resp.BuildJobID, ""))
		got := exportedNames(t, argvOutput(cmd))
		wantNames := []string{tagImage, "ghcr.io/hanzoai/app:v1.2.3", "ghcr.io/hanzoai/app:1.2.3", "ghcr.io/hanzoai/app:latest"}
		if !slices.Equal(got, wantNames) {
			t.Fatalf("the Job exports %v, want %v", got, wantNames)
		}
		// A tag is another name for the image, not another claim about it.
		if v, _ := argvOpt(cmd, "build-arg:VERSION="); v != "sha-0097126" {
			t.Errorf("VERSION = %q, want the image's own tag", v)
		}

		b, err := store.GetBuild(context.Background(), "hanzo", resp.BuildJobID)
		if err != nil {
			t.Fatalf("GetBuild: %v", err)
		}
		if !slices.Equal(b.Tags, want) {
			t.Fatalf("recorded tags %v, want %v", b.Tags, want)
		}
	})
}

func TestAFanOutWritesItsTagsFromTheIndexAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app, store := runnerAppOn(t, k)
		code, body := postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/app", "sha": tagSHA, "image": tagImage,
			"tags": []string{"v1.2.3", "latest"}})
		if code != http.StatusAccepted {
			t.Fatalf("want 202, got %d (%s)", code, body)
		}
		resp := decodeBuild(t, body)

		// Each half publishes at its own architecture's tag and nothing else, the argv
		// it had before tags existed. A tag on a half would name one architecture under
		// a name that claims both.
		for _, arch := range []string{"amd64", "arm64"} {
			out := argvOutput(jobCommand(t, k, buildJobName(resp.BuildJobID, arch)))
			if want := "type=image,name=" + tagImage + "-" + arch + ",push=true,compression=zstd,force-compression=true,oci-mediatypes=true"; out != want {
				t.Errorf("%s half --output\n got %s\nwant %s", arch, out, want)
			}
		}

		b, err := store.GetBuild(context.Background(), "hanzo", resp.BuildJobID)
		if err != nil {
			t.Fatalf("GetBuild: %v", err)
		}
		markAllJobsSucceeded(t, k, k.buildNS)
		s := &cloud.Service[state]{Log: luxlog.New("test"), State: state{k8s: k}}
		if _, _, _, err := directBuildResult(s, context.Background(), b); err != nil {
			t.Fatalf("directBuildResult: %v", err)
		}
		cmd := jobCommand(t, k, buildJobName(b.ID, indexJobSuffix))
		want := []string{tagImage, "ghcr.io/hanzoai/app:v1.2.3", "ghcr.io/hanzoai/app:latest"}
		if got := exportedNames(t, argvOutput(cmd)); !slices.Equal(got, want) {
			t.Fatalf("the index exports %v, want %v", got, want)
		}
		// The halves are found by the image's tag; the extra tags only name the index.
		if src, _ := argvOpt(cmd, "build-arg:SRC="); src != tagImage {
			t.Errorf("SRC = %q, want %q", src, tagImage)
		}
	})
}

func TestBadTagsAreRefusedBeforeAnyJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		nine := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"}
		for name, c := range map[string]struct {
			image string
			tags  []string
			want  string
		}{
			"a comma splits the name list":   {tagImage, []string{"a,b"}, "is not an OCI tag"},
			"an = starts an attribute":       {tagImage, []string{"x=y"}, "is not an OCI tag"},
			"a quote closes the field":       {tagImage, []string{`"q`}, "is not an OCI tag"},
			"129 characters":                 {tagImage, []string{strings.Repeat("a", 129)}, "is not an OCI tag"},
			"a leading dot":                  {tagImage, []string{".v1"}, "is not an OCI tag"},
			"a leading dash":                 {tagImage, []string{"-v1"}, "is not an OCI tag"},
			"whitespace":                     {tagImage, []string{"v1 2"}, "is not an OCI tag"},
			"a newline":                      {tagImage, []string{"v1\nname=evil"}, "is not an OCI tag"},
			"a slash names another repo":     {tagImage, []string{"a/b"}, "is not an OCI tag"},
			"an @ names a digest":            {tagImage, []string{"v1@sha256:" + strings.Repeat("0", 64)}, "is not an OCI tag"},
			"empty":                          {tagImage, []string{""}, "is not an OCI tag"},
			"more than eight":                {tagImage, nine, "at most 8 tags"},
			"a digest image has no tag slot": {"ghcr.io/hanzoai/app@sha256:" + strings.Repeat("0", 64), []string{"v1"}, "not a digest"},
		} {
			t.Run(name, func(t *testing.T) {
				k := fakeK8s()
				app, _ := runnerAppOn(t, k)
				code, body := postRunner(t, app, map[string]any{
					"repo": "https://github.com/hanzoai/app", "sha": tagSHA, "image": c.image,
					"platforms": []string{"linux/amd64"}, "tags": c.tags})
				if code != http.StatusBadRequest || !strings.Contains(string(body), c.want) {
					t.Fatalf("want 400 naming %q, got %d (%s)", c.want, code, body)
				}
				jobs, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
				if err != nil {
					t.Fatalf("list Jobs: %v", err)
				}
				if len(jobs.Items) != 0 {
					t.Fatalf("%d Jobs created for a refused build", len(jobs.Items))
				}
			})
		}

		// The grammar's own edge is still a tag: a refusal that refuses the longest
		// legal tag is an outage, not a check.
		app := runnerApp(t)
		if code, body := postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/app", "sha": tagSHA, "image": tagImage,
			"platforms": []string{"linux/amd64"}, "tags": []string{strings.Repeat("a", 128), "_x", "1.0.0-rc.1"}}); code != http.StatusAccepted {
			t.Fatalf("legal tags: want 202, got %d (%s)", code, body)
		}
	})
}

func TestTheArtifactLaneRefusesTags(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, body := postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/cli", "sha": tagSHA, "tag": "v1.2.3", "tags": []string{"latest"},
			"binaries": []map[string]any{{"name": "cli", "run": "make dist", "out": "dist/*"}}})
		if code != http.StatusBadRequest || !strings.Contains(string(body), "tags") {
			t.Fatalf("tags on the artifact lane: want 400 naming tags, got %d (%s)", code, body)
		}
	})
}

func TestATagPublishesExactlyWhereItsImageDoes(t *testing.T) {
	// Impossible by construction — a tag is a ref of the image's own repository —
	// and asserted anyway, because the push credential is derived from the image
	// alone and has to cover every name the export writes.
	orgs := []string{"admin", "Hanzo"}
	for org := range orgRegistryNamespaces {
		orgs = append(orgs, org)
	}
	for _, image := range []string{
		"ghcr.io/hanzoai/app:sha-0097126",
		"ghcr.io/hanzo-inc/cloud:v8.5.186",
		"oci.hanzo.ai/hanzoai/sandbox:1.0.1-dev",
		"ghcr.io/luxfi/node:v1",
		"ghcr.io/zooai/node",
		"ghcr.io/hanzoai/cloud:v1",
	} {
		tags, err := imageTags(image, []string{"latest", "v9.9.9", "1.0.0-rc.1", "_x"})
		if err != nil {
			t.Fatalf("imageTags(%q): %v", image, err)
		}
		secret, _ := buildPushSecret(image)
		for _, tg := range tags {
			ref := tagRef(image, tg)
			if repository(ref) != repository(image) {
				t.Errorf("%s: tag %q left the repository: %s", image, tg, ref)
			}
			if imageAllowed(ref) != imageAllowed(image) || imageExcluded(ref) != imageExcluded(image) {
				t.Errorf("%s: tag ref %s is judged differently from its image", image, ref)
			}
			for _, org := range orgs {
				if imageInOrgRegistry(ref, org) != imageInOrgRegistry(image, org) {
					t.Errorf("%s: org %q owns tag ref %s differently from its image", image, org, ref)
				}
			}
			if s, _ := buildPushSecret(ref); s != secret {
				t.Errorf("%s: tag ref %s needs credential %q, image has %q", image, ref, s, secret)
			}
		}
	}
	// An image with no tag is `latest`, so a `latest` tag beside it is itself.
	if tags, _ := imageTags("ghcr.io/zooai/node", []string{"latest"}); len(tags) != 0 {
		t.Errorf("latest beside an untagged image survived as %v", tags)
	}
}

func TestEveryChokePointRefusesAHostileTag(t *testing.T) {
	// build() refuses these first; the k8s layer refuses them again because a
	// stored row and a direct caller reach it without passing through build().
	ctx := context.Background()
	k := fakeK8s()
	if _, err := k.launchDirectBuild(ctx, estate, "hanzo", "", "https://github.com/hanzoai/app", tagSHA, tagImage,
		"Dockerfile", "", "", "bld_hostile1", nil, []string{"linux/amd64"}, "x,registry.insecure=true"); err == nil {
		t.Error("launchDirectBuild accepted a tag carrying an exporter attribute")
	}
	if _, err := k.launchIndexBuild(ctx, "hanzo", tagImage, "bld_hostile2",
		[]string{"linux/amd64", "linux/arm64"}, `v1",push=false`); err == nil {
		t.Error("launchIndexBuild accepted a stored tag carrying an exporter attribute")
	}
	if _, err := k.launchIndexBuild(ctx, "hanzo", tagImage+",registry.insecure=true", "bld_hostile3",
		[]string{"linux/amd64", "linux/arm64"}, "v1"); err == nil {
		t.Error("launchIndexBuild accepted a stored image carrying an exporter attribute")
	}
	jobs, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("%d Jobs created from refused input", len(jobs.Items))
	}
}

func TestOneNameIsTheArgvItAlwaysWas(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// Every build in the fleet writes one name. Its argv is the one buildctl has
		// always been handed; the quoting that several names need never reaches it.
		build := buildFrontendCmdRev(estate, "https://git.example/x.git#main", "Dockerfile", "ghcr.io/hanzoai/x:v1", "ghcr.io/hanzoai/x:v1", "", nil)
		if got, want := argvOutput(build), "type=image,name=ghcr.io/hanzoai/x:v1,push=true,compression=zstd,force-compression=true,oci-mediatypes=true"; got != want {
			t.Errorf("build --output\n got %s\nwant %s", got, want)
		}
		index := indexCmd("ghcr.io/hanzoai/x:v1", []string{"linux/amd64", "linux/arm64"})
		if got, want := argvOutput(index), "type=image,name=ghcr.io/hanzoai/x:v1,push=true,oci-mediatypes=true"; got != want {
			t.Errorf("index --output\n got %s\nwant %s", got, want)
		}

		// Through the door: tags that dedupe to nothing are no tags at all — the same
		// argv as a request that never named any, and no promise in the answer.
		k := fakeK8s()
		app, _ := runnerAppOn(t, k)
		argv := func(tags []string) ([]any, []byte) {
			req := map[string]any{"repo": "https://github.com/hanzoai/app", "sha": tagSHA, "image": tagImage,
				"dockerfile": "Dockerfile", "platforms": []string{"linux/amd64"}}
			if tags != nil {
				req["tags"] = tags
			}
			code, body := postRunner(t, app, req)
			if code != http.StatusAccepted {
				t.Fatalf("want 202, got %d (%s)", code, body)
			}
			return jobCommand(t, k, buildJobName(decodeBuild(t, body).BuildJobID, "")), body
		}
		plain, _ := argv(nil)
		same, body := argv([]string{"sha-0097126", "sha-0097126"})
		if !reflect.DeepEqual(plain, same) {
			t.Fatalf("a request whose tags all dedupe away changed the argv\n got %v\nwant %v", same, plain)
		}
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if _, ok := raw["tags"]; ok {
			t.Fatalf("the answer promises tags it will not write: %s", body)
		}
	})
}

func TestTheStoreKeepsABuildsTags(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store, err := openStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		defer func() { _ = store.Close() }()
		ctx := context.Background()
		want := []string{"v1.2.3", "1.2.3", "latest"}
		for _, b := range []Build{
			{ID: "bld_tags", Org: "hanzo", Status: "queued", Image: tagImage, Platforms: []string{"linux/amd64", "linux/arm64"}, Tags: want, CreatedAt: 1, UpdatedAt: 1},
			{ID: "bld_none", Org: "hanzo", Status: "queued", Image: tagImage, CreatedAt: 1, UpdatedAt: 1},
		} {
			if err := store.InsertBuild(ctx, b); err != nil {
				t.Fatalf("InsertBuild %s: %v", b.ID, err)
			}
		}
		got, err := store.GetBuild(ctx, "hanzo", "bld_tags")
		if err != nil {
			t.Fatalf("GetBuild: %v", err)
		}
		if !slices.Equal(got.Tags, want) || len(got.Platforms) != 2 {
			t.Fatalf("read back tags %v platforms %v, want %v and two platforms", got.Tags, got.Platforms, want)
		}
		// UpdateBuild writes the outcome and must leave what the index Job reads alone.
		got.Status = "building"
		if err := store.UpdateBuild(ctx, got); err != nil {
			t.Fatalf("UpdateBuild: %v", err)
		}
		if again, _ := store.GetBuild(ctx, "hanzo", "bld_tags"); !slices.Equal(again.Tags, want) {
			t.Fatalf("an update dropped the tags: %v", again.Tags)
		}
		if none, _ := store.GetBuild(ctx, "hanzo", "bld_none"); len(none.Tags) != 0 {
			t.Fatalf("a build that named no tags read back as %v", none.Tags)
		}
	})
}
