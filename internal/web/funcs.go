package web

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"mailcatcher/internal/mailparse"
)

var iconName = regexp.MustCompile(`^[a-z0-9-]+$`)

var funcs = template.FuncMap{
	// icon renders a Lucide icon from /static/icons.svg.
	"icon": func(name string) template.HTML {
		if !iconName.MatchString(name) {
			return ""
		}
		return template.HTML(`<svg class="icon" aria-hidden="true"><use href="/static/icons.svg?v=` + assetVersion + `#` + name + `"></use></svg>`)
	},
	// initial is the avatar letter for a sender.
	"initial": func(name, addr string) string {
		for _, s := range []string{name, addr} {
			for _, r := range strings.TrimSpace(s) {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					return strings.ToUpper(string(r))
				}
			}
		}
		return "?"
	},
	// hue picks one of 12 avatar colour classes (.h0–.h11), stable per sender;
	// classes because the CSP forbids inline style attributes.
	"hue": func(s string) int {
		h := fnv.New32a()
		h.Write([]byte(strings.ToLower(s)))
		return int(h.Sum32() % 12)
	},
	"hasPrefix": strings.HasPrefix,
	"hasID":     func(ids []int64, id int64) bool { return slices.Contains(ids, id) },
	// asset is a cache-busted /static URL.
	"asset": func(name string) string { return "/static/" + name + "?v=" + assetVersion },
	"when": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		if d := time.Since(t); d < 24*time.Hour && d >= 0 {
			return t.Local().Format("15:04:05")
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	"full": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Local().Format("Mon, 02 Jan 2006 15:04:05 -0700")
	},
	"addr": func(a mailparse.Address) string {
		if a.Name == "" {
			return a.Address
		}
		return a.Name + " <" + a.Address + ">"
	},
	"addrs": func(list []mailparse.Address) string {
		out := make([]string, len(list))
		for i, a := range list {
			out[i] = a.Address
			if a.Name != "" {
				out[i] = a.Name + " <" + a.Address + ">"
			}
		}
		return strings.Join(out, ", ")
	},
	"size": func(n int64) string {
		switch {
		case n >= 1<<20:
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		case n >= 1<<10:
			return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
		}
		return fmt.Sprintf("%d B", n)
	},
	// dict builds a map for passing several values to a sub-template.
	"dict": func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"deref": func(p *int64) int64 { return *p },
	"orNone": func(s string) string {
		if s == "" {
			return "(no subject)"
		}
		return s
	},
}
