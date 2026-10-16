package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"mailcatcher/internal/destinations"
	"mailcatcher/internal/store"
)

type destinationsData struct {
	Enabled bool
	List    []*store.Destination
	Notice  string
	Error   string
}

func (s *Server) renderDestinations(w http.ResponseWriter, r *http.Request, d destinationsData) {
	d.Enabled = s.Dest.Enabled()
	var err error
	if d.List, err = s.Store.ListDestinations(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	if d.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "destinations", "Destinations", d)
}

func (s *Server) destinationsPage(w http.ResponseWriter, r *http.Request) {
	s.renderDestinations(w, r, destinationsData{})
}

func destError(err error) string {
	switch {
	case errors.Is(err, destinations.ErrTooSoon):
		return "Cloudflare sent a verification email to this address recently; wait a few minutes before asking again."
	case errors.Is(err, destinations.ErrDisabled):
		return "Destination management is not configured: set MC_CF_API_TOKEN and MC_CF_ACCOUNT_ID."
	}
	return err.Error()
}

// destinationAdd creates (or re-sends verification for) an address. Called from
// the Destinations page or, with X-Requested-With: fetch, inline from a send error.
func (s *Server) destinationAdd(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PostFormValue("email"))
	d, err := s.Dest.Add(r.Context(), email)
	notice := ""
	if err == nil {
		notice = "Cloudflare emailed a verification link to " + d.Email + ". Its owner must click it; then the address can receive mail."
		if d.Verified() {
			notice = d.Email + " is already verified."
		}
	}
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": destError(err)})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"notice": notice})
		return
	}
	if err != nil {
		s.renderDestinations(w, r, destinationsData{Error: destError(err)})
		return
	}
	s.renderDestinations(w, r, destinationsData{Notice: notice})
}

func (s *Server) destinationSync(w http.ResponseWriter, r *http.Request) {
	if err := s.Dest.Sync(r.Context()); err != nil {
		s.renderDestinations(w, r, destinationsData{Error: destError(err)})
		return
	}
	s.renderDestinations(w, r, destinationsData{Notice: "Refreshed from Cloudflare."})
}

func (s *Server) destinationRemove(w http.ResponseWriter, r *http.Request) {
	email := r.PostFormValue("email")
	if err := s.Dest.Remove(r.Context(), email); err != nil {
		s.renderDestinations(w, r, destinationsData{Error: destError(err)})
		return
	}
	s.renderDestinations(w, r, destinationsData{Notice: "Removed " + email + "."})
}

// verifiedEmails feeds the To suggestions on the send forms.
func (s *Server) verifiedEmails(ctx context.Context) []string {
	list, err := s.Store.ListDestinations(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range list {
		if d.Verified() {
			out = append(out, d.Email)
		}
	}
	return out
}
