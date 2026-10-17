package outbound

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Inline is an image embedded in an HTML body and referenced as cid:<ContentID>.
type Inline struct {
	File
	ContentID string
}

var allowedTags = map[atom.Atom]bool{
	atom.P: true, atom.Br: true, atom.Div: true, atom.Span: true, atom.B: true, atom.Strong: true,
	atom.I: true, atom.Em: true, atom.U: true, atom.S: true, atom.Strike: true, atom.A: true,
	atom.Ul: true, atom.Ol: true, atom.Li: true, atom.Blockquote: true, atom.H1: true, atom.H2: true,
	atom.H3: true, atom.Img: true, atom.Pre: true, atom.Code: true, atom.Hr: true,
}

// Dropped together with everything inside them.
var droppedTags = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Head: true, atom.Title: true}

var dataImageRe = regexp.MustCompile(`^data:(image/(?:png|jpeg|gif|webp));base64,(.+)$`)

// RichBody is a sanitized HTML body with its inline images and a text version.
type RichBody struct {
	HTML   string
	Text   string
	Inline []Inline
}

// PrepareHTML sanitizes editor HTML: only basic formatting tags, links with safe
// schemes, and images that are remote (http/https) or pasted data: images. The
// latter become inline attachments (cid:) because mail clients drop data: URLs.
func PrepareHTML(src, cidDomain string) (*RichBody, error) {
	nodes, err := nethtml.ParseFragment(strings.NewReader(src), &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return nil, err
	}
	rb := &RichBody{}
	var out bytes.Buffer
	for _, n := range nodes {
		if err := rb.clean(&out, n, cidDomain); err != nil {
			return nil, err
		}
	}
	rb.HTML = out.String()
	var text bytes.Buffer
	for _, n := range nodes {
		toText(&text, n)
	}
	rb.Text = strings.TrimSpace(collapseBlankLines(text.String())) + "\n"
	return rb, nil
}

func (rb *RichBody) clean(out *bytes.Buffer, n *nethtml.Node, cidDomain string) error {
	switch n.Type {
	case nethtml.TextNode:
		out.WriteString(html.EscapeString(n.Data))
		return nil
	case nethtml.ElementNode:
	default:
		return nil
	}
	if droppedTags[n.DataAtom] {
		return nil
	}
	keep := allowedTags[n.DataAtom]
	if keep {
		attrs, ok, err := rb.attrs(n, cidDomain)
		if err != nil {
			return err
		}
		if !ok {
			return nil // e.g. an image with an unusable src
		}
		out.WriteString("<" + n.Data + attrs + ">")
		if n.DataAtom == atom.Br || n.DataAtom == atom.Img || n.DataAtom == atom.Hr {
			return nil
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := rb.clean(out, c, cidDomain); err != nil {
			return err
		}
	}
	if keep {
		out.WriteString("</" + n.Data + ">")
	}
	return nil
}

// attrs returns the allowed attributes of n, or ok=false to drop the element.
func (rb *RichBody) attrs(n *nethtml.Node, cidDomain string) (string, bool, error) {
	get := func(key string) string {
		for _, a := range n.Attr {
			if a.Key == key {
				return strings.TrimSpace(a.Val)
			}
		}
		return ""
	}
	switch n.DataAtom {
	case atom.A:
		href := get("href")
		l := strings.ToLower(href)
		if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:") {
			return ` href="` + html.EscapeString(href) + `"`, true, nil
		}
		return "", true, nil
	case atom.Img:
		src := get("src")
		alt := ` alt="` + html.EscapeString(get("alt")) + `"` + imgSize(get("width"))
		if m := dataImageRe.FindStringSubmatch(src); m != nil {
			data, err := base64.StdEncoding.DecodeString(m[2])
			if err != nil {
				return "", false, nil
			}
			i := len(rb.Inline) + 1
			ext := strings.TrimPrefix(m[1], "image/")
			cid := fmt.Sprintf("image%d.%s@%s", i, randHex(), cidDomain)
			rb.Inline = append(rb.Inline, Inline{File: File{Name: fmt.Sprintf("image%d.%s", i, ext), ContentType: m[1], Data: data}, ContentID: cid})
			return ` src="cid:` + cid + `"` + alt + ` style="max-width:100%;height:auto"`, true, nil
		}
		if l := strings.ToLower(src); strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") {
			return ` src="` + html.EscapeString(src) + `"` + alt + ` style="max-width:100%;height:auto"`, true, nil
		}
		return "", false, nil
	}
	return "", true, nil
}

// imgSize renders the editor's chosen display width (1..4000 px, digits only);
// anything else is dropped. No height is set so the aspect ratio is kept.
func imgSize(w string) string {
	if n, err := strconv.Atoi(w); err == nil && n >= 1 && n <= 4000 && w == strconv.Itoa(n) {
		return fmt.Sprintf(` width="%d"`, n)
	}
	return ""
}

var blockTags = map[atom.Atom]bool{atom.P: true, atom.Div: true, atom.Li: true, atom.Blockquote: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.Pre: true, atom.Ul: true, atom.Ol: true, atom.Hr: true}

func toText(out *bytes.Buffer, n *nethtml.Node) {
	switch {
	case n.Type == nethtml.TextNode:
		out.WriteString(n.Data)
		return
	case n.Type != nethtml.ElementNode || droppedTags[n.DataAtom]:
		return
	case n.DataAtom == atom.Br:
		out.WriteString("\n")
		return
	case n.DataAtom == atom.Img:
		out.WriteString("[image]")
		return
	}
	if blockTags[n.DataAtom] {
		out.WriteString("\n")
	}
	if n.DataAtom == atom.Li {
		out.WriteString("- ")
	}
	var inner bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		toText(&inner, c)
	}
	if n.DataAtom == atom.Blockquote {
		for _, l := range strings.Split(strings.Trim(inner.String(), "\n"), "\n") {
			out.WriteString("> " + l + "\n")
		}
	} else {
		out.Write(inner.Bytes())
	}
	if n.DataAtom == atom.A {
		for _, a := range n.Attr {
			if a.Key == "href" && a.Val != inner.String() {
				out.WriteString(" (" + a.Val + ")")
			}
		}
	}
	if blockTags[n.DataAtom] {
		out.WriteString("\n")
	}
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string { return blankRuns.ReplaceAllString(s, "\n\n") }

// QuoteHTML renders an original plain-text message as a quoted HTML block.
func QuoteHTML(attribution, text string) string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	for i, l := range lines {
		lines[i] = html.EscapeString(l)
	}
	return `<div><br>` + html.EscapeString(attribution) + `</div><blockquote style="margin:0 0 0 .8ex;border-left:1px solid #ccc;padding-left:1ex">` +
		strings.Join(lines, "<br>") + `</blockquote>`
}
