// Command index fills the code index that /v1/code and /v1/code/lsp read from.
//
// THE INDEX IS A PUSH: cloud never clones. /v1/code/index takes a repo label and
// the files themselves, which is what makes one index able to hold repositories
// from anywhere without cloud holding a credential for any of them. It also means
// nothing is indexed until something posts, and until today nothing did — the
// endpoint had no caller in the fleet outside its own tests, so a search that
// spans every repo spanned none.
//
// WHY A SWEEP AND NOT A HOOK, TO START. Per-push indexing is the steady state and
// this is built to become that: the server skips unchanged files by content hash,
// so posting a whole tree costs about what posting the diff costs, and one repo's
// sweep IS the per-push job. A sweep first because a hook only ever indexes what
// changes after it is installed, and the question being asked is about everything
// that already exists.
//
//	index                 # every repo the forge lists
//	index hanzoai/cloud   # named repos only
//
// Env: FORGE (host, default git.hanzo.ai), FORGE_TOKEN, API (default
// https://api.hanzo.ai), HANZO_API_KEY, WORKERS (default 8).
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/internal/environ"
)

// The server's own bounds, restated so a batch is split here rather than
// rejected there: 20000 files and 1 GiB per call, 1 MiB per file.
const (
	maxFiles = 20000
	maxBytes = 1 << 30
	maxFile  = 1 << 20
)

// skipDir names directories whose contents are not this repository's code. They
// are someone else's source (vendored, installed) or this repo's output, and both
// answer questions about the wrong thing: a hit in node_modules tells you what a
// dependency does, when you asked what WE do.
var skipDir = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".next": true, "out": true,
	"coverage": true, "__pycache__": true, ".venv": true, "testdata": true,
}

// skipExt drops files that are text but not prose a person reads: generated
// bundles, source maps, lockfiles. They are enormous, they change constantly, and
// nobody has ever wanted a symbol from one.
var skipExt = map[string]bool{
	".map": true, ".min.js": true, ".min.css": true, ".lock": true,
	".snap": true, ".pb.go": true, ".sum": true,
}

type file struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type indexBody struct {
	Repo  string `json:"repo"`
	Files []file `json:"files"`
	Prune bool   `json:"prune,omitempty"`
}

var client = &http.Client{Timeout: 5 * time.Minute}

// repos asks the forge what exists. Paged, because the answer is hundreds and a
// single page would silently index a prefix of the fleet and report success.
func repos(host, token string) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		url := fmt.Sprintf("https://%s/v1/repos/search?limit=50&page=%d", host, page)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Data []struct {
				FullName string `json:"full_name"`
				Empty    bool   `json:"empty"`
				Archived bool   `json:"archived"`
			} `json:"data"`
		}
		err = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(body.Data) == 0 {
			return out, nil
		}
		for _, r := range body.Data {
			// An empty repo has no tree to fetch; an archived one is history, and
			// indexing it would answer "how do we do X" with code nobody may edit.
			if !r.Empty && !r.Archived {
				out = append(out, r.FullName)
			}
		}
	}
}

// tree downloads one repo's default branch and returns the files worth indexing.
//
// A tarball rather than a clone: one request, no working copy, and no git on the
// machine that runs this. The archive endpoint follows the default branch, so the
// question of WHICH ref is answered by the forge rather than guessed here.
func tree(host, token, full string) ([]file, error) {
	url := fmt.Sprintf("https://%s/%s/archive/HEAD.tar.gz", host, full)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive: http %d", res.StatusCode)
	}
	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var files []file
	var total int64
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || h.Size > maxFile {
			continue
		}
		// The archive roots everything under a single directory named for the
		// repo; strip it so a path reads the way it does in a checkout.
		name := h.Name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || skip(name) {
			continue
		}
		buf, err := io.ReadAll(io.LimitReader(tr, maxFile))
		if err != nil {
			return nil, err
		}
		// BINARY IS DROPPED HERE, not by extension. An extension list is a guess
		// about content; invalid UTF-8 is the content saying so. This also catches
		// the minified blob that forgot to be called .min.js.
		if !utf8.Valid(buf) || bytes.IndexByte(buf, 0) >= 0 {
			continue
		}
		if len(files) >= maxFiles || total+int64(len(buf)) > maxBytes {
			break
		}
		total += int64(len(buf))
		files = append(files, file{Path: name, Content: string(buf)})
	}
	return files, nil
}

func skip(name string) bool {
	for seg := range strings.SplitSeq(path.Dir(name), "/") {
		if skipDir[seg] {
			return true
		}
	}
	base := path.Base(name)
	for ext := range skipExt {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// push sends one repo's whole tree. prune is ON: this call is the full state of
// the repo, so a file deleted upstream must leave the index too — otherwise a
// search keeps answering with code that no longer exists, which is worse than not
// finding it.
func push(api, key, repo string, files []file) error {
	body, err := json.Marshal(indexBody{Repo: repo, Files: files, Prune: true})
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodPost, api+"/v1/code/index", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 400))
		return fmt.Errorf("index: http %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func main() {
	var (
		host  = environ.Or("FORGE", "git.hanzo.ai")
		token = os.Getenv("FORGE_TOKEN")
		api   = environ.Or("API", "https://api.hanzo.ai")
		key   = os.Getenv("HANZO_API_KEY")
	)
	if key == "" {
		fmt.Fprintln(os.Stderr, "HANZO_API_KEY is required — the index is per-org and the key is what says which")
		os.Exit(2)
	}

	list := os.Args[1:]
	if len(list) == 0 {
		var err error
		if list, err = repos(host, token); err != nil {
			fmt.Fprintf(os.Stderr, "list repos: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("indexing %d repos from %s\n", len(list), host)

	workers := 8
	if n := environ.Or("WORKERS", ""); n != "" {
		fmt.Sscanf(n, "%d", &workers)
	}
	// Fan out over repos rather than within one: each repo is an independent
	// download plus one post, and the slow part is the network both times.
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, failed int

	for i := 0; i < workers; i++ {
		wg.Go(func() {
			for full := range jobs {
				files, err := tree(host, token, full)
				if err == nil && len(files) > 0 {
					err = push(api, key, full, files)
				}
				mu.Lock()
				switch {
				case err != nil:
					failed++
					// REPORTED, never counted as done. A sweep that hides the repos
					// it could not read reports full coverage of a partial index.
					fmt.Printf("  FAIL %-40s %v\n", full, err)
				case len(files) == 0:
					fmt.Printf("  skip %-40s no indexable files\n", full)
				default:
					ok++
					fmt.Printf("  ok   %-40s %d files\n", full, len(files))
				}
				mu.Unlock()
			}
		})
	}
	for _, r := range list {
		jobs <- r
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("indexed %d, failed %d, of %d\n", ok, failed, len(list))
	if failed > 0 {
		os.Exit(1)
	}
}
