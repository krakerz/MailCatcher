// Package address maps real addresses (b@gmail.com) to catch-all addresses
// (b.gmail@catch.example) and back, using per-inbox format templates.
//
// Tokens: {local} real local part, {domain} real domain, {name} real domain without
// its last label (single label only: gmail.com → gmail), {tld} the last label,
// {inbox} the inbox slug. Everything else in the template is literal.
package address

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Presets offered in the UI.
var Presets = []struct{ Template, Example string }{
	{"{local}.{name}", "b.gmail — Mailosaur style, needs an assumed TLD"},
	{"{local}={domain}", "b=gmail.com"},
	{"{local}+{domain}", "b+gmail.com"},
	{"{inbox}-{local}={domain}", "team1-b=gmail.com — inbox marker, several inboxes on one domain"},
}

var tokenRe = regexp.MustCompile(`\{[a-z]+\}`)

var tokenPattern = map[string]string{
	"{local}":  `(.+)`, // greedy: with an ambiguous separator the last one splits
	"{domain}": `([a-z0-9-]+(?:\.[a-z0-9-]+)+)`,
	"{name}":   `([a-z0-9-]+)`,
	"{tld}":    `([a-z0-9-]+)`,
}

// Format is a compiled template, bound to one inbox slug.
type Format struct {
	Template   string
	AssumedTLD string
	Slug       string

	re     *regexp.Regexp
	groups []string // token per capture group, in order
}

func Compile(template, assumedTLD, slug string) (*Format, error) {
	template = strings.TrimSpace(strings.ToLower(template))
	assumedTLD = strings.Trim(strings.ToLower(strings.TrimSpace(assumedTLD)), ".")
	f := &Format{Template: template, AssumedTLD: assumedTLD, Slug: strings.ToLower(slug)}

	seen := map[string]bool{}
	var pat strings.Builder
	pat.WriteString("^")
	last := 0
	for _, loc := range tokenRe.FindAllStringIndex(template, -1) {
		lit := template[last:loc[0]]
		if strings.ContainsAny(lit, "{}@ ") {
			return nil, fmt.Errorf("invalid characters in %q", lit)
		}
		pat.WriteString(regexp.QuoteMeta(lit))
		tok := template[loc[0]:loc[1]]
		if seen[tok] {
			return nil, fmt.Errorf("%s used twice", tok)
		}
		seen[tok] = true
		switch tok {
		case "{inbox}":
			if f.Slug == "" {
				return nil, errors.New("{inbox} needs an inbox slug")
			}
			pat.WriteString(regexp.QuoteMeta(f.Slug))
		case "{local}", "{domain}", "{name}", "{tld}":
			pat.WriteString(tokenPattern[tok])
			f.groups = append(f.groups, tok)
		default:
			return nil, fmt.Errorf("unknown token %s", tok)
		}
		last = loc[1]
	}
	lit := template[last:]
	if strings.ContainsAny(lit, "{}@ ") {
		return nil, fmt.Errorf("invalid characters in %q", lit)
	}
	pat.WriteString(regexp.QuoteMeta(lit))
	pat.WriteString("$")

	switch {
	case !seen["{local}"]:
		return nil, errors.New("template needs {local}")
	case seen["{domain}"] && (seen["{name}"] || seen["{tld}"]):
		return nil, errors.New("use either {domain} or {name}, not both")
	case !seen["{domain}"] && !seen["{name}"]:
		return nil, errors.New("template needs {domain} or {name}")
	case seen["{tld}"] && !seen["{name}"]:
		return nil, errors.New("{tld} only makes sense with {name}")
	case seen["{name}"] && !seen["{tld}"] && f.AssumedTLD == "":
		return nil, errors.New("{name} without {tld} needs an assumed TLD (e.g. com)")
	}
	var err error
	if f.re, err = regexp.Compile("(?i)" + pat.String()); err != nil {
		return nil, err
	}
	return f, nil
}

// Decode turns a catch-all local part into the real address. ok is false when
// the local part does not match this format.
func (f *Format) Decode(local string) (real string, ok bool) {
	m := f.re.FindStringSubmatch(strings.ToLower(local))
	if m == nil {
		return "", false
	}
	var l, domain, name, tld string
	for i, tok := range f.groups {
		switch tok {
		case "{local}":
			l = m[i+1]
		case "{domain}":
			domain = m[i+1]
		case "{name}":
			name = m[i+1]
		case "{tld}":
			tld = m[i+1]
		}
	}
	if domain == "" {
		if tld == "" {
			tld = f.AssumedTLD
		}
		domain = name + "." + tld
	}
	return l + "@" + domain, true
}

// ErrUnsupported matches (errors.Is) every "address doesn't fit this format" error.
var ErrUnsupported = errors.New("address does not fit this format")

type unsupported struct{ msg string }

func (e unsupported) Error() string        { return e.msg }
func (e unsupported) Is(target error) bool { return target == ErrUnsupported }

func doesNotFit(real, why string, args ...any) error {
	return unsupported{real + " doesn't fit: " + fmt.Sprintf(why, args...)}
}

// Encode returns the catch-all local part for a real address. It verifies that
// the result decodes back to the same address, so ambiguous encodings are refused.
func (f *Format) Encode(real string) (string, error) {
	real = strings.ToLower(strings.TrimSpace(real))
	at := strings.LastIndexByte(real, '@')
	if at <= 0 || at == len(real)-1 {
		return "", fmt.Errorf("%q is not an email address", real)
	}
	local, domain := real[:at], real[at+1:]
	labels := strings.Split(domain, ".")
	r := strings.NewReplacer(
		"{local}", local,
		"{domain}", domain,
		"{inbox}", f.Slug,
		"{name}", labels[0],
		"{tld}", labels[len(labels)-1],
	)
	if strings.Contains(f.Template, "{name}") {
		switch {
		case len(labels) != 2:
			return "", doesNotFit(real, "{name} formats only cover two-part domains like gmail.com")
		case !strings.Contains(f.Template, "{tld}") && labels[1] != f.AssumedTLD:
			return "", doesNotFit(real, "this format only covers .%s addresses (assumed TLD)", f.AssumedTLD)
		}
	}
	enc := r.Replace(f.Template)
	if back, ok := f.Decode(enc); !ok || back != real {
		return "", doesNotFit(real, "%s would be read back as %s", enc, back)
	}
	return enc, nil
}

// Warnings describes ambiguity for the settings page.
func (f *Format) Warnings() []string {
	var out []string
	idx := strings.Index(f.Template, "{local}")
	after := f.Template[idx+len("{local}"):]
	if sep, rest, found := strings.Cut(after, "{"); found && sep != "" {
		if strings.ContainsAny(sep, ".+-_") {
			out = append(out, fmt.Sprintf("Real local parts can contain %q too; decoding splits at the last one.", sep))
		}
		if strings.HasPrefix(rest, "domain}") && strings.ContainsAny(sep, ".-") {
			out = append(out, fmt.Sprintf("Domains contain %q as well, so some addresses (e.g. multi-part domains) are misread and refused when composing.", sep))
		}
	}
	if strings.Contains(f.Template, "{name}") && !strings.Contains(f.Template, "{tld}") {
		out = append(out, fmt.Sprintf("Only addresses at <name>.%s domains can be represented (e.g. gmail.%s); others such as yahoo.co.id cannot.", f.AssumedTLD, f.AssumedTLD))
	}
	return out
}

// Split returns the local part and lower-cased domain of an address.
func Split(addr string) (local, domain string, ok bool) {
	addr = strings.TrimSpace(addr)
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return "", "", false
	}
	return addr[:at], strings.ToLower(addr[at+1:]), true
}
