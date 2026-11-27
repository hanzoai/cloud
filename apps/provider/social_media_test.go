package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// swap replaces a package variable for one test. Production never reassigns the
// addresses and clients this is used on; a test points them at a stand-in.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// quick makes every look and every retry immediate.
func quick(t *testing.T) {
	t.Helper()
	swap(t, &pollWaits, []time.Duration{0, 0, 0, 0})
	swap(t, &postWaits, []time.Duration{0, 0})
}

// open lets the media and server clients reach a loopback stand-in, which the
// public-address client would refuse.
func open(t *testing.T) {
	t.Helper()
	swap(t, &mediaClient, http.DefaultClient)
	swap(t, &blueskyClient, http.DefaultClient)
	swap(t, &mastodonClient, http.DefaultClient)
}

func TestMediaIsSortedIntoPhotosAndVideos(t *testing.T) {
	images, videos, err := splitMedia([]string{
		"https://cdn.example.com/a.JPG?w=1", "https://cdn.example.com/b.mp4", "https://cdn.example.com/c.webp",
	})
	if err != nil || len(images) != 2 || len(videos) != 1 || videos[0] != "https://cdn.example.com/b.mp4" {
		t.Fatalf("images=%v videos=%v err=%v", images, videos, err)
	}
	for _, bad := range []string{"https://cdn.example.com/file", "ftp://cdn.example.com/a.png", "not an address", "https:///a.png"} {
		if _, _, err := splitMedia([]string{bad}); err == nil {
			t.Errorf("%q was sorted", bad)
		}
	}
}

// A tenant's media address is fetched through the public-address client: a
// private address is refused, whatever the host says it is.
func TestMediaFetchRefusesAPrivateAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	t.Cleanup(srv.Close)
	_, err := fetchMedia(context.Background(), srv.URL+"/a.png", 1<<20)
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("a loopback address was fetched: %v", err)
	}
}

func TestMediaFetchBoundsTheFile(t *testing.T) {
	open(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	t.Cleanup(srv.Close)
	if _, _, err := readMedia(context.Background(), srv.URL+"/a.png", 50); !errors.Is(err, errMediaTooLarge) {
		t.Fatalf("a file over the limit was read: %v", err)
	}
	f, b, err := readMedia(context.Background(), srv.URL+"/a.png", 200)
	if err != nil || len(b) != 100 || f.Type != "image/png" || f.Name != "a.png" {
		t.Fatalf("file=%+v len=%d err=%v", f, len(b), err)
	}
}

func TestSettleLooksUntilDoneAndGivesUp(t *testing.T) {
	quick(t)
	looks := 0
	if err := settle(context.Background(), func(context.Context) (bool, error) { looks++; return looks == 3, nil }); err != nil || looks != 3 {
		t.Fatalf("looks=%d err=%v", looks, err)
	}
	if err := settle(context.Background(), func(context.Context) (bool, error) { return false, nil }); !errors.Is(err, errProcessing) {
		t.Fatalf("a file that never finished = %v", err)
	}
	boom := errors.New("refused")
	if err := settle(context.Background(), func(context.Context) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatalf("a refusal = %v", err)
	}
}

// A transport failure never reports the address, which can carry a credential.
func TestACallFailureDoesNotReportTheAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()
	req, _ := http.NewRequest(http.MethodGet, addr+"/?access_token=SECRET-TOKEN", nil)
	_, err := call(context.Background(), req, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("the credential was reported: %v", err)
	}
}
