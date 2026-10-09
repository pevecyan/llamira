// Package presets implements a lossless model of the llama.cpp router preset INI file.
// Comments, blank lines, ordering and unknown entries survive a parse/edit/render cycle.
package presets

import (
	"strings"
)

// GlobalSection is the name of the section holding defaults for every model.
const GlobalSection = "*"

type lineKind int

const (
	lineBlank lineKind = iota
	lineComment
	lineKV
	lineOther
)

type line struct {
	kind  lineKind
	raw   string
	key   string
	value string
}

// Section is a "[name]" block, or the preamble before the first header (Name == "", no header).
type Section struct {
	Name   string
	header string
	lines  []line
}

// Document is a parsed preset file.
type Document struct {
	Preamble *Section
	Sections []*Section
	// trailingNewline records whether the source ended with a newline.
	trailingNewline bool
}

// KV is one key/value entry.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Parse parses preset INI text. It never fails: lines it does not understand are kept verbatim.
func Parse(text string) *Document {
	d := &Document{Preamble: &Section{}, trailingNewline: text == "" || strings.HasSuffix(text, "\n")}
	cur := d.Preamble
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return d
	}
	for _, raw := range strings.Split(text, "\n") {
		t := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		switch {
		case t == "":
			cur.lines = append(cur.lines, line{kind: lineBlank, raw: raw})
		case t[0] == ';' || t[0] == '#':
			cur.lines = append(cur.lines, line{kind: lineComment, raw: raw})
		case t[0] == '[' && strings.HasSuffix(t, "]"):
			cur = &Section{Name: strings.TrimSpace(t[1 : len(t)-1]), header: raw}
			d.Sections = append(d.Sections, cur)
		default:
			if k, v, ok := strings.Cut(t, "="); ok {
				cur.lines = append(cur.lines, line{kind: lineKV, raw: raw, key: strings.TrimSpace(k), value: strings.TrimSpace(v)})
			} else {
				cur.lines = append(cur.lines, line{kind: lineOther, raw: raw})
			}
		}
	}
	return d
}

// String renders the document. An unmodified document renders byte-identical to its source.
func (d *Document) String() string {
	var b strings.Builder
	write := func(s *Section) {
		if s.header != "" {
			b.WriteString(s.header)
			b.WriteByte('\n')
		}
		for _, l := range s.lines {
			b.WriteString(l.raw)
			b.WriteByte('\n')
		}
	}
	write(d.Preamble)
	for _, s := range d.Sections {
		write(s)
	}
	out := b.String()
	if !d.trailingNewline {
		out = strings.TrimSuffix(out, "\n")
	}
	return out
}

// Section returns the first section with the given name, or nil.
func (d *Document) Section(name string) *Section {
	for _, s := range d.Sections {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// Names returns section names in file order.
func (d *Document) Names() []string {
	var names []string
	for _, s := range d.Sections {
		names = append(names, s.Name)
	}
	return names
}

// Entries returns the key/value entries of a section in order.
func (s *Section) Entries() []KV {
	var kvs []KV
	for _, l := range s.lines {
		if l.kind == lineKV {
			kvs = append(kvs, KV{Key: l.key, Value: l.value})
		}
	}
	return kvs
}

// Comments returns the comment lines of a section (without the leading ; or #).
func (s *Section) Comments() []string {
	var out []string
	for _, l := range s.lines {
		if l.kind == lineComment {
			t := strings.TrimSpace(l.raw)
			out = append(out, strings.TrimSpace(t[1:]))
		}
	}
	return out
}

// Get returns the value of key (first occurrence).
func (s *Section) Get(key string) (string, bool) {
	for _, l := range s.lines {
		if l.kind == lineKV && l.key == key {
			return l.value, true
		}
	}
	return "", false
}

// SetEntries replaces the section's entries with kvs while keeping comments and the position
// of entries that still exist. canon maps a key to its canonical form so that e.g. "c" and
// "ctx-size" are treated as the same entry; pass nil for exact matching.
func (s *Section) SetEntries(kvs []KV, canon func(string) string) {
	if canon == nil {
		canon = func(k string) string { return k }
	}
	want := map[string]KV{}
	var order []string
	for _, kv := range kvs {
		c := canon(kv.Key)
		if _, dup := want[c]; !dup {
			order = append(order, c)
		}
		want[c] = kv
	}
	done := map[string]bool{}
	var out []line
	for _, l := range s.lines {
		if l.kind != lineKV {
			out = append(out, l)
			continue
		}
		c := canon(l.key)
		kv, keep := want[c]
		if !keep || done[c] {
			continue // removed, or a duplicate of an entry already written
		}
		done[c] = true
		if l.value != kv.Value || l.key != kv.Key {
			l = newKV(kv)
		}
		out = append(out, l)
	}
	// Append new entries after the last non-blank line of the section.
	insert := len(out)
	for insert > 0 && out[insert-1].kind == lineBlank {
		insert--
	}
	var add []line
	for _, c := range order {
		if !done[c] {
			add = append(add, newKV(want[c]))
		}
	}
	out = append(out[:insert], append(add, out[insert:]...)...)
	s.lines = out
}

func newKV(kv KV) line {
	return line{kind: lineKV, raw: kv.Key + " = " + kv.Value, key: kv.Key, value: kv.Value}
}

// Upsert creates or updates a section. New sections are appended at the end, except the global
// section which is placed before all model sections.
func (d *Document) Upsert(name string, kvs []KV, canon func(string) string) *Section {
	if s := d.Section(name); s != nil {
		s.SetEntries(kvs, canon)
		return s
	}
	s := &Section{Name: name, header: "[" + name + "]"}
	s.SetEntries(kvs, canon)
	if name == GlobalSection {
		d.ensureBlankBefore(0)
		d.Sections = append([]*Section{s}, d.Sections...)
		s.lines = append(s.lines, line{kind: lineBlank})
		return s
	}
	d.ensureBlankBefore(len(d.Sections))
	d.Sections = append(d.Sections, s)
	return s
}

// ensureBlankBefore makes sure the block preceding section index i ends with a blank line,
// so that inserted sections are visually separated.
func (d *Document) ensureBlankBefore(i int) {
	prev := d.Preamble
	if i > 0 {
		prev = d.Sections[i-1]
	}
	if prev == d.Preamble && len(prev.lines) == 0 {
		return
	}
	if n := len(prev.lines); n == 0 || prev.lines[n-1].kind != lineBlank {
		prev.lines = append(prev.lines, line{kind: lineBlank})
	}
}

// Delete removes a section. It reports whether the section existed.
func (d *Document) Delete(name string) bool {
	for i, s := range d.Sections {
		if s.Name == name {
			d.Sections = append(d.Sections[:i], d.Sections[i+1:]...)
			return true
		}
	}
	return false
}

// Rename changes a section's name, keeping its content and position.
func (d *Document) Rename(oldName, newName string) bool {
	s := d.Section(oldName)
	if s == nil {
		return false
	}
	s.Name = newName
	s.header = "[" + newName + "]"
	return true
}

// SetPreamble sets a top-level (pre-section) key such as "version".
func (d *Document) SetPreamble(key, value string) {
	kvs := d.Preamble.Entries()
	found := false
	for i := range kvs {
		if kvs[i].Key == key {
			kvs[i].Value, found = value, true
		}
	}
	if !found {
		kvs = append(kvs, KV{Key: key, Value: value})
	}
	d.Preamble.SetEntries(kvs, nil)
}
