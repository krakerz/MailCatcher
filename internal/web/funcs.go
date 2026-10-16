package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"mailcatcher/internal/mailparse"
)

var funcs = template.FuncMap{
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
	"orNone": func(s string) string {
		if s == "" {
			return "(no subject)"
		}
		return s
	},
}
