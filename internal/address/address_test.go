package address

import (
	"errors"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, tpl, tld, slug string) *Format {
	t.Helper()
	f, err := Compile(tpl, tld, slug)
	if err != nil {
		t.Fatalf("Compile(%q): %v", tpl, err)
	}
	return f
}

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		tpl, tld, slug, real, local string
	}{
		{"{local}.{name}", "com", "", "b@gmail.com", "b.gmail"},
		{"{local}.{name}", "com", "", "john.doe@gmail.com", "john.doe.gmail"},
		{"{local}={domain}", "", "", "b@gmail.com", "b=gmail.com"},
		{"{local}={domain}", "", "", "b.c@mail.co.uk", "b.c=mail.co.uk"},
		{"{local}+{domain}", "", "", "b+tag@gmail.com", "b+tag+gmail.com"},
		{"{inbox}-{local}={domain}", "", "team-1", "b@gmail.com", "team-1-b=gmail.com"},
		{"{local}.{name}.{tld}", "", "", "b@yahoo.de", "b.yahoo.de"},
		{"{local}_at_{name}", "net", "", "x@example.net", "x_at_example"},
		{"{inbox}-{domain}~{local}", "", "team1", "any@gmail.com", "team1-gmail.com~any"},
		{"{inbox}-{domain}~{local}", "", "team1", "a.b@mail.co.uk", "team1-mail.co.uk~a.b"},
	}
	for _, c := range cases {
		f := mustCompile(t, c.tpl, c.tld, c.slug)
		got, err := f.Encode(c.real)
		if err != nil || got != c.local {
			t.Errorf("%s Encode(%s) = %q, %v; want %q", c.tpl, c.real, got, err, c.local)
			continue
		}
		back, ok := f.Decode(got)
		if !ok || back != c.real {
			t.Errorf("%s Decode(%s) = %q, %v; want %q", c.tpl, got, back, ok, c.real)
		}
	}
}

func TestDecodeCaseAndMismatch(t *testing.T) {
	f := mustCompile(t, "{local}.{name}", "com", "")
	if got, ok := f.Decode("B.Gmail"); !ok || got != "b@gmail.com" {
		t.Errorf("case-insensitive decode: %q %v", got, ok)
	}
	if _, ok := f.Decode("nodots"); ok {
		t.Error("matched without separator")
	}
	g := mustCompile(t, "{inbox}-{local}={domain}", "", "team1")
	if _, ok := g.Decode("team2-b=gmail.com"); ok {
		t.Error("matched another inbox's marker")
	}
}

func TestEncodeUnsupported(t *testing.T) {
	f := mustCompile(t, "{local}.{name}", "com", "")
	if _, err := f.Encode("b@gmail.net"); err == nil || err.Error() != "b@gmail.net doesn't fit: this format only covers .com addresses (assumed TLD)" || !errors.Is(err, ErrUnsupported) {
		t.Errorf("unclear error: %v", err)
	}
	for _, real := range []string{"b@yahoo.co.id", "b@web.de", "not-an-address"} {
		if _, err := f.Encode(real); err == nil {
			t.Errorf("Encode(%s) should fail", real)
		}
	}
	// '.' separates local and domain but also appears inside the domain:
	// b.mail.co.uk would read back as b.mail@co.uk.
	g := mustCompile(t, "{local}.{domain}", "", "")
	if _, err := g.Encode("b@mail.co.uk"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ambiguous encode not refused: %v", err)
	}
}

func TestCompileErrors(t *testing.T) {
	for _, tpl := range []string{
		"{domain}",                 // no local
		"{local}",                  // no domain/name
		"{local}.{name}",           // name without assumed tld (tld passed empty below)
		"{local}={domain}.{name}",  // both
		"{local}.{tld}={domain}",   // tld without name
		"{local}{local}={domain}",  // duplicate
		"{local}={bogus}",          // unknown token
		"{local}@{domain}",         // @ literal
		"{inbox}-{local}={domain}", // inbox without slug
	} {
		if _, err := Compile(tpl, "", ""); err == nil {
			t.Errorf("Compile(%q) should fail", tpl)
		}
	}
}

func TestWarnings(t *testing.T) {
	if w := mustCompile(t, "{local}.{name}", "com", "").Warnings(); len(w) != 2 {
		t.Errorf("want 2 warnings, got %v", w)
	}
	if w := mustCompile(t, "{local}={domain}", "", "").Warnings(); len(w) != 0 {
		t.Errorf("want none, got %v", w)
	}
}

func TestPrefixWarning(t *testing.T) {
	f := mustCompile(t, "{domain}~{local}", "", "team1")
	w := f.PrefixWarning([]string{"team1-*@main.test"})
	if !strings.Contains(w, `"{inbox}-{domain}~{local}"`) {
		t.Errorf("no {inbox} suggestion: %q", w)
	}
	g := mustCompile(t, "{inbox}-{domain}~{local}", "", "team1")
	if w := g.PrefixWarning([]string{"team1-*@main.test"}); w != "" {
		t.Errorf("false positive: %q", w)
	}
	// Exact addresses and bare wildcards are normal pins, not prefixes.
	if w := mustCompile(t, "{local}.{name}", "com", "a").PrefixWarning([]string{"*@main.test", "b.gmail@main.test"}); w != "" {
		t.Errorf("false positive: %q", w)
	}
}
