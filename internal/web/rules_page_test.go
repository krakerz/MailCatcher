package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/store"
)

func TestRuleGlobWildcardCap(t *testing.T) {
	c, st := setup(t)
	c.login("admin", "admin-pass")
	f := url.Values{"csrf": {c.csrf("/rules")}, "inbox": {"0"}, "destination": {"x@example.com"}, "glob": {"a*a*a*a*b"}}
	code, _, body := c.do("POST", "/rules", f)
	if code != 400 || !strings.Contains(body, "at most 3 * wildcards") {
		t.Errorf("4-star glob: %d", code)
	}
	if list, _ := st.ListRules(context.Background()); len(list) != 0 {
		t.Errorf("stored %d rules", len(list))
	}
	f.Set("csrf", c.csrf("/rules"))
	f.Set("glob", "a*a*a*b")
	if code, _, _ := c.do("POST", "/rules", f); code != 303 {
		t.Errorf("3-star glob: %d", code)
	}
}

func TestRuleTesterHidesDecodedAddress(t *testing.T) {
	c, st := setup(t)
	ctx := context.Background()
	other := &store.Inbox{Name: "Other", Slug: "other", FormatTemplate: "{inbox}-{local}={domain}"}
	st.CreateInbox(ctx, other)
	hash, _ := auth.HashPassword("user-password")
	u := &store.User{Username: "scoped", PasswordHash: hash, Role: "user"}
	st.CreateUser(ctx, u)
	st.SetUserInboxes(ctx, u.ID, []int64{1})
	probe := "/rules?test=" + url.QueryEscape("other-bob=foo.com@example.test")

	lc := newClient(t, c.base)
	lc.login("scoped", "user-password")
	_, _, body := lc.do("GET", probe, nil)
	if !strings.Contains(body, "an inbox you cannot open") || strings.Contains(body, "bob@foo.com") {
		t.Error("restricted user sees the hidden inbox's decoded address")
	}

	c.login("admin", "admin-pass")
	_, _, body = c.do("GET", probe, nil)
	if !strings.Contains(body, "bob@foo.com") {
		t.Error("admin lost the decoded address")
	}
}
