package mailparse

import (
	"reflect"
	"strings"
	"testing"
)

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

const multipartMail = `From: "Alice Example" <Alice@Example.com>
To: b.gmail@example.test, "Carol" <carol@example.org>
Cc: dave@example.net
Subject: =?UTF-8?Q?Your_c=C3=B3de?=
Date: Fri, 16 Oct 2026 17:35:16 +0700
Message-ID: <abc123@mail.example.com>
In-Reply-To: <prev@example.test>
References: <first@x.com>
 <prev@example.test>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="mixed"

--mixed
Content-Type: multipart/related; boundary="rel"

--rel
Content-Type: multipart/alternative; boundary="alt"

--alt
Content-Type: text/plain; charset=utf-8

Your verification code is 482913.
Open https://app.example.com/verify?t=abc.
--alt
Content-Type: text/html; charset=utf-8

<p>Your verification code is <b>482913</b>.</p>
<a href="https://app.example.com/verify?t=abc">Verify</a>
<a href="mailto:help@example.com">Help</a>
<img src="cid:logo@x">
--alt--
--rel
Content-Type: image/png
Content-ID: <logo@x>
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--rel--
--mixed
Content-Type: text/plain; name="report.txt"
Content-Disposition: attachment; filename="report.txt"

hello attachment
--mixed--
`

func TestParseMultipart(t *testing.T) {
	p, err := Parse(crlf(multipartMail))
	if err != nil {
		t.Fatal(err)
	}
	if p.MessageID != "<abc123@mail.example.com>" || p.InReplyTo != "<prev@example.test>" {
		t.Errorf("ids: %q %q", p.MessageID, p.InReplyTo)
	}
	if want := []string{"<first@x.com>", "<prev@example.test>"}; !reflect.DeepEqual(p.References, want) {
		t.Errorf("references: %v", p.References)
	}
	if p.From != (Address{Name: "Alice Example", Address: "alice@example.com"}) {
		t.Errorf("from: %+v", p.From)
	}
	if len(p.To) != 2 || p.To[0].Address != "b.gmail@example.test" || p.To[1].Name != "Carol" {
		t.Errorf("to: %+v", p.To)
	}
	if len(p.Cc) != 1 || p.Cc[0].Address != "dave@example.net" {
		t.Errorf("cc: %+v", p.Cc)
	}
	if p.Subject != "Your códe" {
		t.Errorf("subject: %q", p.Subject)
	}
	if p.Date.IsZero() || p.Date.UTC().Hour() != 10 {
		t.Errorf("date: %v", p.Date)
	}
	if !strings.Contains(p.Text, "482913") || !strings.Contains(p.HTML, "<b>482913</b>") {
		t.Errorf("bodies: %q / %q", p.Text, p.HTML)
	}
	if want := []string{"https://app.example.com/verify?t=abc"}; !reflect.DeepEqual(p.Links, want) {
		t.Errorf("links: %v", p.Links)
	}
	if want := []string{"482913"}; !reflect.DeepEqual(p.Codes, want) {
		t.Errorf("codes: %v", p.Codes)
	}

	var gotAtt, gotInline bool
	for _, a := range p.Attachments {
		switch {
		case a.Filename == "report.txt" && !a.Inline && string(a.Data) == "hello attachment":
			gotAtt = true
		case a.ContentID == "logo@x" && a.Inline && a.ContentType == "image/png":
			gotInline = true
		}
	}
	if !gotAtt || !gotInline {
		t.Errorf("attachments: %+v", p.Attachments)
	}
}

func TestParseHTMLOnlyGetsText(t *testing.T) {
	p, err := Parse(crlf("From: a@b.c\nSubject: x\nContent-Type: text/html\n\n<p>Your OTP: 7731</p>\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "7731") {
		t.Errorf("text not derived from html: %q", p.Text)
	}
	if want := []string{"7731"}; !reflect.DeepEqual(p.Codes, want) {
		t.Errorf("codes: %v", p.Codes)
	}
}

func TestCodes(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"Your code is 123456", []string{"123456"}},
		{"Use one-time password: AB12CD34 to sign in", []string{"AB12CD34"}},
		{"Hi,\n\n  884422  \n\nThanks", []string{"884422"}},
		{"Order 12345 shipped on 2026-10-16", nil},
		{"Verification code: 1111. Your code again: 1111", []string{"1111"}},
		{"Your code is ABCDEF", nil},          // letters only
		{"PIN 123456789012 is too long", nil}, // > 10 chars
	}
	for _, c := range cases {
		if got := Codes(c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Codes(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestLinksDedupAndTrim(t *testing.T) {
	got := Links(`<a href="https://x.io/a">a</a><a href="javascript:x()">b</a>`, "see https://x.io/a, and http://y.io/b.")
	if want := []string{"https://x.io/a", "http://y.io/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}
