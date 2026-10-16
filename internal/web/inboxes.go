package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"mailcatcher/internal/address"
	"mailcatcher/internal/auth"
	"mailcatcher/internal/store"
)

type inboxesData struct {
	Inboxes []*store.Inbox
	Domain  string
	Presets any
	Form    *store.Inbox // create form values (sticky on error)
	Key     string       // freshly generated API key, shown once
	KeyFor  string
	Notice  string
	Error   string
}

func (s *Server) renderInboxes(w http.ResponseWriter, r *http.Request, d inboxesData) {
	var err error
	if d.Inboxes, err = s.Store.ListInboxes(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	d.Domain, d.Presets = s.Domain, address.Presets
	if d.Form == nil {
		d.Form = &store.Inbox{FormatTemplate: "{inbox}-{local}={domain}", AssumedTLD: "com"}
	}
	if d.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "inboxes", "Inboxes", d)
}

func (s *Server) inboxesPage(w http.ResponseWriter, r *http.Request) {
	s.renderInboxes(w, r, inboxesData{})
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	domainRe = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)
)

// inboxFromForm reads and validates the inbox form.
func inboxFromForm(r *http.Request) (*store.Inbox, string) {
	in := &store.Inbox{
		Name:           strings.TrimSpace(r.PostFormValue("name")),
		Slug:           strings.ToLower(strings.TrimSpace(r.PostFormValue("slug"))),
		Domain:         strings.ToLower(strings.TrimSpace(r.PostFormValue("domain"))),
		FormatTemplate: strings.TrimSpace(r.PostFormValue("format")),
		AssumedTLD:     strings.Trim(strings.ToLower(strings.TrimSpace(r.PostFormValue("tld"))), "."),
	}
	in.Priority, _ = strconv.Atoi(r.PostFormValue("priority"))
	for _, line := range strings.Split(r.PostFormValue("addresses"), "\n") {
		if a := strings.ToLower(strings.TrimSpace(line)); a != "" {
			if strings.ContainsAny(a, " ,;") || !strings.Contains(a, "@") {
				return in, "Addresses: one address per line, like b.gmail@example.com or qa-*@example.com (" + a + ")."
			}
			in.Addresses = append(in.Addresses, a)
		}
	}
	switch {
	case in.Name == "":
		return in, "Name is required."
	case !slugRe.MatchString(in.Slug):
		return in, "Slug: 1–31 characters, lowercase letters, digits or dashes, starting with a letter or digit."
	case in.Domain != "" && !domainRe.MatchString(in.Domain):
		return in, "Domain does not look like a domain."
	}
	if _, err := address.For(in); err != nil {
		return in, "Address format: " + err.Error() + "."
	}
	return in, ""
}

func (s *Server) inboxCreate(w http.ResponseWriter, r *http.Request) {
	in, msg := inboxFromForm(r)
	if msg != "" {
		s.renderInboxes(w, r, inboxesData{Form: in, Error: msg})
		return
	}
	if err := s.Store.CreateInbox(r.Context(), in); err != nil {
		if err == store.ErrExists {
			s.renderInboxes(w, r, inboxesData{Form: in, Error: "That slug is taken."})
			return
		}
		s.fail(w, err)
		return
	}
	s.rulesChanged()
	s.renderInboxes(w, r, inboxesData{Notice: "Created inbox " + in.Name + ". New mail is routed to it right away; use “Re-apply routing” to move existing mail."})
}

type inboxEditData struct {
	Inbox   *store.Inbox
	Domain  string
	Presets any
	Error   string
}

func (s *Server) inboxEditPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	in, err := s.Store.GetInbox(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "inbox_edit", "Edit "+in.Name, inboxEditData{Inbox: in, Domain: s.Domain, Presets: address.Presets})
}

func (s *Server) inboxUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	cur, err := s.Store.GetInbox(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	switch r.PostFormValue("action") {
	case "default":
		if err := s.Store.SetDefaultInbox(r.Context(), id); err != nil {
			s.fail(w, err)
			return
		}
		s.rulesChanged()
		s.renderInboxes(w, r, inboxesData{Notice: cur.Name + " is now the default inbox."})
	case "delete":
		if cur.IsDefault {
			s.renderInboxes(w, r, inboxesData{Error: "The default inbox cannot be deleted. Make another inbox the default first."})
			return
		}
		if r.PostFormValue("confirm") != cur.Slug {
			s.renderInboxes(w, r, inboxesData{Error: "Type the inbox slug to confirm deleting it."})
			return
		}
		if err := s.Store.DeleteInbox(r.Context(), id); err != nil {
			s.fail(w, err)
			return
		}
		s.rulesChanged()
		s.renderInboxes(w, r, inboxesData{Notice: "Deleted inbox " + cur.Name + " and its messages."})
	default:
		in, msg := inboxFromForm(r)
		in.ID, in.IsDefault = id, cur.IsDefault
		if msg == "" {
			if err := s.Store.UpdateInbox(r.Context(), in); err == store.ErrExists {
				msg = "That slug is taken."
			} else if err != nil {
				s.fail(w, err)
				return
			}
		}
		if msg != "" {
			w.WriteHeader(http.StatusBadRequest)
			s.render(w, r, "inbox_edit", "Edit "+cur.Name, inboxEditData{Inbox: in, Domain: s.Domain, Presets: address.Presets, Error: msg})
			return
		}
		s.rulesChanged()
		s.renderInboxes(w, r, inboxesData{Notice: "Saved " + in.Name + ". Use “Re-apply routing” to apply the change to existing mail."})
	}
}

func (s *Server) inboxReroute(w http.ResponseWriter, r *http.Request) {
	n, err := address.Reroute(r.Context(), s.Store, s.Domain)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.renderInboxes(w, r, inboxesData{Notice: fmt.Sprintf("Routing re-applied: %d message(s) moved or re-decoded.", n)})
}

func (s *Server) inboxAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	in, err := s.Store.GetInbox(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	key := "mc_" + auth.Token(24)
	if err := s.Store.SetInboxAPIKeyHash(r.Context(), id, auth.HashToken(key)); err != nil {
		s.fail(w, err)
		return
	}
	s.renderInboxes(w, r, inboxesData{Key: key, KeyFor: in.Name})
}

type previewResult struct {
	Address  string   `json:"address,omitempty"`
	Decoded  string   `json:"decoded,omitempty"`
	Lands    string   `json:"lands,omitempty"`    // where mail to Address (or a listed address) really goes
	Conflict bool     `json:"conflict,omitempty"` // it goes to another inbox
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings"`
}

// formatPreview powers the live preview on the inbox forms.
func (s *Server) formatPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := &store.Inbox{FormatTemplate: q.Get("format"), AssumedTLD: q.Get("tld"), Slug: strings.ToLower(q.Get("slug")),
		Domain: strings.ToLower(q.Get("domain")), Name: strings.TrimSpace(q.Get("name")), Addresses: strings.Fields(strings.ToLower(q.Get("addresses")))}
	in.ID, _ = strconv.ParseInt(q.Get("id"), 10, 64)
	in.Priority, _ = strconv.Atoi(q.Get("priority"))
	if in.Slug == "" {
		in.Slug = "team1" // stand-in while the slug field is still empty
	}
	res := previewResult{Warnings: []string{}}
	f, err := address.For(in)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Warnings = append(res.Warnings, f.Warnings()...)
		if w := f.PrefixWarning(strings.Fields(q.Get("addresses"))); w != "" {
			res.Warnings = append(res.Warnings, w)
		}
		if local, err := f.Encode(q.Get("address")); err != nil {
			res.Error = err.Error()
		} else {
			res.Address = local + "@" + address.CatchDomain(in, s.Domain)
			res.Decoded, _ = f.Decode(local)
		}
		s.explainRouting(r, in, &res)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// rulesChanged re-pushes forward rules: inbox formats and priorities shape them.
func (s *Server) rulesChanged() {
	if s.Rules != nil {
		s.Rules.PushSoon()
	}
}

// explainRouting says where mail to the preview address (or, for an address
// list, its first entry) really lands, routing against the saved inboxes with
// this form's values in place of the inbox being edited.
func (s *Server) explainRouting(r *http.Request, in *store.Inbox, res *previewResult) {
	all, err := s.Store.ListInboxes(r.Context())
	if err != nil {
		return
	}
	if in.ID == 0 {
		in.ID = -1 // not saved yet
	}
	list := []*store.Inbox{in}
	for _, other := range all {
		if other.ID == in.ID {
			in.IsDefault = other.IsDefault
			continue
		}
		list = append(list, other)
	}
	sample := res.Address
	if len(in.Addresses) > 0 {
		sample = strings.ReplaceAll(in.Addresses[0], "*", "any")
	}
	if sample == "" {
		return
	}
	id, decoded := address.Route(list, s.Domain, sample)
	name := in.Name
	if name == "" {
		name = "this inbox"
	}
	shown := ""
	if decoded != "" {
		shown = ", shown as " + decoded
	}
	if id == in.ID {
		res.Lands = "Mail to " + sample + " lands in " + name + shown + "."
		return
	}
	for _, other := range list {
		if other.ID == id {
			res.Lands, res.Conflict = "Mail to "+sample+" would land in "+other.Name+" instead — that inbox is checked first (address list, priority or default).", true
		}
	}
}
