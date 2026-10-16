// Package rules compiles auto-forward rules into the form the Worker evaluates:
// per rule, regexes that must all match the recipient and regexes none of which
// may match. Only syntax shared by RE2 and JavaScript is used, and matching is
// case-insensitive on both sides, so Match here behaves exactly like the Worker.
package rules

import (
	"regexp"
	"slices"
	"strings"

	"mailcatcher/internal/address"
	"mailcatcher/internal/store"
)

// WorkerRule is the JSON stored in the Worker's KV under "rules".
type WorkerRule struct {
	All         []string `json:"all"`
	None        []string `json:"none"`
	Destination string   `json:"destination"`
}

// inboxExprs expresses "address routes to this inbox", mirroring address.Route.
func inboxExprs(inboxes []*store.Inbox, mainDomain string) map[int64]WorkerRule {
	out := map[int64]WorkerRule{}
	var before []string // match patterns of inboxes tried earlier
	for _, in := range address.RouteOrder(inboxes) {
		if in.IsDefault {
			// Route only reaches the default when nothing before matched.
			if _, seen := out[in.ID]; !seen {
				out[in.ID] = WorkerRule{All: []string{}, None: slices.Clone(before)}
			}
			break
		}
		var m string
		if len(in.Addresses) > 0 {
			alts := make([]string, len(in.Addresses))
			for i, g := range in.Addresses {
				alts[i] = address.GlobPattern(g)
			}
			m = "^(?:" + strings.Join(alts, "|") + ")$"
		} else {
			f, err := address.For(in)
			if err != nil {
				continue // Route skips it as well
			}
			m = "^(?:" + f.Pattern() + ")@" + regexp.QuoteMeta(address.CatchDomain(in, mainDomain)) + "$"
		}
		if _, seen := out[in.ID]; !seen {
			out[in.ID] = WorkerRule{All: []string{m}, None: slices.Clone(before)}
		}
		before = append(before, m)
	}
	return out
}

// GlobRegex turns a catch-all address glob ("*" wildcard) into an anchored regex.
// An empty glob or "*" means any address and returns "".
func GlobRegex(glob string) string {
	glob = strings.ToLower(strings.TrimSpace(glob))
	if glob == "" || glob == "*" {
		return ""
	}
	parts := strings.Split(glob, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return "^" + strings.Join(parts, ".*") + "$"
}

// Compile returns the Worker rules for the enabled rules. A rule's address is
// either empty/"*" (any), a catch-all pattern (its domain is one of ours), or a
// real recipient such as any@example.com: that is encoded with the format of
// each inbox it applies to (its own inbox, or every inbox for "any inbox"), so
// the Worker matches exactly the catch-all addresses that decode to it. Inboxes
// that cannot express the address are skipped; duplicate destinations are
// collapsed at match time.
func Compile(list []*store.ForwardRule, inboxes []*store.Inbox, mainDomain string) []WorkerRule {
	exprs := inboxExprs(inboxes, mainDomain)
	out := []WorkerRule{}
	for _, r := range list {
		if r.Enabled {
			out = append(out, compileRule(r, inboxes, exprs, mainDomain)...)
		}
	}
	return out
}

func compileRule(r *store.ForwardRule, inboxes []*store.Inbox, exprs map[int64]WorkerRule, mainDomain string) []WorkerRule {
	dest := strings.ToLower(r.Destination)
	inScope := func(in *store.Inbox) (WorkerRule, bool) {
		e, ok := exprs[in.ID]
		return WorkerRule{All: slices.Clone(e.All), None: slices.Clone(e.None), Destination: dest}, ok
	}
	glob := strings.ToLower(strings.TrimSpace(r.AddressGlob))

	if glob == "" || glob == "*" || IsCatchAllPattern(glob, inboxes, mainDomain) {
		wr := WorkerRule{All: []string{}, None: []string{}, Destination: dest}
		if r.InboxID != 0 {
			e, ok := exprs[r.InboxID]
			if !ok {
				return nil
			}
			wr.All, wr.None = append(wr.All, e.All...), append(wr.None, e.None...)
		}
		if g := GlobRegex(glob); g != "" {
			wr.All = append(wr.All, g)
		}
		return []WorkerRule{wr}
	}

	// A real recipient: one variant per inbox, with that inbox's encoding.
	var out []WorkerRule
	for _, in := range inboxes {
		if r.InboxID != 0 && in.ID != r.InboxID {
			continue
		}
		f, err := address.For(in)
		if err != nil {
			continue
		}
		local, err := encodePattern(f, glob)
		if err != nil || !listAccepts(in, f, glob, mainDomain) {
			continue
		}
		wr, ok := inScope(in)
		if !ok {
			continue
		}
		wr.All = append(wr.All, "^"+local+"@"+regexp.QuoteMeta(address.CatchDomain(in, mainDomain))+"$")
		out = append(out, wr)
	}
	return out
}

// IsCatchAllPattern reports whether a rule address names our own catch-all
// addresses (its domain is the main domain or an inbox's own domain).
func IsCatchAllPattern(glob string, inboxes []*store.Inbox, mainDomain string) bool {
	_, domain, ok := address.Split(glob)
	if !ok {
		return true // "*", "team1-*" and the like: treat as a pattern
	}
	if strings.EqualFold(domain, mainDomain) || domain == "*" {
		return true
	}
	for _, in := range inboxes {
		if in.Domain != "" && strings.EqualFold(in.Domain, domain) {
			return true
		}
	}
	return false
}

// starMarker stands in for "*" while encoding: letters only, so every format accepts it.
const starMarker = "zqzstarzqz"

// encodePattern encodes a real address that may use "*" in its local part and
// returns the encoded local part as a regex.
func encodePattern(f *address.Format, real string) (string, error) {
	local, domain, ok := address.Split(real)
	if !ok || strings.Contains(domain, "*") {
		return "", address.ErrUnsupported
	}
	enc, err := f.Encode(strings.ReplaceAll(local, "*", starMarker) + "@" + domain)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(regexp.QuoteMeta(enc), starMarker, ".*"), nil
}

// listAccepts: an inbox with an address list only receives listed addresses, so
// the encoded address (a sample, for wildcards) must be on its list.
func listAccepts(in *store.Inbox, f *address.Format, real, mainDomain string) bool {
	if len(in.Addresses) == 0 {
		return true
	}
	local, domain, _ := address.Split(real)
	enc, err := f.Encode(strings.ReplaceAll(local, "*", "x") + "@" + domain)
	return err == nil && address.MatchesAny(in.Addresses, enc+"@"+address.CatchDomain(in, mainDomain))
}

// Reach returns the names of the inboxes a rule can match in (for the UI);
// empty means the rule never fires.
func Reach(r *store.ForwardRule, inboxes []*store.Inbox, mainDomain string) []string {
	exprs := inboxExprs(inboxes, mainDomain)
	glob := strings.ToLower(strings.TrimSpace(r.AddressGlob))
	var out []string
	for _, in := range inboxes {
		if r.InboxID != 0 && in.ID != r.InboxID {
			continue
		}
		if _, ok := exprs[in.ID]; !ok {
			continue
		}
		if glob != "" && glob != "*" && !IsCatchAllPattern(glob, inboxes, mainDomain) {
			f, err := address.For(in)
			if err != nil {
				continue
			}
			if _, err := encodePattern(f, glob); err != nil || !listAccepts(in, f, glob, mainDomain) {
				continue
			}
		}
		out = append(out, in.Name)
	}
	return out
}

// Match returns the destinations the Worker would forward rcpt to, in rule order, without duplicates.
func Match(compiled []WorkerRule, rcpt string) []string {
	rcpt = strings.ToLower(strings.TrimSpace(rcpt))
	test := func(re string) bool {
		r, err := regexp.Compile("(?i)" + re)
		return err == nil && r.MatchString(rcpt)
	}
	var out []string
	for _, r := range compiled {
		if !all(r.All, test) || slices.ContainsFunc(r.None, test) {
			continue
		}
		if !slices.Contains(out, r.Destination) {
			out = append(out, r.Destination)
		}
	}
	return out
}

func all(list []string, f func(string) bool) bool {
	for _, s := range list {
		if !f(s) {
			return false
		}
	}
	return true
}
