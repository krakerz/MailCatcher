package address

import (
	"testing"

	"mailcatcher/internal/store"
)

func TestRoute(t *testing.T) {
	inboxes := []*store.Inbox{
		{ID: 1, Slug: "default", FormatTemplate: "{local}.{name}", AssumedTLD: "com", IsDefault: true},
		{ID: 2, Slug: "team1", FormatTemplate: "{inbox}-{local}={domain}", Priority: 0},
		{ID: 3, Slug: "other", Domain: "other.test", FormatTemplate: "{local}.{name}", AssumedTLD: "com"},
		{ID: 4, Slug: "broken", FormatTemplate: "{nope}"},
	}
	cases := []struct {
		rcpt    string
		inbox   int64
		decoded string
	}{
		{"b.gmail@main.test", 1, "b@gmail.com"},
		{"team1-b=gmail.com@main.test", 2, "b@gmail.com"},
		{"team2-b@main.test", 1, ""}, // unknown marker → default inbox, nothing decodes
		{"b.gmail@other.test", 3, "b@gmail.com"},
		{"plain@main.test", 1, ""},
		{"b.gmail@unknown.test", 1, ""}, // no inbox for that domain → default
		{"garbage", 1, ""},
	}
	for _, c := range cases {
		id, dec := Route(inboxes, "Main.Test", c.rcpt)
		if id != c.inbox || dec != c.decoded {
			t.Errorf("Route(%s) = %d %q, want %d %q", c.rcpt, id, dec, c.inbox, c.decoded)
		}
	}
}

func TestRoutePriority(t *testing.T) {
	inboxes := []*store.Inbox{
		{ID: 1, Slug: "d", FormatTemplate: "{local}={domain}", IsDefault: true},
		{ID: 2, Slug: "low", FormatTemplate: "{local}+{domain}", Priority: 5},
		{ID: 3, Slug: "high", FormatTemplate: "{local}+{domain}", Priority: 1},
	}
	if id, _ := Route(inboxes, "m.test", "b+gmail.com@m.test"); id != 3 {
		t.Errorf("priority ignored: got inbox %d", id)
	}
}

// The scenario from the README: specific addresses pinned to inboxes.
func TestRouteAddressLists(t *testing.T) {
	inboxes := []*store.Inbox{
		{ID: 1, Slug: "default", FormatTemplate: "{local}.{name}", AssumedTLD: "com", IsDefault: true},
		{ID: 2, Slug: "inbox-a", FormatTemplate: "{local}.{name}", AssumedTLD: "com", Addresses: []string{"b.gmail@main.test"}},
		{ID: 3, Slug: "inbox-c", FormatTemplate: "{local}={domain}", Addresses: []string{"support@main.test", "qa-*@main.test"}},
		{ID: 4, Slug: "team1", FormatTemplate: "{inbox}-{local}={domain}"},
	}
	cases := []struct {
		rcpt    string
		inbox   int64
		decoded string
	}{
		{"a.gmail@main.test", 1, "a@gmail.com"},
		{"B.Gmail@main.test", 2, "b@gmail.com"},
		{"support@main.test", 3, ""},
		{"qa-anything@main.test", 3, ""},
		{"team1-x=yahoo.com@main.test", 4, "x@yahoo.com"},
		{"b.gmail@other.test", 1, ""}, // the list is exact, domain included
	}
	for _, c := range cases {
		if id, dec := Route(inboxes, "main.test", c.rcpt); id != c.inbox || dec != c.decoded {
			t.Errorf("Route(%s) = %d %q, want %d %q", c.rcpt, id, dec, c.inbox, c.decoded)
		}
	}
}
