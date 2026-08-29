package space

// The percent-decoding trap, measured at every path capture this surface reads.
//
// The router runs with fiber's UnescapePath off, so a captured segment arrives
// EXACTLY as it was sent, while every client generated from this API
// percent-encodes a path parameter. Undecoded, two spellings of one file address
// two different keys and both answer success — a delete sent by any SDK removes a
// key nobody stored and reports 204 while the file it named survives.
// apps/framework/spacepath_test.go is the same trap one subsystem over, where a
// DocType named "Sales Invoice" could be created and never reached again.
//
// Each case reads the key the STORE was asked for, or the key the signature
// covers, because that is the only place a missing decode is visible.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestPathCapturesAreDecoded drives the real routes with encoded captures and
// requires the store to be asked about the decoded ones.
func TestPathCapturesAreDecoded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		url     string
		wantKey string // the key, relative to the bucket, the store must be asked for
		org     string
		space   string
	}{
		{
			name:   "an encoded separator in a file name is a folder, not a literal",
			method: http.MethodDelete, org: "acme", space: "hq",
			url:     "/v1/space/hq/drives/media/files/2019%2Fsummer%2Fa.jpg",
			wantKey: "media/2019/summer/a.jpg",
		},
		{
			name:   "an encoded space in a file name is a space",
			method: http.MethodDelete, org: "acme", space: "hq",
			url:     "/v1/space/hq/drives/media/files/notes%20and%20drafts.txt",
			wantKey: "media/notes and drafts.txt",
		},
		{
			name:   "an encoded letter in the SPACE segment names the same space",
			method: http.MethodDelete, org: "acme", space: "hq",
			url:     "/v1/space/h%71/drives/media/files/a.txt",
			wantKey: "media/a.txt",
		},
		{
			name:   "an encoded letter in the DRIVE segment names the same drive",
			method: http.MethodDelete, org: "acme", space: "hq",
			url:     "/v1/space/hq/drives/med%69a/files/a.txt",
			wantKey: "media/a.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			app := mount(t, st)

			code, body := send(t, app, tc.method, tc.url, tc.org, "")
			if code != http.StatusNoContent {
				t.Fatalf("%s %s = %d (%s), want 204", tc.method, tc.url, code, body)
			}
			del := st.asked(http.MethodDelete)
			want := "/" + bucket(tc.org, tc.space) + "/" + tc.wantKey
			if len(del) != 1 || del[0].Path != want {
				t.Fatalf("store was asked %+v, want exactly %q — the capture reached the store undecoded", del, want)
			}
		})
	}
}

// TestASignedURLCoversTheDecodedKey is the same property one plane over: the
// signature is what the caller then presents to the store, so an undecoded key
// there mints a URL for a file that does not exist and reports success.
func TestASignedURLCoversTheDecodedKey(t *testing.T) {
	st := newStore()
	app := mount(t, st)

	code, body := send(t, app, http.MethodGet,
		"/v1/space/hq/drives/media/files/2019%2Fsummer%2Fa%20b.jpg", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("read file = %d (%s), want 200", code, body)
	}
	var got struct{ URL, File string }
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if got.File != "2019/summer/a b.jpg" {
		t.Fatalf("answered file %q, want %q", got.File, "2019/summer/a b.jpg")
	}
	u, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("minted an unparseable URL %q: %v", got.URL, err)
	}
	if want := "/" + bucket("acme", "hq") + "/media/2019/summer/a b.jpg"; u.Path != want {
		t.Fatalf("signed for %q, want %q", u.Path, want)
	}
}

// TestAnUndecodableAddressIsRefused. "%zz" is not a name carrying a stray
// percent, it is an address that does not decode, and the one decoder says so
// rather than handing the store something arbitrary.
//
// It is asked of the DECODER and not of a route because a request carrying an
// invalid escape cannot be BUILT here: net/url refuses to parse one, so
// httptest.NewRequest panics before a router is reached. The refusal is still
// worth pinning — a decoder that fell back to the raw string on a bad escape
// would turn every one of the cases above from a decoded key into a literal one.
func TestAnUndecodableAddressIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
		want string
	}{
		{"a malformed escape", "a%zz.txt", false, ""},
		{"a truncated escape", "a%2", false, ""},
		{"an encoded separator", "2019%2Fsummer", true, "2019/summer"},
		{"an encoded space", "notes%20and%20drafts.txt", true, "notes and drafts.txt"},
		{"a plain name", "a.txt", true, "a.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decode(tc.raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("decode(%q) = %q,%v want %q,%v", tc.raw, got, ok, tc.want, tc.ok)
			}
			// remainder and named are the two readers, and neither may salvage what
			// the decoder refused.
			if _, ok := remainder(tc.raw); ok != tc.ok {
				t.Fatalf("remainder(%q) accepted %v, want %v", tc.raw, ok, tc.ok)
			}
			if _, ok := named(tc.raw); !tc.ok && ok {
				t.Fatalf("named(%q) accepted an address that does not decode", tc.raw)
			}
		})
	}
}

// TestAFileNameCannotEscapeItsDrive. A cleaned name may carry separators — that
// is what makes a folder — and may not climb out of the drive it was addressed
// under, whichever way it is spelled.
func TestAFileNameCannotEscapeItsDrive(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"traversal", "/v1/space/hq/drives/media/files/../../etc/passwd"},
		{"encoded traversal", "/v1/space/hq/drives/media/files/..%2F..%2Fetc%2Fpasswd"},
		{"a bare folder marker", "/v1/space/hq/drives/media/files/2019%2F"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			app := mount(t, st)

			code, _ := send(t, app, http.MethodDelete, tc.url, "acme", "")
			if code == http.StatusNoContent {
				t.Fatalf("delete %s = 204 — a name that escapes its drive was served", tc.url)
			}
			for _, c := range st.asked(http.MethodDelete) {
				if !hasPrefix(c.Path, "/"+bucket("acme", "hq")+"/media/") {
					t.Fatalf("store was asked to delete %q, which is outside the drive", c.Path)
				}
			}
		})
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
