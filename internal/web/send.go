package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"mailcatcher/internal/outbound"
	"mailcatcher/internal/store"
)

// formFiles reads the "files" uploads of a multipart form.
func formFiles(r *http.Request) ([]outbound.File, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	var out []outbound.File
	var total int64
	for _, fh := range r.MultipartForm.File["files"] {
		if fh.Size == 0 && fh.Filename == "" {
			continue
		}
		total += fh.Size
		if total > outbound.MaxUpload {
			return nil, fmt.Errorf("attachments are larger than %d MB in total", outbound.MaxUpload>>20)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, outbound.File{Name: fh.Filename, ContentType: fh.Header.Get("Content-Type"), Data: data})
	}
	return out, nil
}

// sendError turns a send failure into a message for the form.
func sendError(err error, to string) string {
	switch {
	case errors.Is(err, outbound.ErrNotVerified):
		return to + " is not a verified destination address. Cloudflare (free plan) only delivers to verified addresses: its owner has to click a verification link first, then send again."
	case errors.Is(err, outbound.ErrNotActive):
		return to + " is verified, but Cloudflare has not activated it for sending yet. This usually takes under a minute — send again shortly."
	case errors.Is(err, outbound.ErrDisabled):
		return "Sending is not configured: set MC_WORKER_URL to the Worker's URL."
	}
	return "Sending failed: " + err.Error()
}

func userID(r *http.Request) *int64 {
	id := current(r).User.ID
	return &id
}

type replyData struct {
	M          *store.Message
	To         string
	Subject    string
	Text       string
	HTML       string // editor markup, kept when the form is shown again
	Quote      bool
	Error      string
	Unverified string // recipient to offer verification for
}

func (s *Server) replyPage(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "reply", "Reply", replyData{M: m, To: s.Mailer.ReplyTarget(m), Subject: replySubject(m.Subject), Quote: true})
}

func replySubject(s string) string { return outbound.ReplySubject(s) }

func (s *Server) replySubmit(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	d := replyData{M: m, To: s.Mailer.ReplyTarget(m), Subject: replySubject(m.Subject), Text: r.PostFormValue("text"), HTML: r.PostFormValue("html"), Quote: r.PostFormValue("quote") == "1"}
	files, err := formFiles(r)
	if err == nil {
		var out *store.Message
		out, err = s.Mailer.Reply(r.Context(), m, outbound.ReplyOptions{Text: d.Text, HTML: d.HTML, Quote: d.Quote, Files: files, SentBy: userID(r)})
		if err == nil {
			http.Redirect(w, r, fmt.Sprintf("/m/%d?sent=%d", m.ID, out.ID), http.StatusSeeOther)
			return
		}
	}
	d.Error, d.Unverified = sendError(err, d.To), s.offerVerification(err, d.To)
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, r, "reply", "Reply", d)
}

type forwardData struct {
	M          *store.Message
	To, Note   string
	ReplyBack  bool
	Sender     string
	Error      string
	Unverified string
	Verified   []string // To suggestions
}

func (s *Server) forwardPage(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "forward", "Forward", forwardData{M: m, Sender: s.Mailer.ReplyTarget(m), Verified: s.verifiedEmails(r.Context())})
}

func (s *Server) forwardSubmit(w http.ResponseWriter, r *http.Request) {
	m, err := s.message(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	d := forwardData{M: m, To: strings.TrimSpace(r.PostFormValue("to")), Note: r.PostFormValue("note"),
		ReplyBack: r.PostFormValue("reply_back") == "1", Sender: s.Mailer.ReplyTarget(m)}
	out, err := s.Mailer.Forward(r.Context(), m, outbound.ForwardOptions{To: d.To, Note: d.Note, ReplyToMailcatcher: d.ReplyBack, SentBy: userID(r)})
	if err == nil {
		http.Redirect(w, r, fmt.Sprintf("/m/%d?sent=%d", m.ID, out.ID), http.StatusSeeOther)
		return
	}
	d.Error, d.Unverified = sendError(err, d.To), s.offerVerification(err, d.To)
	d.Verified = s.verifiedEmails(r.Context())
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, r, "forward", "Forward", d)
}

type composeData struct {
	Inboxes                          []*store.Inbox
	InboxID                          int64
	Identity, To, Subject, Text, Err string
	HTML                             string
	Unverified                       string
	Verified                         []string
}

func (s *Server) composePage(w http.ResponseWriter, r *http.Request) {
	d := composeData{}
	d.InboxID, _ = strconv.ParseInt(r.URL.Query().Get("inbox"), 10, 64)
	s.renderCompose(w, r, d)
}

func (s *Server) renderCompose(w http.ResponseWriter, r *http.Request, d composeData) {
	var err error
	if d.Inboxes, err = s.Store.ListInboxes(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	d.Inboxes = current(r).visible(d.Inboxes)
	if d.InboxID == 0 && len(d.Inboxes) > 0 {
		d.InboxID = d.Inboxes[0].ID
	}
	if d.InboxID == 0 {
		for _, in := range d.Inboxes {
			if in.IsDefault {
				d.InboxID = in.ID
			}
		}
	}
	d.Verified = s.verifiedEmails(r.Context())
	if d.Err != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "compose", "Compose", d)
}

func (s *Server) composeSubmit(w http.ResponseWriter, r *http.Request) {
	d := composeData{
		Identity: strings.TrimSpace(r.PostFormValue("identity")),
		To:       strings.TrimSpace(r.PostFormValue("to")),
		Subject:  r.PostFormValue("subject"),
		Text:     r.PostFormValue("text"),
		HTML:     r.PostFormValue("html"),
	}
	d.InboxID, _ = strconv.ParseInt(r.PostFormValue("inbox"), 10, 64)
	if !current(r).canSee(d.InboxID) {
		d.Err = "You do not have access to that inbox."
		s.renderCompose(w, r, d)
		return
	}
	files, err := formFiles(r)
	if err == nil {
		var out *store.Message
		out, err = s.Mailer.Compose(r.Context(), outbound.ComposeOptions{InboxID: d.InboxID, Identity: d.Identity, To: d.To,
			Subject: d.Subject, Text: d.Text, HTML: d.HTML, Files: files, SentBy: userID(r)})
		if err == nil {
			http.Redirect(w, r, fmt.Sprintf("/m/%d", out.ID), http.StatusSeeOther)
			return
		}
	}
	d.Err, d.Unverified = sendError(err, d.To), s.offerVerification(err, d.To)
	s.renderCompose(w, r, d)
}

// composeFrom previews the From address for an identity (compose form).
func (s *Server) composeFrom(w http.ResponseWriter, r *http.Request) {
	res := map[string]string{}
	id, _ := strconv.ParseInt(r.URL.Query().Get("inbox"), 10, 64)
	in, err := s.Store.GetInbox(r.Context(), id)
	if err == nil && !current(r).canSee(id) {
		err = store.ErrNotFound
	}
	if err == nil {
		var from string
		if from, _, err = s.Mailer.FromAddress(in, r.URL.Query().Get("identity")); err == nil {
			res["from"] = from
		}
	}
	if err != nil {
		res["error"] = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

type sentData struct {
	Messages []*store.Message
	Users    map[int64]string
	Inboxes  []*store.Inbox
	Names    map[int64]string // inbox id → name
	InboxID  int64            // filter, 0 = all
	Before   int64
	Older    int64
}

func (s *Server) sentPage(w http.ResponseWriter, r *http.Request) {
	c := current(r)
	d := sentData{Users: map[int64]string{}, Names: map[int64]string{}}
	d.Before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	d.InboxID, _ = strconv.ParseInt(r.URL.Query().Get("inbox"), 10, 64)
	all, err := s.Store.ListInboxes(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	d.Inboxes = c.visible(all)
	for _, in := range all {
		d.Names[in.ID] = in.Name
	}
	f := store.Filter{Direction: "out", InboxIDs: c.Allowed, BeforeID: d.Before, Limit: pageSize + 1}
	if d.InboxID != 0 {
		if !c.canSee(d.InboxID) {
			http.NotFound(w, r)
			return
		}
		f.InboxID = d.InboxID
	}
	msgs, err := s.Store.ListMessages(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(msgs) > pageSize {
		msgs = msgs[:pageSize]
		d.Older = msgs[pageSize-1].ID
	}
	d.Messages = msgs
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, u := range users {
		d.Users[u.ID] = u.Username
	}
	s.render(w, r, "sent", "Sent", d)
}

// offerVerification returns the recipient to show a "send verification link"
// button for, when that is possible.
func (s *Server) offerVerification(err error, to string) string {
	if errors.Is(err, outbound.ErrNotVerified) && s.Dest.Enabled() {
		return to
	}
	return ""
}
