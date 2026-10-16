package web

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/events"
	"mailcatcher/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func setup(t *testing.T) (*client, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := Bootstrap(context.Background(), st, "admin-pass"); err != nil {
		t.Fatal(err)
	}
	s, err := New(st, events.NewBroker(), "example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(SecurityHeaders(mux))
	t.Cleanup(srv.Close)
	return newClient(t, srv.URL), st
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) do(method, path string, form url.Values) (int, string, string) {
	c.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, c.base+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Location"), string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (c *client) csrf(path string) string {
	_, _, body := c.do("GET", path, nil)
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		c.t.Fatalf("no csrf token on %s", path)
	}
	return m[1]
}

func (c *client) login(user, pw string) {
	c.t.Helper()
	code, loc, _ := c.do("POST", "/login", url.Values{"username": {user}, "password": {pw}})
	if code != http.StatusSeeOther || loc != "/" {
		c.t.Fatalf("login %s: %d %s", user, code, loc)
	}
}

func TestLoginFlow(t *testing.T) {
	c, _ := setup(t)
	if code, loc, _ := c.do("GET", "/inbox/1", nil); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?next=") {
		t.Fatalf("anonymous: %d %s", code, loc)
	}
	if code, _, _ := c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", code)
	}
	code, loc, _ := c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"admin-pass"}, "next": {"//evil.example"}})
	if code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("open redirect not blocked: %d %s", code, loc)
	}
	if code, _, body := c.do("GET", "/inbox/1", nil); code != 200 || !strings.Contains(body, "Default") {
		t.Fatalf("inbox: %d", code)
	}
	if code, _, _ := c.do("POST", "/logout", url.Values{"csrf": {"bogus"}}); code != http.StatusForbidden {
		t.Fatalf("logout without valid csrf: %d", code)
	}
	tok := c.csrf("/inbox/1")
	c.do("POST", "/logout", url.Values{"csrf": {tok}})
	if code, _, _ := c.do("GET", "/inbox/1", nil); code != http.StatusSeeOther {
		t.Fatalf("still logged in after logout: %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	c, _ := setup(t)
	for range 10 {
		c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"wrong"}})
	}
	if code, _, _ := c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"admin-pass"}}); code != http.StatusTooManyRequests {
		t.Fatalf("not rate limited: %d", code)
	}
}

func TestCreatedUserMustChangePassword(t *testing.T) {
	c, st := setup(t)
	c.login("admin", "admin-pass")
	_, _, body := c.do("POST", "/admin/users", url.Values{"csrf": {c.csrf("/admin/users")}, "username": {"user1"}, "role": {"user"}})
	m := regexp.MustCompile(`must be changed at first login\): ([A-Za-z0-9_-]+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no temp password in: %s", body)
	}

	u := newClient(t, c.base)
	u.login("user1", m[1])
	if code, loc, _ := u.do("GET", "/inbox/1", nil); code != http.StatusSeeOther || loc != "/account/password" {
		t.Fatalf("not forced to change password: %d %s", code, loc)
	}
	tok := u.csrf("/account/password")
	u.do("POST", "/account/password", url.Values{"csrf": {tok}, "current": {m[1]}, "new": {"new-password-1"}, "confirm": {"new-password-1"}})
	if code, _, _ := u.do("GET", "/inbox/1", nil); code != 200 {
		t.Fatalf("after change: %d", code)
	}
	if code, _, _ := u.do("GET", "/admin/users", nil); code != http.StatusForbidden {
		t.Fatalf("user reached admin page: %d", code)
	}
	saved, _ := st.GetUserByName(context.Background(), "user1")
	if ok, _ := auth.CheckPassword(saved.PasswordHash, "new-password-1"); !ok || saved.MustChangePassword {
		t.Fatalf("password not saved: %+v", saved)
	}
}

func TestLastAdminProtected(t *testing.T) {
	c, st := setup(t)
	c.login("admin", "admin-pass")
	admin, _ := st.GetUserByName(context.Background(), "admin")
	path := "/admin/users/" + itoa(admin.ID)
	for _, f := range []url.Values{
		{"action": {"disable"}},
		{"action": {"role"}, "role": {"user"}},
	} {
		f.Set("csrf", c.csrf("/admin/users"))
		if code, _, _ := c.do("POST", path, f); code != http.StatusBadRequest {
			t.Errorf("%v: %d", f, code)
		}
	}
	if u, _ := st.GetUser(context.Background(), admin.ID); u.Disabled || !u.IsAdmin() {
		t.Fatalf("last admin changed: %+v", u)
	}
}

func TestDisabledUserLoggedOut(t *testing.T) {
	c, st := setup(t)
	hash, _ := auth.HashPassword("user-password")
	plainUser := &store.User{Username: "user2", PasswordHash: hash, Role: "user"}
	st.CreateUser(context.Background(), plainUser)
	u := newClient(t, c.base)
	u.login("user2", "user-password")

	c.login("admin", "admin-pass")
	c.do("POST", "/admin/users/"+itoa(plainUser.ID), url.Values{"csrf": {c.csrf("/admin/users")}, "action": {"disable"}})
	if code, _, _ := u.do("GET", "/inbox/1", nil); code != http.StatusSeeOther {
		t.Fatalf("disabled user still in: %d", code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestInboxAdmin(t *testing.T) {
	c, st := setup(t)
	ctx := context.Background()
	raw := []byte("From: a@x.test\r\nTo: team1-b=gmail.com@example.test\r\nSubject: s\r\n\r\nhi\r\n")
	m := &store.Message{InboxID: 1, Direction: "in", EnvelopeTo: "team1-b=gmail.com@example.test"}
	if _, err := st.InsertMessage(ctx, m, raw, nil); err != nil {
		t.Fatal(err)
	}
	c.login("admin", "admin-pass")

	bad := url.Values{"csrf": {c.csrf("/admin/inboxes")}, "name": {"T"}, "slug": {"team1"}, "format": {"{local}"}}
	if code, _, body := c.do("POST", "/admin/inboxes", bad); code != http.StatusBadRequest || !strings.Contains(body, "needs {domain} or {name}") {
		t.Fatalf("invalid format accepted: %d", code)
	}
	good := url.Values{"csrf": {c.csrf("/admin/inboxes")}, "name": {"Team 1"}, "slug": {"team1"}, "format": {"{inbox}-{local}={domain}"}}
	if code, _, body := c.do("POST", "/admin/inboxes", good); code != 200 || !strings.Contains(body, "Created inbox Team 1") {
		t.Fatalf("create: %d", code)
	}

	_, _, body := c.do("GET", "/admin/format-preview?format=%7Binbox%7D-%7Blocal%7D%3D%7Bdomain%7D&slug=team1&address=b@gmail.com", nil)
	if !strings.Contains(body, `"address":"team1-b=gmail.com@example.test"`) || !strings.Contains(body, `"decoded":"b@gmail.com"`) {
		t.Fatalf("preview: %s", body)
	}

	c.do("POST", "/admin/inboxes/reroute", url.Values{"csrf": {c.csrf("/admin/inboxes")}})
	got, _ := st.GetMessage(ctx, m.ID)
	if got.InboxID == 1 || got.DecodedTo != "b@gmail.com" {
		t.Fatalf("not rerouted: inbox %d decoded %q", got.InboxID, got.DecodedTo)
	}
}
