package address

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"

	"mailcatcher/internal/store"
)

// Route picks the inbox for a recipient and decodes the real address it stands for.
// Order: inboxes with an address list (by priority, then id) match only their
// listed addresses; then inboxes without one match by domain and format; the
// default inbox takes the rest. decoded is "" when nothing decodes.
func Route(inboxes []*store.Inbox, mainDomain, rcpt string) (inboxID int64, decoded string) {
	local, domain, ok := Split(rcpt)
	mainDomain = strings.ToLower(mainDomain)
	decode := func(in *store.Inbox) string {
		if !ok {
			return ""
		}
		f, err := Compile(in.FormatTemplate, in.AssumedTLD, in.Slug)
		if err != nil {
			return ""
		}
		real, _ := f.Decode(local)
		return real
	}

	for _, in := range RouteOrder(inboxes) {
		switch {
		case in.IsDefault: // last in RouteOrder; takes everything left, address list or not
			if ok && domainMatches(in, mainDomain, domain) {
				return in.ID, decode(in)
			}
			return in.ID, ""
		case len(in.Addresses) > 0:
			if MatchesAny(in.Addresses, rcpt) {
				return in.ID, decode(in)
			}
		case ok && domainMatches(in, mainDomain, domain):
			if real := decode(in); real != "" {
				return in.ID, real
			}
		}
	}
	return 0, ""
}

// RouteOrder is the order Route tries inboxes in: address lists first, then
// format/domain inboxes, each by priority then id, and the default inbox last.
// Without a default inbox the first inbox serves as one.
func RouteOrder(inboxes []*store.Inbox) []*store.Inbox {
	ordered := slices.Clone(inboxes)
	rank := func(in *store.Inbox) int {
		switch {
		case in.IsDefault:
			return 2
		case len(in.Addresses) > 0:
			return 0
		}
		return 1
	}
	slices.SortStableFunc(ordered, func(a, b *store.Inbox) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a.Priority, b.Priority), cmp.Compare(a.ID, b.ID))
	})
	if len(ordered) > 0 && !ordered[len(ordered)-1].IsDefault {
		fallback := *ordered[0]
		fallback.IsDefault, fallback.Addresses = true, nil
		ordered = append(ordered, &fallback)
	}
	return ordered
}

// GlobPattern turns a catch-all address glob ("*" wildcard) into an unanchored,
// RE2/JavaScript-compatible regex.
func GlobPattern(glob string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(glob)), "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(parts, ".*")
}

// MatchesAny reports whether addr matches one of the globs (case-insensitive).
func MatchesAny(globs []string, addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	for _, g := range globs {
		if re, err := regexp.Compile("^(?:" + GlobPattern(g) + ")$"); err == nil && re.MatchString(addr) {
			return true
		}
	}
	return false
}

func domainMatches(in *store.Inbox, mainDomain, domain string) bool {
	if in.Domain != "" {
		return strings.EqualFold(in.Domain, domain)
	}
	return domain == mainDomain
}

// For returns the compiled format of an inbox.
func For(in *store.Inbox) (*Format, error) {
	return Compile(in.FormatTemplate, in.AssumedTLD, in.Slug)
}

// CatchDomain is the domain an inbox receives on.
func CatchDomain(in *store.Inbox, mainDomain string) string {
	if in.Domain != "" {
		return strings.ToLower(in.Domain)
	}
	return strings.ToLower(mainDomain)
}

// Reroute applies the current inbox settings to all stored inbound mail and
// returns how many messages changed inbox or decoded address.
func Reroute(ctx context.Context, st *store.Store, mainDomain string) (int, error) {
	inboxes, err := st.ListInboxes(ctx)
	if err != nil {
		return 0, err
	}
	current, err := st.InboundRouting(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for id, cur := range current {
		inbox, decoded := Route(inboxes, mainDomain, cur.EnvelopeTo)
		if cur.InboxID == inbox && cur.DecodedTo == decoded {
			continue
		}
		if err := st.SetRouting(ctx, id, inbox, decoded); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}
