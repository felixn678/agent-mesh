package cli

import (
	"encoding/json"
	"errors"
)

// jnode is a JSON value with its byte span in the source, so settings.json
// can be edited by splicing bytes instead of re-encoding (re-encoding would
// reorder keys and reformat the user's file).
type jnode struct {
	kind       byte // '{', '[', '"' or 'v' (number, bool, null)
	start, end int  // [start, end)
	members    []jmember
	elems      []*jnode
}

type jmember struct {
	key      string
	keyStart int
	val      *jnode
}

// member returns the value of key in an object node, or nil.
func (n *jnode) member(key string) (*jmember, int) {
	for i := range n.members {
		if n.members[i].key == key {
			return &n.members[i], i
		}
	}
	return nil, -1
}

// parseSpans parses valid JSON (checked first with json.Valid, so the
// scanner below can assume well-formed input).
func parseSpans(b []byte) (*jnode, error) {
	if !json.Valid(b) {
		return nil, errors.New("invalid JSON")
	}
	p := spanParser{b: b}
	return p.value()
}

type spanParser struct {
	b []byte
	i int
}

func (p *spanParser) ws() {
	for p.i < len(p.b) && (p.b[p.i] == ' ' || p.b[p.i] == '\t' || p.b[p.i] == '\n' || p.b[p.i] == '\r') {
		p.i++
	}
}

func (p *spanParser) value() (*jnode, error) {
	p.ws()
	n := &jnode{start: p.i}
	switch p.b[p.i] {
	case '{':
		n.kind = '{'
		p.i++
		for p.ws(); p.b[p.i] != '}'; p.ws() {
			keyStart := p.i
			p.str()
			var key string
			if err := json.Unmarshal(p.b[keyStart:p.i], &key); err != nil {
				return nil, err
			}
			p.ws()
			p.i++ // ':'
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			n.members = append(n.members, jmember{key: key, keyStart: keyStart, val: v})
			if p.ws(); p.b[p.i] == ',' {
				p.i++
			}
		}
		p.i++
	case '[':
		n.kind = '['
		p.i++
		for p.ws(); p.b[p.i] != ']'; p.ws() {
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			n.elems = append(n.elems, v)
			if p.ws(); p.b[p.i] == ',' {
				p.i++
			}
		}
		p.i++
	case '"':
		n.kind = '"'
		p.str()
	default:
		n.kind = 'v'
		for p.i < len(p.b) && !isDelim(p.b[p.i]) {
			p.i++
		}
	}
	n.end = p.i
	return n, nil
}

// str advances over a string literal starting at the opening quote.
func (p *spanParser) str() {
	for p.i++; p.b[p.i] != '"'; p.i++ {
		if p.b[p.i] == '\\' {
			p.i++
		}
	}
	p.i++
}

func isDelim(c byte) bool {
	return c == ',' || c == ']' || c == '}' || c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// sepBefore returns the whitespace that precedes the element starting at
// start, after the previous separator ('{', '[' or ','), so an appended
// sibling can reuse the file's indentation.
func sepBefore(b []byte, start int) string {
	i := start
	for i > 0 && (b[i-1] == ' ' || b[i-1] == '\t' || b[i-1] == '\n' || b[i-1] == '\r') {
		i--
	}
	return string(b[i:start])
}

// splice replaces b[from:to] with s.
func splice(b []byte, from, to int, s string) []byte {
	out := make([]byte, 0, len(b)-(to-from)+len(s))
	out = append(out, b[:from]...)
	out = append(out, s...)
	return append(out, b[to:]...)
}
