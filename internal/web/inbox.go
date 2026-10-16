package web

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/store"
)

const pageSize = 50

// home opens the default inbox, or the first one a restricted user may see.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	all, err := s.Store.ListInboxes(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	visible := current(r).visible(all)
	if len(visible) == 0 {
		http.Error(w, "no inbox is shared with you yet; ask an admin", http.StatusForbidden)
		return
	}
	id := visible[0].ID
	for _, in := range visible {
		if in.IsDefault {
			id = in.ID
		}
	}
	http.Redirect(w, r, "/inbox/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

type inboxData struct {
	Inbox    *store.Inbox
	Inboxes  []*store.Inbox
	Q        string
	Before   int64
	Messages []*store.Message
	Unread   map[int64]bool
	Older    int64 // id for the "older" link, 0 if no more
	Fetched  string
}

func (s *Server) loadInbox(r *http.Request) (*inboxData, error) {
	id, ok := pathID(r, "id")
	if !ok || !current(r).canSee(id) {
		return nil, store.ErrNotFound
	}
	in, err := s.Store.GetInbox(r.Context(), id)
	if err != nil {
		return nil, err
	}
	d := &inboxData{Inbox: in, Q: r.URL.Query().Get("q")}
	d.Before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	// One extra row tells whether an older page exists.
	msgs, err := s.Store.ListMessages(r.Context(), store.Filter{InboxID: id, Direction: "in", Q: d.Q, BeforeID: d.Before, Limit: pageSize + 1})
	if err != nil {
		return nil, err
	}
	if len(msgs) > pageSize {
		msgs = msgs[:pageSize]
		d.Older = msgs[pageSize-1].ID
	}
	d.Messages = msgs
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if d.Unread, err = s.Store.Unread(r.Context(), current(r).User, ids); err != nil {
		return nil, err
	}
	d.Fetched = r.URL.Query().Get("fetched")
	return d, nil
}

func (s *Server) inboxPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadInbox(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if d.Inboxes, err = s.Store.ListInboxes(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "inbox", d.Inbox.Name, d)
}

// inboxRows re-renders only the table body; app.js swaps it in on new-mail events.
func (s *Server) inboxRows(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadInbox(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.pages["inbox"].ExecuteTemplate(w, "rows", d)
}

// events streams new-message notifications (SSE) to the open inbox page.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := s.Broker.Subscribe()
	defer cancel()

	fmt.Fprint(w, "retry: 5000\n\n")
	rc.Flush()
	// Keeps the tunnel from closing an idle stream (Cloudflare drops after ~100 s).
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		case e := <-ch:
			if !current(r).canSee(e.InboxID) {
				continue
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

type messageData struct {
	M           *store.Message
	Inbox       *store.Inbox
	Attachments []store.Attachment
	Headers     string
	Images      bool
	SentBy      string           // username, outbound only
	Children    []*store.Message // replies/forwards sent from here
	JustSent    int64            // id of a message just sent in reply to this one
}

func (s *Server) messagePage(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	d := messageData{M: m, Images: r.URL.Query().Get("images") == "1"}
	if d.Inbox, err = s.Store.GetInbox(r.Context(), m.InboxID); err != nil {
		s.fail(w, err)
		return
	}
	if d.Attachments, err = s.Store.Attachments(r.Context(), m.ID); err != nil {
		s.fail(w, err)
		return
	}
	d.Headers = rawHeaders(s.Store.Path(m.RawPath))
	if m.Direction == "in" {
		s.Store.MarkRead(r.Context(), current(r).User.ID, m.ID)
	}
	if d.Children, err = s.Store.Children(r.Context(), m.ID); err != nil {
		s.fail(w, err)
		return
	}
	if m.SentBy != nil {
		if u, err := s.Store.GetUser(r.Context(), *m.SentBy); err == nil {
			d.SentBy = u.Username
		}
	}
	d.JustSent, _ = strconv.ParseInt(r.URL.Query().Get("sent"), 10, 64)
	title := m.Subject
	if title == "" {
		title = "(no subject)"
	}
	s.render(w, r, "message", title, d)
}

func (s *Server) message(r *http.Request) (*store.Message, error) {
	id, ok := pathID(r, "id")
	if !ok {
		return nil, store.ErrNotFound
	}
	m, err := s.Store.GetMessage(r.Context(), id)
	if err == nil && !current(r).canSee(m.InboxID) {
		return nil, store.ErrNotFound // hidden inboxes look empty, not forbidden
	}
	return m, err
}

// rawHeaders returns the header block of the stored source (capped at 256 KiB).
func rawHeaders(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var out bytes.Buffer
	sc := bufio.NewScanner(io.LimitReader(f, 256<<10))
	sc.Buffer(make([]byte, 64<<10), 256<<10)
	for sc.Scan() {
		line := bytes.TrimRight(sc.Bytes(), "\r")
		if len(line) == 0 {
			break
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.String()
}

var cidRe = regexp.MustCompile(`(?i)cid:([^"'\s)>]+)`)

// messageBody serves the HTML part for the sandboxed iframe. The CSP sandboxes it
// even when opened directly; remote images load only with ?images=1.
func (s *Server) messageBody(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	atts, err := s.Store.Attachments(r.Context(), m.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Inline cid: images as data: URIs — a sandboxed (opaque-origin) frame would not
	// send the session cookie when fetching them from us.
	byCID := map[string]store.Attachment{}
	for _, a := range atts {
		if a.ContentID != "" {
			byCID[a.ContentID] = a
		}
	}
	body := cidRe.ReplaceAllStringFunc(m.HTML, func(ref string) string {
		a, ok := byCID[cidRe.FindStringSubmatch(ref)[1]]
		if !ok || a.Size > 5<<20 {
			return ref
		}
		data, err := os.ReadFile(s.Store.Path(a.Path))
		if err != nil {
			return ref
		}
		return "data:" + a.ContentType + ";base64," + base64.StdEncoding.EncodeToString(data)
	})

	img := "data:"
	if r.URL.Query().Get("images") == "1" {
		img = "data: https: http:"
	}
	w.Header().Set("Content-Security-Policy", "sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; "+
		"img-src "+img+"; style-src 'unsafe-inline' "+imgIfRemote(img)+"; font-src data: "+imgIfRemote(img)+"; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, `<!doctype html><meta charset="utf-8"><base target="_blank">`)
	io.WriteString(w, body)
}

func imgIfRemote(img string) string {
	if img == "data:" {
		return ""
	}
	return "https:"
}

func (s *Server) messageRaw(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="message-%d.eml"`, m.ID))
	}
	http.ServeFile(w, r, s.Store.Path(m.RawPath))
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	if _, err := s.message(r); err != nil {
		s.fail(w, err)
		return
	}
	ServeAttachment(w, r, s.Store)
}

// ServeAttachment always downloads (never renders inline) so attachments cannot run in our origin.
func ServeAttachment(w http.ResponseWriter, r *http.Request, st *store.Store) {
	mid, ok1 := pathID(r, "id")
	aid, ok2 := pathID(r, "aid")
	if !ok1 || !ok2 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	a, err := st.GetAttachment(r.Context(), mid, aid)
	if err != nil {
		if err == store.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	name := a.Filename
	if name == "" {
		name = fmt.Sprintf("attachment-%d", a.ID)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeFile(w, r, st.Path(a.Path))
}

func (s *Server) messageUnread(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.MarkUnread(r.Context(), current(r).User.ID, m.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/inbox/"+strconv.FormatInt(m.InboxID, 10), http.StatusSeeOther)
}

func (s *Server) inboxMarkRead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || !current(r).canSee(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.MarkInboxRead(r.Context(), current(r).User, id); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/inbox/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// refresh collects mail still waiting in the Worker's KV, then reloads the page.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	back := safeNext(r.PostFormValue("back"))
	n := 0
	if s.Drain != nil {
		var err error
		if n, err = s.Drain(r.Context()); err != nil {
			slog.Warn("manual refresh", "err", err)
		}
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"fetched="+strconv.Itoa(n), http.StatusSeeOther)
}

// messagesBulk applies read / unread / delete to the ticked messages the user
// may see, then returns to the list it came from.
func (s *Server) messagesBulk(w http.ResponseWriter, r *http.Request) {
	c := current(r)
	back := safeNext(r.PostFormValue("back"))
	var ids []int64
	for _, id := range formIDs(r, "id") {
		m, err := s.Store.GetMessage(r.Context(), id)
		if err == nil && c.canSee(m.InboxID) {
			ids = append(ids, id)
		}
	}
	var err error
	switch r.PostFormValue("action") {
	case "read":
		for _, id := range ids {
			if err = s.Store.MarkRead(r.Context(), c.User.ID, id); err != nil {
				break
			}
		}
	case "unread":
		for _, id := range ids {
			if err = s.Store.MarkUnread(r.Context(), c.User.ID, id); err != nil {
				break
			}
		}
	case "delete":
		if len(ids) > 0 {
			var n int
			n, err = s.Store.DeleteMessages(r.Context(), store.Filter{IDs: ids})
			slog.Info("messages deleted", "user", c.User.Username, "count", n)
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
