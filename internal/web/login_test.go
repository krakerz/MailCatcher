package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLoginOversizedBodyRefused(t *testing.T) {
	c, _ := setup(t)
	code, _, _ := c.do("POST", "/login", url.Values{
		"username": {"admin"}, "password": {strings.Repeat("x", 1<<20)},
	})
	if code < 400 || code >= 500 {
		t.Fatalf("1 MB login body: %d, want 4xx", code)
	}
	if code, loc, _ := c.do("GET", "/", nil); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login") {
		t.Fatalf("session was created: %d %s", code, loc)
	}
}

func TestLoginLongUsernameIsPlainFailure(t *testing.T) {
	c, _ := setup(t)
	code, _, body := c.do("POST", "/login", url.Values{
		"username": {strings.Repeat("a", 500)}, "password": {"whatever"},
	})
	if code != http.StatusUnauthorized || !strings.Contains(body, "Wrong username or password.") {
		t.Fatalf("got %d\n%s", code, body)
	}
}
