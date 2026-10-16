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

func TestRulesPage(t *testing.T) {
	c, st := setup(t)
	c.login("admin", "admin-pass")
	f := url.Values{"csrf": {c.csrf("/rules")}, "inbox": {"1"}, "glob": {"*.gmail@*"}, "destination": {"Fwd@Example.com"}}
	if code, _, body := c.do("POST", "/rules", f); code != 200 || !strings.Contains(body, "Rule added") {
		t.Fatalf("create: %d", code)
	}
	list, _ := st.ListRules(context.Background())
	if len(list) != 1 || list[0].Destination != "fwd@example.com" || !list[0].Enabled {
		t.Fatalf("stored %+v", list)
	}
	_, _, body := c.do("GET", "/rules?test=b.gmail@example.test", nil)
	if !strings.Contains(body, "Routes to inbox <b>Default (real address b@gmail.com)</b>") || !strings.Contains(body, `<span class="mono">fwd@example.com</span>`) {
		t.Fatalf("test box wrong:\n%s", body[strings.Index(body, "Test an address"):])
	}
	_, _, body = c.do("GET", "/rules?test=plain@example.test", nil)
	if !strings.Contains(body, "No rule matches") {
		t.Fatal("non-matching address reported as forwarded")
	}
	c.do("POST", "/rules/"+itoa(list[0].ID), url.Values{"csrf": {c.csrf("/rules")}, "action": {"disable"}})
	if list, _ = st.ListRules(context.Background()); list[0].Enabled {
		t.Fatal("not disabled")
	}
}

func TestInboxAddressList(t *testing.T) {
	c, st := setup(t)
	c.login("admin", "admin-pass")
	f := url.Values{"csrf": {c.csrf("/admin/inboxes")}, "name": {"Inbox A"}, "slug": {"inbox-a"},
		"format": {"{local}.{name}"}, "tld": {"com"}, "addresses": {"B.gmail@example.test\r\n\r\nqa-*@example.test"}}
	if code, _, _ := c.do("POST", "/admin/inboxes", f); code != 200 {
		t.Fatalf("create: %d", code)
	}
	list, _ := st.ListInboxes(context.Background())
	var a *store.Inbox
	for _, in := range list {
		if in.Slug == "inbox-a" {
			a = in
		}
	}
	if a == nil || len(a.Addresses) != 2 || a.Addresses[0] != "b.gmail@example.test" {
		t.Fatalf("stored %+v", a)
	}
	f.Set("csrf", c.csrf("/admin/inboxes"))
	f.Set("slug", "inbox-b")
	f.Set("addresses", "not an address")
	if code, _, _ := c.do("POST", "/admin/inboxes", f); code != http.StatusBadRequest {
		t.Fatalf("bad address list accepted: %d", code)
	}
}

// A user limited to some inboxes cannot see, open or send from the others.
func TestUserInboxAccess(t *testing.T) {
	c, st := setup(t)
	ctx := context.Background()
	other := &store.Inbox{Name: "Other", Slug: "other", FormatTemplate: "{inbox}-{local}={domain}", AssumedTLD: "com"}
	if err := st.CreateInbox(ctx, other); err != nil {
		t.Fatal(err)
	}
	hidden := &store.Message{InboxID: 1, Direction: "in", Subject: "secret-default"}
	st.InsertMessage(ctx, hidden, []byte("Subject: secret-default\r\n\r\nx\r\n"), nil)
	shown := &store.Message{InboxID: other.ID, Direction: "in", Subject: "visible-other"}
	st.InsertMessage(ctx, shown, []byte("Subject: visible-other\r\n\r\ny\r\n"), nil)

	hash, _ := auth.HashPassword("user-password")
	u := &store.User{Username: "limited", PasswordHash: hash, Role: "user"}
	st.CreateUser(ctx, u)
	c.login("admin", "admin-pass")
	c.do("POST", "/admin/users/"+itoa(u.ID), url.Values{"csrf": {c.csrf("/admin/users")}, "action": {"inboxes"}, "inbox": {itoa(other.ID)}})
	if ids, _ := st.UserInboxIDs(ctx, u.ID); len(ids) != 1 || ids[0] != other.ID {
		t.Fatalf("access not saved: %v", ids)
	}

	lc := newClient(t, c.base)
	lc.login("limited", "user-password")
	if _, loc, _ := lc.do("GET", "/", nil); loc != "/inbox/"+itoa(other.ID) {
		t.Errorf("home redirects to %s", loc)
	}
	_, _, body := lc.do("GET", "/inbox/"+itoa(other.ID), nil)
	if !strings.Contains(body, "visible-other") || strings.Contains(body, ">Default<") {
		t.Error("allowed inbox page wrong or sidebar shows hidden inbox")
	}
	for _, path := range []string{"/inbox/1", "/inbox/1/rows", "/m/" + itoa(hidden.ID), "/m/" + itoa(hidden.ID) + "/raw", "/m/" + itoa(hidden.ID) + "/reply"} {
		if code, _, _ := lc.do("GET", path, nil); code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, code)
		}
	}
	if code, _, _ := lc.do("GET", "/m/"+itoa(shown.ID), nil); code != 200 {
		t.Errorf("allowed message: %d", code)
	}
	f := url.Values{"csrf": {lc.csrf("/compose")}, "inbox": {"1"}, "identity": {"b@gmail.com"}, "to": {"x@example.com"}}
	if _, _, body := lc.do("POST", "/compose", f); !strings.Contains(body, "do not have access to that inbox") {
		t.Error("compose from a hidden inbox not refused")
	}
	lc.do("POST", "/messages/bulk", url.Values{"csrf": {lc.csrf("/inbox/" + itoa(other.ID))}, "action": {"delete"}, "id": {itoa(hidden.ID)}, "back": {"/"}})
	if _, err := st.GetMessage(ctx, hidden.ID); err != nil {
		t.Error("restricted user deleted a message from a hidden inbox")
	}
	// Clearing the list gives access to everything again.
	c.do("POST", "/admin/users/"+itoa(u.ID), url.Values{"csrf": {c.csrf("/admin/users")}, "action": {"inboxes"}})
	if code, _, _ := lc.do("GET", "/inbox/1", nil); code != 200 {
		t.Errorf("after clearing access: %d", code)
	}
}

func TestRulesScopedToUserInboxes(t *testing.T) {
	c, st := setup(t)
	ctx := context.Background()
	other := &store.Inbox{Name: "Other", Slug: "other", FormatTemplate: "{inbox}-{local}={domain}"}
	st.CreateInbox(ctx, other)
	st.CreateRule(ctx, &store.ForwardRule{InboxID: 1, Destination: "default-dest@example.com", Enabled: true})
	theirs := &store.ForwardRule{InboxID: other.ID, Destination: "other-dest@example.com", Enabled: true}
	st.CreateRule(ctx, theirs)

	hash, _ := auth.HashPassword("user-password")
	u := &store.User{Username: "scoped", PasswordHash: hash, Role: "user"}
	st.CreateUser(ctx, u)
	st.SetUserInboxes(ctx, u.ID, []int64{other.ID})
	lc := newClient(t, c.base)
	lc.login("scoped", "user-password")

	_, _, body := lc.do("GET", "/rules", nil)
	if !strings.Contains(body, "other-dest@example.com") || strings.Contains(body, "default-dest@example.com") || strings.Contains(body, "Any inbox") {
		t.Fatal("rules page not scoped")
	}
	f := url.Values{"csrf": {lc.csrf("/rules")}, "inbox": {"1"}, "destination": {"x@example.com"}}
	if code, _, _ := lc.do("POST", "/rules", f); code != http.StatusBadRequest {
		t.Errorf("rule for hidden inbox: %d", code)
	}
	f.Set("csrf", lc.csrf("/rules"))
	f.Set("inbox", "0")
	if code, _, _ := lc.do("POST", "/rules", f); code != http.StatusBadRequest {
		t.Errorf("any-inbox rule by restricted user: %d", code)
	}
	f.Set("csrf", lc.csrf("/rules"))
	f.Set("inbox", itoa(other.ID))
	if code, _, _ := lc.do("POST", "/rules", f); code != 200 {
		t.Errorf("own rule: %d", code)
	}
	list, _ := st.ListRules(ctx)
	if len(list) != 3 {
		t.Fatalf("rules %d", len(list))
	}
	if code, _, _ := lc.do("POST", "/rules/"+itoa(list[0].ID), url.Values{"csrf": {lc.csrf("/rules")}, "action": {"delete"}}); code != http.StatusNotFound {
		t.Errorf("deleting a hidden inbox's rule: %d", code)
	}
}

func TestUnreadAndSentFilter(t *testing.T) {
	c, st := setup(t)
	ctx := context.Background()
	c.login("admin", "admin-pass")
	m := &store.Message{InboxID: 1, Direction: "in", Subject: "fresh"}
	st.InsertMessage(ctx, m, []byte("Subject: fresh\r\n\r\nx\r\n"), nil)

	unreadRow := `class="mail-row unread"` // the inbox holds only this message
	if _, _, body := c.do("GET", "/inbox/1", nil); !strings.Contains(body, unreadRow) || !strings.Contains(body, `<span class="count">1</span>`) {
		t.Fatal("new mail not shown as unread with a sidebar count")
	}
	c.do("GET", "/m/"+itoa(m.ID), nil)
	if _, _, body := c.do("GET", "/inbox/1", nil); strings.Contains(body, unreadRow) {
		t.Fatal("opened mail still unread")
	}
	c.do("POST", "/m/"+itoa(m.ID)+"/unread", url.Values{"csrf": {c.csrf("/inbox/1")}})
	if _, _, body := c.do("GET", "/inbox/1", nil); !strings.Contains(body, unreadRow) {
		t.Fatal("mark unread had no effect")
	}
	c.do("POST", "/inbox/1/read", url.Values{"csrf": {c.csrf("/inbox/1")}})
	if _, _, body := c.do("GET", "/inbox/1", nil); strings.Contains(body, unreadRow) {
		t.Fatal("mark all read had no effect")
	}

	other := &store.Inbox{Name: "Other", Slug: "other", FormatTemplate: "{inbox}-{local}={domain}"}
	st.CreateInbox(ctx, other)
	st.InsertMessage(ctx, &store.Message{InboxID: 1, Direction: "out", Subject: "from-default"}, []byte("Subject: a\r\n\r\n1\r\n"), nil)
	st.InsertMessage(ctx, &store.Message{InboxID: other.ID, Direction: "out", Subject: "from-other"}, []byte("Subject: b\r\n\r\n2\r\n"), nil)
	_, _, body := c.do("GET", "/sent?inbox="+itoa(other.ID), nil)
	if !strings.Contains(body, "from-other") || strings.Contains(body, "from-default") {
		t.Fatal("sent not filtered by inbox")
	}
	if _, _, body := c.do("GET", "/sent", nil); !strings.Contains(body, "from-other") || !strings.Contains(body, "from-default") {
		t.Fatal("all-inboxes sent view incomplete")
	}
}

func TestBulkActions(t *testing.T) {
	c, st := setup(t)
	ctx := context.Background()
	c.login("admin", "admin-pass")
	var ids []string
	for i := range 3 {
		m := &store.Message{InboxID: 1, Direction: "in", Subject: "bulk"}
		st.InsertMessage(ctx, m, []byte("Subject: b\r\n\r\n"+itoa(int64(i))+"\r\n"), nil)
		ids = append(ids, itoa(m.ID))
	}
	post := func(action string, sel ...string) {
		c.do("POST", "/messages/bulk", url.Values{"csrf": {c.csrf("/inbox/1")}, "action": {action}, "id": sel, "back": {"/inbox/1"}})
	}
	post("read", ids[0], ids[1])
	if _, _, body := c.do("GET", "/inbox/1", nil); strings.Count(body, `class="mail-row unread"`) != 1 {
		t.Fatalf("after bulk read: %d unread", strings.Count(body, `class="mail-row unread"`))
	}
	post("unread", ids[0])
	post("delete", ids[1], ids[2])
	list, _ := st.ListMessages(ctx, store.Filter{InboxID: 1})
	if len(list) != 1 || itoa(list[0].ID) != ids[0] {
		t.Fatalf("after delete: %+v", list)
	}
	if _, _, body := c.do("GET", "/inbox/1", nil); strings.Count(body, `class="mail-row unread"`) != 1 {
		t.Fatal("bulk unread lost")
	}
}
