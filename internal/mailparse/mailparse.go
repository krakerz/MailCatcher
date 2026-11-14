// Package mailparse turns raw MIME into the fields mailcatcher stores and shows.
package mailparse

import (
	"bytes"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jhillyerd/enmime/v2"
	"golang.org/x/net/html"
)

type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Inline      bool
	Data        []byte
}

type Parsed struct {
	MessageID   string
	InReplyTo   string
	References  []string
	From        Address
	To          []Address
	Cc          []Address
	Subject     string
	Date        time.Time // zero if missing/unparseable
	Text        string
	HTML        string
	Links       []string
	Codes       []string
	Attachments []Attachment
}

func Parse(raw []byte) (*Parsed, error) {
	e, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	p := &Parsed{
		MessageID:  strings.TrimSpace(e.GetHeader("Message-ID")),
		InReplyTo:  strings.TrimSpace(e.GetHeader("In-Reply-To")),
		References: msgIDs(e.GetHeader("References")),
		Subject:    e.GetHeader("Subject"),
		To:         addrs(e, "To"),
		Cc:         addrs(e, "Cc"),
		Text:       e.Text,
		HTML:       e.HTML,
	}
	if from := addrs(e, "From"); len(from) > 0 {
		p.From = from[0]
	}
	if d, err := e.Date(); err == nil {
		p.Date = d
	}
	for _, part := range e.Attachments {
		p.Attachments = append(p.Attachments, attachment(part, false))
	}
	for _, part := range e.Inlines {
		p.Attachments = append(p.Attachments, attachment(part, true))
	}
	// Images inside multipart/related usually land here rather than in Inlines.
	for _, part := range e.OtherParts {
		if part.ContentID != "" || part.FileName != "" {
			p.Attachments = append(p.Attachments, attachment(part, part.ContentID != ""))
		}
	}
	p.Links = Links(p.HTML, p.Text)
	p.Codes = Codes(p.Text)
	return p, nil
}

func attachment(part *enmime.Part, inline bool) Attachment {
	return Attachment{
		Filename:    part.FileName,
		ContentType: part.ContentType,
		ContentID:   part.ContentID,
		Inline:      inline,
		Data:        part.Content,
	}
}

func addrs(e *enmime.Envelope, key string) []Address {
	list, err := e.AddressList(key)
	if err != nil {
		// Malformed header: keep the raw value so it is still visible and searchable.
		if v := strings.TrimSpace(e.GetHeader(key)); v != "" {
			return []Address{{Address: v}}
		}
		return nil
	}
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Address: strings.ToLower(a.Address)})
	}
	return out
}

var msgIDRe = regexp.MustCompile(`<[^<>\s]+>`)

func msgIDs(h string) []string {
	return msgIDRe.FindAllString(h, -1)
}

var textURLRe = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

// Links returns unique http(s) links, HTML hrefs first, then URLs found in the text.
func Links(htmlBody, text string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), ".,;:!?")
		if u == "" || seen[u] {
			return
		}
		if l := strings.ToLower(u); !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	if htmlBody != "" {
		z := html.NewTokenizer(strings.NewReader(htmlBody))
		for {
			tt := z.Next()
			if tt == html.ErrorToken {
				break
			}
			if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
				continue
			}
			name, hasAttr := z.TagName()
			if string(name) != "a" || !hasAttr {
				continue
			}
			for {
				k, v, more := z.TagAttr()
				if string(k) == "href" {
					add(string(v))
				}
				if !more {
					break
				}
			}
		}
	}
	for _, u := range textURLRe.FindAllString(text, -1) {
		add(u)
	}
	return out
}

var (
	codeKeywordRe = regexp.MustCompile(`(?i)\b(code|otp|pin|passcode|password|token|verification|verify|one[- ]time)\b`)
	codeCandRe    = regexp.MustCompile(`\b[A-Z0-9]{4,10}\b`)
	digitsRe      = regexp.MustCompile(`^[0-9]+$`)
	hasDigitRe    = regexp.MustCompile(`[0-9]`)
	hasLetterRe   = regexp.MustCompile(`[A-Z]`)
)

// Window after a keyword in which a code is accepted ("Your code is: 123456").
const codeWindow = 80

// Only the head of a mail is scanned for codes: they sit near the top, and
// scanning a huge body is wasted work.
const maxCodeScan = 256 << 10

const maxCodes = 20

// Codes extracts likely verification codes: 4–8 digit numbers or 6–10 char
// uppercase alphanumerics (with at least one digit and one letter) that follow a
// code-ish keyword closely, plus digit-only codes standing alone on a line.
func Codes(text string) []string {
	if len(text) > maxCodeScan {
		text = text[:maxCodeScan]
		// Drop a rune cut in half by the byte limit.
		for len(text) > 0 && !utf8.RuneStart(text[len(text)-1]) {
			text = text[:len(text)-1]
		}
	}
	// Blank out URLs (keeping offsets) so query-string numbers are not taken as codes.
	text = textURLRe.ReplaceAllStringFunc(text, func(u string) string { return strings.Repeat(" ", len(u)) })
	kw := codeKeywordRe.FindAllStringIndex(text, -1)
	seen := map[string]bool{}
	var out []string
	ki := 0 // kw[ki] is the last keyword ending at or before the candidate start
	for _, m := range codeCandRe.FindAllStringIndex(text, -1) {
		c := text[m[0]:m[1]]
		digits := digitsRe.MatchString(c)
		switch {
		case digits && len(c) <= 8:
		case !digits && len(c) >= 6 && hasDigitRe.MatchString(c) && hasLetterRe.MatchString(c):
		default:
			continue
		}
		// Candidates ascend, so ki only moves forward.
		for ki+1 < len(kw) && kw[ki+1][1] <= m[0] {
			ki++
		}
		ok := ki < len(kw) && kw[ki][1] <= m[0] && m[0]-kw[ki][1] <= codeWindow
		if !ok && digits && aloneOnLine(text, m[0], m[1]) {
			ok = true
		}
		if ok && !seen[c] {
			seen[c] = true
			out = append(out, c)
			if len(out) >= maxCodes {
				break
			}
		}
	}
	return out
}

func aloneOnLine(s string, start, end int) bool {
	ls := strings.LastIndexByte(s[:start], '\n') + 1
	le := strings.IndexByte(s[end:], '\n')
	if le < 0 {
		le = len(s)
	} else {
		le += end
	}
	return strings.TrimSpace(s[ls:start]) == "" && strings.TrimSpace(s[end:le]) == ""
}
