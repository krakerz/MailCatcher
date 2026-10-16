package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/address"
	"mailcatcher/internal/rules"
	"mailcatcher/internal/store"
)

type ruleRow struct {
	*store.ForwardRule
	InboxName string
	Verified  bool
	Known     bool     // destination is in the synced list
	Pattern   bool     // address is a catch-all pattern rather than a real recipient
	Reach     []string // inboxes the rule can fire in
}

type rulesData struct {
	Rules      []ruleRow
	Inboxes    []*store.Inbox
	Verified   []string
	Domain     string
	PushedAt   time.Time
	PushedN    int
	PushErr    string
	Test       string
	TestInbox  string
	TestResult []string
	TestWarn   string // the address is malformed or not on one of our domains
	Tested     bool
	Notice     string
	Error      string
	AnyAllowed bool // may create "any inbox" rules
}

func (s *Server) renderRules(w http.ResponseWriter, r *http.Request, d rulesData) {
	ctx := r.Context()
	c := current(r)
	all, err := s.Store.ListRules(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	var list []*store.ForwardRule
	for _, rl := range all {
		if c.canManageRule(rl.InboxID) {
			list = append(list, rl)
		}
	}
	allInboxes, err := s.Store.ListInboxes(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	d.Inboxes, d.AnyAllowed = c.visible(allInboxes), c.canManageRule(0)
	names := map[int64]string{}
	for _, in := range d.Inboxes {
		names[in.ID] = in.Name
	}
	dests, _ := s.Store.ListDestinations(ctx)
	status := map[string]bool{}
	for _, dst := range dests {
		status[dst.Email] = dst.Verified()
		if dst.Verified() {
			d.Verified = append(d.Verified, dst.Email)
		}
	}
	for _, rl := range list {
		v, known := status[strings.ToLower(rl.Destination)]
		row := ruleRow{ForwardRule: rl, InboxName: names[rl.InboxID], Verified: v, Known: known,
			Pattern: rules.IsCatchAllPattern(strings.ToLower(rl.AddressGlob), d.Inboxes, s.Domain), Reach: rules.Reach(rl, allInboxes, s.Domain)}
		d.Rules = append(d.Rules, row)
	}
	d.Domain = s.Domain
	if s.Rules != nil {
		var perr error
		d.PushedAt, d.PushedN, perr = s.Rules.Status()
		if perr != nil {
			d.PushErr = perr.Error()
		}
	}
	if d.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "rules", "Forward rules", d)
}

// rulesPage also answers "which rules match this address?" via ?test=.
func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	d := rulesData{Test: strings.TrimSpace(r.URL.Query().Get("test"))}
	if d.Test != "" {
		c := current(r)
		all, err := s.Store.ListRules(r.Context())
		inboxes, err2 := s.Store.ListInboxes(r.Context())
		if err == nil {
			err = err2
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		var mine []*store.ForwardRule // test only the rules this user manages
		for _, rl := range all {
			if c.canManageRule(rl.InboxID) {
				mine = append(mine, rl)
			}
		}
		d.Tested, d.TestResult = true, rules.Match(rules.Compile(mine, inboxes, s.Domain), d.Test)
		if _, domain, ok := address.Split(d.Test); !ok {
			d.TestWarn = "This is not a valid email address."
		} else if !address.OwnDomain(inboxes, s.Domain, domain) {
			d.TestWarn = "The domain " + domain + " is not one of yours (main domain: " + s.Domain + "). Mail to it never reaches mailcatcher. Check for a typo."
		}
		{
			id, decoded := address.Route(inboxes, s.Domain, d.Test)
			d.TestInbox = "an inbox you cannot open"
			for _, in := range c.visible(inboxes) {
				if in.ID == id {
					d.TestInbox = in.Name
				}
			}
			if decoded != "" {
				d.TestInbox += " (real address " + decoded + ")"
			}
		}
	}
	s.renderRules(w, r, d)
}

func (s *Server) ruleCreate(w http.ResponseWriter, r *http.Request) {
	rl := &store.ForwardRule{
		AddressGlob: strings.TrimSpace(r.PostFormValue("glob")),
		Destination: strings.ToLower(strings.TrimSpace(r.PostFormValue("destination"))),
		Enabled:     true,
	}
	rl.InboxID, _ = strconv.ParseInt(r.PostFormValue("inbox"), 10, 64)
	if !current(r).canManageRule(rl.InboxID) {
		s.renderRules(w, r, rulesData{Error: "You can only add rules for inboxes you can open."})
		return
	}
	if _, _, ok := address.Split(rl.Destination); !ok {
		s.renderRules(w, r, rulesData{Error: "Destination must be an email address."})
		return
	}
	if strings.ContainsAny(rl.AddressGlob, " ,") {
		s.renderRules(w, r, rulesData{Error: "Address pattern: one address, * as wildcard."})
		return
	}
	if err := s.Store.CreateRule(r.Context(), rl); err != nil {
		s.fail(w, err)
		return
	}
	s.afterRuleChange(w, r, "Rule added.")
}

func (s *Server) ruleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	rl, err := s.Store.GetRule(r.Context(), id)
	if err == nil && !current(r).canManageRule(rl.InboxID) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	switch r.PostFormValue("action") {
	case "enable":
		err = s.Store.SetRuleEnabled(r.Context(), id, true)
	case "disable":
		err = s.Store.SetRuleEnabled(r.Context(), id, false)
	case "delete":
		err = s.Store.DeleteRule(r.Context(), id)
	default:
		s.renderRules(w, r, rulesData{Error: "Unknown action."})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.afterRuleChange(w, r, "Saved.")
}

// afterRuleChange pushes synchronously so the page shows whether the Worker has the change.
func (s *Server) afterRuleChange(w http.ResponseWriter, r *http.Request, notice string) {
	d := rulesData{Notice: notice}
	if s.Rules != nil {
		if err := s.Rules.Push(r.Context()); err != nil {
			d.Notice += " Could not update the Worker yet; it retries automatically."
		} else {
			d.Notice += " The Worker has the new rules."
		}
	}
	s.renderRules(w, r, d)
}

func (s *Server) rulesPush(w http.ResponseWriter, r *http.Request) {
	s.afterRuleChange(w, r, "Pushed.")
}
