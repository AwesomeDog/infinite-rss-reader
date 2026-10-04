package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
)

const forumzillaNS = "urn:forumzilla:"

// node is a minimal XML tree, hand-rolled because encoding/xml has no "keep any
// attribute" tag and a declared struct would reorder and drop unknown attributes.
type node struct {
	name  xml.Name
	attrs []xml.Attr
	text  string
	kids  []*node
}

func attr(n *node, local string) string {
	for _, a := range n.attrs {
		if a.Name.Space == "" && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// parse builds the tree, dropping fz:options on the way in: it embeds the last
// update timestamp, so it differs between every export and is the main source of
// diff noise. fz:quickMode is stable and is kept.
func parse(r io.Reader) (*node, map[string]string, error) {
	ns, dec, stack := map[string]string{}, xml.NewDecoder(r), []*node{}
	var root *node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					ns[strings.TrimPrefix(a.Name.Local, "xmlns")] = a.Value
				} else if !(t.Name.Local == "outline" && a.Name.Space == forumzillaNS && a.Name.Local == "options") {
					n.attrs = append(n.attrs, a)
				}
			}
			if len(stack) == 0 {
				root = n
			} else {
				stack[len(stack)-1].kids = append(stack[len(stack)-1].kids, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil {
		return nil, nil, fmt.Errorf("no root element")
	}
	return root, ns, nil
}

// dedupe drops repeated feeds, keeping the first occurrence of each xmlUrl, and
// returns how many it removed here. A wrapper left empty by deduplication goes too.
func dedupe(n *node, seen map[string]bool) int {
	kept, removed := n.kids[:0], 0
	for _, kid := range n.kids {
		url := attr(kid, "xmlUrl")
		isFeed := url != "" && kid.name.Local == "outline"
		if isFeed && seen[url] {
			removed++
			continue
		}
		if isFeed {
			seen[url] = true
		}
		r := dedupe(kid, seen)
		removed += r
		if r > 0 && !isFeed && kid.name.Local == "outline" && len(kid.kids) == 0 {
			continue
		}
		kept = append(kept, kid)
	}
	n.kids = kept
	return removed
}

func qname(n xml.Name, ns map[string]string) string {
	for p, uri := range ns {
		if uri == n.Space && p != "" {
			return p + ":" + n.Local
		}
	}
	return n.Local
}

// escapes mirrors ElementTree: text uses the first three pairs, attributes all.
var escapes = []string{"&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "\r", "&#13;", "\n", "&#10;", "\t", "&#09;"}

func escape(s string, inAttr bool) string {
	for i := 0; i < len(escapes); i += 2 {
		if !inAttr && i >= 6 {
			break
		}
		s = strings.ReplaceAll(s, escapes[i], escapes[i+1])
	}
	return s
}

// writeNode sorts outlines by lowercase title, non-outlines first, equal titles in
// document order.
func writeNode(w *strings.Builder, n *node, ns map[string]string, pad, decls string) {
	name := qname(n.name, ns)
	w.WriteString("<" + name + decls)
	for _, a := range n.attrs {
		w.WriteString(" " + qname(a.Name, ns) + `="` + escape(a.Value, true) + `"`)
	}
	if len(n.kids) == 0 {
		if n.text == "" {
			w.WriteString(" />")
			return
		}
		w.WriteString(">" + escape(n.text, false) + "</" + name + ">")
		return
	}
	inner := pad + "  "
	w.WriteString(">")
	sort.SliceStable(n.kids, func(i, j int) bool {
		a, b := n.kids[i], n.kids[j]
		oa, ob := a.name.Local == "outline", b.name.Local == "outline"
		if oa != ob {
			return ob
		} else if !oa {
			return false
		}
		return strings.ToLower(attr(a, "title")) < strings.ToLower(attr(b, "title"))
	})
	for _, kid := range n.kids {
		w.WriteString(inner)
		writeNode(w, kid, ns, inner, "")
	}
	w.WriteString(pad + "</" + name + ">")
}

// normalizeOPML returns a canonical form of r: sorted outlines, no duplicate feeds,
// no volatile fz:options. Running it twice gives identical bytes.
func normalizeOPML(r io.Reader) ([]byte, error) {
	root, ns, err := parse(r)
	if err != nil {
		return nil, err
	}
	dedupe(root, map[string]bool{})
	decls := ""
	for _, p := range slices.Sorted(maps.Keys(ns)) {
		decls += " xmlns" + strings.TrimSuffix(":"+p, ":") + `="` + escape(ns[p], true) + `"`
	}
	var buf strings.Builder
	writeNode(&buf, root, ns, "\n", decls)
	buf.WriteByte('\n')
	return []byte(buf.String()), nil
}
