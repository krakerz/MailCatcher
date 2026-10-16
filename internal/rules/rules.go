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

// Compile returns the Worker rules for the enabled rules. A rule whose inbox no
// longer exists or cannot match is dropped.
func Compile(list []*store.ForwardRule, inboxes []*store.Inbox, mainDomain string) []WorkerRule {
	exprs := inboxExprs(inboxes, mainDomain)
	out := []WorkerRule{}
	for _, r := range list {
		if !r.Enabled {
			continue
		}
		wr := WorkerRule{All: []string{}, None: []string{}, Destination: strings.ToLower(r.Destination)}
		if r.InboxID != 0 {
			e, ok := exprs[r.InboxID]
			if !ok {
				continue
			}
			wr.All = append(wr.All, e.All...)
			wr.None = append(wr.None, e.None...)
		}
		if g := GlobRegex(r.AddressGlob); g != "" {
			wr.All = append(wr.All, g)
		}
		out = append(out, wr)
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
