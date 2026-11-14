package store

import (
	"context"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestThreadingStaysInInbox(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	other := &Inbox{Name: "Other", Slug: "other", FormatTemplate: "{local}.{name}", AssumedTLD: "com"}
	if err := st.CreateInbox(ctx, other); err != nil {
		t.Fatal(err)
	}
	orig := &Message{InboxID: 1, Direction: "in", MessageID: "<a@x.test>"}
	st.InsertMessage(ctx, orig, []byte("Subject: a\r\n\r\n1\r\n"), nil)

	foreign := &Message{InboxID: other.ID, Direction: "in", InReplyTo: "<a@x.test>", References: []string{"<a@x.test>"}}
	st.InsertMessage(ctx, foreign, []byte("Subject: b\r\n\r\n2\r\n"), nil)
	if foreign.ParentID != nil {
		t.Fatalf("reply in another inbox linked to parent %d", *foreign.ParentID)
	}
	kids, _ := st.Children(ctx, orig.ID)
	if len(kids) != 0 {
		t.Fatalf("children leaked across inboxes: %d", len(kids))
	}

	same := &Message{InboxID: 1, Direction: "in", InReplyTo: "<a@x.test>"}
	st.InsertMessage(ctx, same, []byte("Subject: c\r\n\r\n3\r\n"), nil)
	if same.ParentID == nil || *same.ParentID != orig.ID {
		t.Fatalf("same-inbox reply not linked: %v", same.ParentID)
	}
}

func TestDedupPerInbox(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	other := &Inbox{Name: "Other", Slug: "other", FormatTemplate: "{local}.{name}", AssumedTLD: "com"}
	if err := st.CreateInbox(ctx, other); err != nil {
		t.Fatal(err)
	}
	raw := []byte("Subject: same\r\n\r\nbytes\r\n")
	a1 := &Message{InboxID: 1, Direction: "in"}
	a2 := &Message{InboxID: 1, Direction: "in"}
	b := &Message{InboxID: other.ID, Direction: "in"}
	if dup, err := st.InsertMessage(ctx, a1, raw, nil); dup || err != nil {
		t.Fatalf("first: dup=%v err=%v", dup, err)
	}
	if dup, err := st.InsertMessage(ctx, a2, raw, nil); !dup || err != nil || a2.ID != a1.ID {
		t.Fatalf("repeat in same inbox: dup=%v err=%v id=%d", dup, err, a2.ID)
	}
	if dup, err := st.InsertMessage(ctx, b, raw, nil); dup || err != nil || b.ID == a1.ID {
		t.Fatalf("other inbox: dup=%v err=%v id=%d", dup, err, b.ID)
	}
	// Rerouting into an inbox that already holds the bytes must not fail.
	if err := st.SetRouting(ctx, b.ID, 1, "x@y.test"); err != nil {
		t.Fatal(err)
	}
}

// The dedup migration rebuilds messages; attachments must survive it.
func TestMigrationKeepsChildren(t *testing.T) {
	dir := t.TempDir()
	full := migrations
	migrations = full[:len(full)-1]
	st, err := Open(dir)
	migrations = full
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx, `INSERT INTO messages (inbox_id, direction, received_at, size, raw_sha256) VALUES (1, 'in', 1, 1, 'h');
		INSERT INTO attachments (message_id, size) VALUES (1, 1);`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM attachments WHERE message_id = 1").Scan(&n)
	if n != 1 {
		t.Fatalf("attachments after migration: %d", n)
	}
}
