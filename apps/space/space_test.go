package space

// The three facts this surface is built on, each measured against the REAL mount
// and the store double rather than asserted about a helper:
//
//	one bucket per (org, space)   — spaces_test below, and every store call here
//	a drive is the first segment  — drives_test and files_test
//	a path capture is DECODED     — decode_test
//
// Every case reads WHAT THE STORE WAS ASKED, because that is where a wrong
// derivation shows: a handler that looked in the wrong bucket still answers 200.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/s3admin"
)

// ── the bucket derivation ───────────────────────────────────────────────────

// TestOneBucketPerOrgAndSpace is the isolation property stated as arithmetic: the
// bucket a space lives in is a function of the VALIDATED org and the space name,
// and two orgs asking for one space name never meet.
func TestOneBucketPerOrgAndSpace(t *testing.T) {
	for _, tc := range []struct{ org, space string }{
		{"acme", "hq"},
		{"acme", "finance"},
		{"globex", "hq"},
		{"a", "b"},
	} {
		if got, want := bucket(tc.org, tc.space), s3admin.BucketName(tc.org, tc.space); got != want {
			t.Errorf("bucket(%q,%q) = %q, want s3admin.BucketName = %q", tc.org, tc.space, got, want)
		}
		if pfx := spacePrefix(tc.org); !strings.HasPrefix(bucket(tc.org, tc.space), pfx) {
			t.Errorf("bucket(%q,%q) = %q does not begin with the org prefix %q — the listing could not find it",
				tc.org, tc.space, bucket(tc.org, tc.space), pfx)
		}
		name, ok := spaceOf(tc.org, bucket(tc.org, tc.space))
		if !ok || name != tc.space {
			t.Errorf("spaceOf(%q, own bucket) = %q,%v want %q,true", tc.org, name, ok, tc.space)
		}
	}
	if bucket("acme", "hq") == bucket("globex", "hq") {
		t.Fatal("two orgs share one bucket for one space name — a cross-org leak")
	}
	// The boundary fold is the one this hash exists to close: "acme"+"my-space"
	// and "acme-my"+"space" must not name one bucket.
	if bucket("acme", "my-space") == bucket("acme-my", "space") {
		t.Fatal("the org/space boundary folds — two orgs name one bucket")
	}
	if _, ok := spaceOf("globex", bucket("acme", "hq")); ok {
		t.Fatal("spaceOf(globex, acme's bucket) = owned — a cross-org leak")
	}
}

// TestListingSpacesSeesOnlyTheCallersOwn drives the live route: the store answers
// with three orgs' buckets and the caller is told about their own, by the name
// they created it under. Another org's is not refused but ABSENT, so the listing
// cannot be used to learn that a name is taken elsewhere.
func TestListingSpacesSeesOnlyTheCallersOwn(t *testing.T) {
	st := newStore()
	st.buckets = []string{
		bucket("acme", "hq"),
		bucket("acme", "finance"),
		bucket("globex", "hq"),
		"some-hand-made-bucket",
	}
	app := mount(t, st)

	code, body := send(t, app, http.MethodGet, "/v1/space/spaces", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("list spaces = %d (%s), want 200", code, body)
	}
	var got struct {
		Spaces []struct{ Name string } `json:"spaces"`
		Total  int                     `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	var names []string
	for _, s := range got.Spaces {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "hq,finance" || got.Total != 2 {
		t.Fatalf("spaces = %v (total %d), want [hq finance] — globex's and the hand-made bucket are not this org's",
			names, got.Total)
	}
}

// TestCreatingASpaceMakesTheOrgsOwnBucket. The physical name is derived from the
// validated org, so a body field cannot redirect the create into another org's
// namespace.
func TestCreatingASpaceMakesTheOrgsOwnBucket(t *testing.T) {
	st := newStore()
	app := mount(t, st)

	code, body := send(t, app, http.MethodPost, "/v1/space/spaces", "acme",
		`{"name":"hq","org":"globex"}`)
	if code != http.StatusCreated {
		t.Fatalf("create space = %d (%s), want 201", code, body)
	}
	made := st.asked(http.MethodPut)
	if len(made) != 1 {
		t.Fatalf("store saw %d PUTs, want exactly the one MakeBucket: %+v", len(made), made)
	}
	if want := "/" + bucket("acme", "hq") + "/"; made[0].Path != want {
		t.Fatalf("MakeBucket at %q, want %q — the org in the body must not choose the bucket", made[0].Path, want)
	}
}

// ── the drive derivation ────────────────────────────────────────────────────

// TestListingDrivesIsListingTheRootFolder. A drive is the FIRST KEY SEGMENT, so
// the drives of a space are exactly the folders at its root — one mechanism, not
// two, which is what makes a drives table impossible to disagree with.
func TestListingDrivesIsListingTheRootFolder(t *testing.T) {
	b := bucket("acme", "hq")
	st := newStore()
	st.keys[b] = []object{
		{Key: "docs/"}, // an empty drive: its marker alone
		{Key: "media/2019/summer/a.jpg", Size: 12},
		{Key: "media/notes.txt", Size: 3},
		{Key: "stray.txt", Size: 1}, // a file at the space root, in no drive
	}
	app := mount(t, st)

	code, body := send(t, app, http.MethodGet, "/v1/space/hq/drives", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("list drives = %d (%s), want 200", code, body)
	}
	var got struct {
		Space  string                  `json:"space"`
		Drives []struct{ Name string } `json:"drives"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	var names []string
	for _, d := range got.Drives {
		names = append(names, d.Name)
	}
	if got.Space != "hq" || strings.Join(names, ",") != "docs,media" {
		t.Fatalf("drives = %v in space %q, want [docs media] in hq — a bare key at the root is a file, not a drive",
			names, got.Space)
	}
	// And it asked the ONE bucket the space lives in, at the root, folded on "/".
	asked := st.asked(http.MethodGet)
	last := asked[len(asked)-1]
	if strings.TrimSuffix(last.Path, "/") != "/"+b {
		t.Fatalf("listed %q, want the space's own bucket %q", last.Path, "/"+b)
	}
	q, _ := url.ParseQuery(last.Query)
	if q.Get("prefix") != "" || q.Get("delimiter") != "/" {
		t.Fatalf("listed prefix=%q delimiter=%q, want the root folded on a separator", q.Get("prefix"), q.Get("delimiter"))
	}
}

// TestCreatingADriveWritesItsMarker. A drive is a prefix, so making one writes a
// zero-byte marker at "<name>/" — the only thing that makes an EMPTY drive
// visible to a listing with no other key to find. Creating one that is already
// there is 409.
func TestCreatingADriveWritesItsMarker(t *testing.T) {
	b := bucket("acme", "hq")
	st := newStore()
	app := mount(t, st)

	code, body := send(t, app, http.MethodPost, "/v1/space/hq/drives", "acme", `{"name":"docs"}`)
	if code != http.StatusCreated {
		t.Fatalf("create drive = %d (%s), want 201", code, body)
	}
	put := st.asked(http.MethodPut)
	if len(put) != 1 || put[0].Path != "/"+b+"/docs/" {
		t.Fatalf("wrote %+v, want one marker at %q", put, "/"+b+"/docs/")
	}

	st.keys[b] = []object{{Key: "docs/"}}
	if code, body = send(t, app, http.MethodPost, "/v1/space/hq/drives", "acme", `{"name":"docs"}`); code != http.StatusConflict {
		t.Fatalf("re-create drive = %d (%s), want 409", code, body)
	}
}

// TestDeletingADriveRefusesToCascade. An empty drive goes; one holding files is
// 409, because deleting an org's files behind a single call is not a thing this
// surface does silently; one that is not there is 404.
func TestDeletingADriveRefusesToCascade(t *testing.T) {
	b := bucket("acme", "hq")
	for _, tc := range []struct {
		name string
		keys []object
		want int
		gone string
	}{
		{"empty drive", []object{{Key: "docs/"}}, http.StatusNoContent, "/" + b + "/docs/"},
		{"holds a file", []object{{Key: "docs/"}, {Key: "docs/a.txt", Size: 1}}, http.StatusConflict, ""},
		{"no marker but files", []object{{Key: "docs/a.txt", Size: 1}}, http.StatusConflict, ""},
		{"not there", nil, http.StatusNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			st.keys[b] = tc.keys
			app := mount(t, st)

			code, body := send(t, app, http.MethodDelete, "/v1/space/hq/drives/docs", "acme", "")
			if code != tc.want {
				t.Fatalf("delete drive = %d (%s), want %d", code, body, tc.want)
			}
			del := st.asked(http.MethodDelete)
			if tc.gone == "" {
				if len(del) != 0 {
					t.Fatalf("store was asked to delete %+v for a refused call — nothing may be touched", del)
				}
				return
			}
			if len(del) != 1 || del[0].Path != tc.gone {
				t.Fatalf("deleted %+v, want exactly %q", del, tc.gone)
			}
		})
	}
}

// ── files, folders, and the key a file operation addresses ──────────────────

// TestFilesAreKeysUnderTheDrive: the prefix a listing reads is the drive's own
// segment plus the folder asked for, and names come back RELATIVE to it — which
// is what makes a folder emergent from the names rather than a row somewhere.
func TestFilesAreKeysUnderTheDrive(t *testing.T) {
	b := bucket("acme", "hq")
	for _, tc := range []struct {
		name       string
		url        string
		wantPrefix string
		wantDelim  string
		wantNames  []string
	}{
		{
			name: "the drive root, folded", url: "/v1/space/hq/drives/media/files",
			wantPrefix: "media/", wantDelim: "/", wantNames: []string{"notes.txt", "2019/"},
		},
		{
			name: "one folder down", url: "/v1/space/hq/drives/media/files?folder=2019",
			wantPrefix: "media/2019/", wantDelim: "/", wantNames: []string{"summer/"},
		},
		{
			name: "recursive is flat", url: "/v1/space/hq/drives/media/files?recursive=true",
			wantPrefix: "media/", wantDelim: "", wantNames: []string{"2019/summer/a.jpg", "notes.txt"},
		},
		{
			name: "a folder that escapes is the drive root", url: "/v1/space/hq/drives/media/files?folder=../../etc",
			wantPrefix: "media/", wantDelim: "/", wantNames: []string{"notes.txt", "2019/"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			st.keys[b] = []object{
				{Key: "media/"},
				{Key: "media/notes.txt", Size: 3},
				{Key: "media/2019/summer/a.jpg", Size: 12},
			}
			app := mount(t, st)

			code, body := send(t, app, http.MethodGet, tc.url, "acme", "")
			if code != http.StatusOK {
				t.Fatalf("list files = %d (%s), want 200", code, body)
			}
			var got struct {
				Files []struct {
					Name   string `json:"name"`
					Folder bool   `json:"isFolder"`
				} `json:"files"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("body %s: %v", body, err)
			}
			var names []string
			for _, f := range got.Files {
				names = append(names, f.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Fatalf("files = %v, want %v (names are RELATIVE to the folder)", names, tc.wantNames)
			}
			asked := st.asked(http.MethodGet)
			last := asked[len(asked)-1]
			if strings.TrimSuffix(last.Path, "/") != "/"+b {
				t.Fatalf("listed %q, want the space's own bucket %q", last.Path, "/"+b)
			}
			q, _ := url.ParseQuery(last.Query)
			if q.Get("prefix") != tc.wantPrefix || q.Get("delimiter") != tc.wantDelim {
				t.Fatalf("listed prefix=%q delimiter=%q, want %q / %q",
					q.Get("prefix"), q.Get("delimiter"), tc.wantPrefix, tc.wantDelim)
			}
		})
	}
}

// TestSignedURLsNameTheOneBucketAndTheOneKey. Bytes never pass through this
// process: read and write mint a URL against the PUBLIC host, and the signature
// covers exactly one space, one drive and one file.
func TestSignedURLsNameTheOneBucketAndTheOneKey(t *testing.T) {
	b := bucket("acme", "hq")
	for _, tc := range []struct {
		method     string
		wantMethod string
	}{
		{http.MethodGet, http.MethodGet},
		{http.MethodPut, http.MethodPut},
	} {
		t.Run(tc.method, func(t *testing.T) {
			st := newStore()
			app := mount(t, st)

			code, body := send(t, app, tc.method, "/v1/space/hq/drives/media/files/2019/summer/a.jpg", "acme", "")
			if code != http.StatusOK {
				t.Fatalf("%s file = %d (%s), want 200", tc.method, code, body)
			}
			var got struct {
				URL, Method, File string
				Expiry            int64 `json:"expiresIn"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("body %s: %v", body, err)
			}
			if got.Method != tc.wantMethod || got.File != "2019/summer/a.jpg" || got.Expiry != int64(presignTTL.Seconds()) {
				t.Fatalf("minted %+v, want method %s, file 2019/summer/a.jpg, expiry %d",
					got, tc.wantMethod, int64(presignTTL.Seconds()))
			}
			u, err := url.Parse(got.URL)
			if err != nil {
				t.Fatalf("minted an unparseable URL %q: %v", got.URL, err)
			}
			if want := "/" + b + "/media/2019/summer/a.jpg"; u.Path != want {
				t.Fatalf("signed for %q, want %q — the drive is the first key segment", u.Path, want)
			}
			if len(st.seen()) != 0 {
				t.Fatalf("minting reached the store %d times, want 0 — a presign is a signature, not a call", len(st.seen()))
			}
		})
	}
}

// TestDeletingAFileRemovesExactlyIt. One file and never a prefix: the key is the
// drive's segment plus the cleaned name, and nothing beneath it is touched.
func TestDeletingAFileRemovesExactlyIt(t *testing.T) {
	b := bucket("acme", "hq")
	st := newStore()
	app := mount(t, st)

	code, body := send(t, app, http.MethodDelete, "/v1/space/hq/drives/media/files/2019/summer/a.jpg", "acme", "")
	if code != http.StatusNoContent {
		t.Fatalf("delete file = %d (%s), want 204", code, body)
	}
	del := st.asked(http.MethodDelete)
	if len(del) != 1 || del[0].Path != "/"+b+"/media/2019/summer/a.jpg" {
		t.Fatalf("deleted %+v, want exactly %q", del, "/"+b+"/media/2019/summer/a.jpg")
	}
}
