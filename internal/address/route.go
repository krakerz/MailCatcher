package address

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"mailcatcher/internal/store"
)

// Route picks the inbox for a recipient and decodes the real address it stands for.
// Non-default inboxes are tried by priority (then id); the first whose domain and
// format match wins. Otherwise the default inbox takes the mail, with a decoded
// address only if its own format matches. decoded is "" when nothing decodes.
func Route(inboxes []*store.Inbox, mainDomain, rcpt string) (inboxID int64, decoded string) {
	local, domain, ok := Split(rcpt)
	mainDomain = strings.ToLower(mainDomain)
	ordered := slices.Clone(inboxes)
	slices.SortStableFunc(ordered, func(a, b *store.Inbox) int {
		if a.IsDefault != b.IsDefault {
			if a.IsDefault {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(a.ID, b.ID))
	})

	var def *store.Inbox
	for _, in := range ordered {
		if in.IsDefault {
			def = in
		}
		if !ok || !domainMatches(in, mainDomain, domain) {
			continue
		}
		f, err := Compile(in.FormatTemplate, in.AssumedTLD, in.Slug)
		if err != nil {
			continue
		}
		if real, matched := f.Decode(local); matched {
			return in.ID, real
		}
		if in.IsDefault {
			return in.ID, ""
		}
	}
	if def != nil {
		return def.ID, ""
	}
	if len(ordered) > 0 {
		return ordered[0].ID, ""
	}
	return 0, ""
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
