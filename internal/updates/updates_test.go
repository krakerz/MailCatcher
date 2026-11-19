package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func newChecker(url, current, file string) *Checker {
	c := New(current, file, time.Hour)
	c.URL = url
	return c
}

func serve(status int, body, etag string, seen *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = append(*seen, r.Header.Get("If-None-Match"))
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/vnd.github+json" {
			http.Error(w, "bad headers", 400)
			return
		}
		if etag != "" && r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

const rel = `{"tag_name":"v0.6.0","html_url":"https://example.test/r","published_at":"2026-01-02T00:00:00Z"}`

func TestNewerRelease(t *testing.T) {
	srv := serve(200, rel, `"a"`, nil)
	defer srv.Close()
	c := newChecker(srv.URL, "0.5.0", "")
	c.Check(context.Background())
	v := c.Snapshot()
	if !v.Available() || v.Latest != "0.6.0" || v.URL != "https://example.test/r" || v.Err != "" {
		t.Fatalf("unexpected view %+v", v)
	}
}

func TestSameVersion(t *testing.T) {
	srv := serve(200, rel, "", nil)
	defer srv.Close()
	c := newChecker(srv.URL, "0.6.0", "")
	c.Check(context.Background())
	if c.Snapshot().Available() {
		t.Fatal("same version must not be available")
	}
}

func TestNotModifiedKeepsResult(t *testing.T) {
	var seen []string
	srv := serve(200, rel, `"a"`, &seen)
	defer srv.Close()
	c := newChecker(srv.URL, "0.5.0", "")
	c.Check(context.Background())
	c.Check(context.Background())
	v := c.Snapshot()
	if len(seen) != 2 || seen[0] != "" || seen[1] != `"a"` {
		t.Fatalf("If-None-Match sequence %q", seen)
	}
	if !v.Available() || v.Err != "" {
		t.Fatalf("304 must keep the result: %+v", v)
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{404, "", "no published release found (HTTP 404)"},
		{403, "", "GitHub refused the request (HTTP 403, rate limit?)"},
		{500, "", "GitHub answered HTTP 500"},
		{200, `{"tag_name":"nightly"}`, `unrecognised release tag "nightly"`},
	} {
		srv := serve(tc.status, tc.body, "", nil)
		c := newChecker(srv.URL, "0.5.0", "")
		c.Check(context.Background())
		if got := c.Snapshot().Err; got != tc.want {
			t.Errorf("status %d: err %q, want %q", tc.status, got, tc.want)
		}
		srv.Close()
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	srv := serve(200, rel, `"a"`, nil)
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "updates.json")
	newChecker(srv.URL, "0.5.0", file).Check(context.Background())
	v := newChecker(srv.URL, "0.5.0", file).Snapshot()
	if !v.Available() || v.Latest != "0.6.0" {
		t.Fatalf("state lost: %+v", v)
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"0.10.0", "0.9.0", true},
		{"0.9.0", "0.10.0", false},
		{"1.0", "1.0.0", false},
		{"1.0.0", "1.0", false},
		{"1.0.0", "1.0.0-rc1", true},
		{"1.0.0-rc1", "1.0.0", false},
		{"1.0.0-rc2", "1.0.0-rc1", true},
		{"v1.1.0", "1.0.0", true},
		{"V1.0.0", "v1.0.0", false},
		{"junk", "1.0.0", false},
	} {
		if got := Newer(tc.latest, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}
