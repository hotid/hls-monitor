// Package detail is the figures a conclusion was drawn from: what was
// requested, what came back and what it was compared with. A problem
// carries them so that whoever reads of it can check the reasoning.
//
// A figure is made by the function that names it, which takes the kind of
// value the figure has: seconds are a number, a server is text, a moment
// is a time. Outside the program a set of figures is a JSON object with
// the names of the figures for keys.
package detail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/types/enum"
	"github.com/hotid/hls-monitor/internal/types/node"
)

type domain struct{}

var set = enum.NewSet("detail")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Key names one figure.
type Key struct{ value }

// valueKind is what the value of a figure is.
type valueKind uint8

const (
	_ valueKind = iota
	numberValue
	countValue
	textValue
	linesValue
	timeValue
	variantValue
	hashesValue
	serverValue
)

// kinds tells what the value of each key is, by the key's place in the set.
var kinds = []valueKind{0}

func defKey(name string, kind valueKind) Key {
	_ = set // the set is there before the first key is
	k := Key{enum.Def[domain](name)}
	kinds = append(kinds, kind)
	return k
}

// ParseKey finds the figure a name stands for.
func ParseKey(name string) (Key, error) {
	v, err := enum.Parse[domain](name)
	return Key{v}, err
}

func (k Key) kind() valueKind { return kinds[k.Ord()] }

type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

// Field is one figure: its key and its value. The zero Field is no figure
// at all, and so is a text that is empty or a list with nothing in it:
// adding one to a Set adds nothing.
type Field struct {
	key   Key
	num   float64
	text  string
	lines []string
}

// Key is which figure it is.
func (f Field) Key() Key { return f.key }

// IsZero tells that there is no figure.
func (f Field) IsZero() bool { return f.key.IsZero() }

func numberField(k Key, v float64) Field {
	// Figures are for reading: a millisecond is precise enough.
	s := 1.0
	if v < 0 {
		s = -1
	}
	return Field{key: k, num: float64(int64(v*1000+0.5*s)) / 1000}
}

func countField(k Key, v float64) Field { return Field{key: k, num: v} }

func textField(k Key, v string) Field {
	if v == "" {
		return Field{}
	}
	return Field{key: k, text: v}
}

func linesField(k Key, v []string) Field {
	if len(v) == 0 {
		return Field{}
	}
	return Field{key: k, lines: v}
}

func timeField(k Key, v time.Time) Field {
	if v.IsZero() {
		return Field{}
	}
	return Field{key: k, text: v.UTC().Format(time.RFC3339)}
}

// SignatureMax is how many coded frames a clip is known by at the most.
const SignatureMax = 256

// hashesField is the coded frames a clip is known by. Outside the program
// they are written in hexadecimal with commas between.
func hashesField(k Key, v []uint64) Field {
	if len(v) == 0 {
		return Field{}
	}
	parts := make([]string, len(v))
	for i, h := range v {
		parts[i] = strconv.FormatUint(h, 16)
	}
	return Field{key: k, text: strings.Join(parts, ",")}
}

// Hashes is a figure that is the coded frames of a clip, sorted and
// without repeats; nil when the set has none such.
func (s Set) Hashes(k Key) []uint64 {
	f, ok := s.m[k]
	if !ok || k.kind() != hashesValue {
		return nil
	}
	out, _ := parseHashes(f.text)
	return out
}

func parseHashes(text string) ([]uint64, error) {
	var out []uint64
	for _, p := range strings.Split(text, ",") {
		h, err := strconv.ParseUint(p, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("frame %q of a clip: %w", p, err)
		}
		if len(out) < SignatureMax {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func (f Field) MarshalJSON() ([]byte, error) {
	switch f.key.kind() {
	case numberValue, countValue:
		return json.Marshal(f.num)
	case linesValue:
		return json.Marshal(f.lines)
	}
	return json.Marshal(f.text)
}

func (f *Field) unmarshal(k Key, raw []byte) error {
	f.key = k
	switch k.kind() {
	case numberValue, countValue:
		return json.Unmarshal(raw, &f.num)
	case linesValue:
		return json.Unmarshal(raw, &f.lines)
	}
	if err := json.Unmarshal(raw, &f.text); err != nil {
		return err
	}
	if k.kind() == hashesValue {
		_, err := parseHashes(f.text)
		return err
	}
	return nil
}

// Set is the figures of one problem, one of a kind.
type Set struct {
	m map[Key]Field
}

// Of makes a set of the figures given.
func Of(fs ...Field) Set {
	var s Set
	s.Add(fs...)
	return s
}

// Add puts figures into the set. A figure the set already has stays as it
// is: the first value recorded is the one that tells.
func (s *Set) Add(fs ...Field) { s.put(fs, false) }

// Replace puts figures into the set, in place of those it has.
func (s *Set) Replace(fs ...Field) { s.put(fs, true) }

func (s *Set) put(fs []Field, replace bool) {
	for _, f := range fs {
		if f.IsZero() {
			continue
		}
		if _, have := s.m[f.key]; have && !replace {
			continue
		}
		if s.m == nil {
			s.m = map[Key]Field{}
		}
		s.m[f.key] = f
	}
}

// Fill adds the figures of another set that this one does not have.
func (s *Set) Fill(from Set) {
	for _, f := range from.m {
		s.put([]Field{f}, false)
	}
}

// Len is how many figures there are.
func (s Set) Len() int { return len(s.m) }

// IsZero tells that there are none; it is what omitzero asks.
func (s Set) IsZero() bool { return len(s.m) == 0 }

// Has tells whether the set has the figure.
func (s Set) Has(k Key) bool {
	_, ok := s.m[k]
	return ok
}

// Number is a figure that is a number; false when the set has none such.
func (s Set) Number(k Key) (float64, bool) {
	f, ok := s.m[k]
	if !ok || (k.kind() != numberValue && k.kind() != countValue) {
		return 0, false
	}
	return f.num, true
}

// Text is a figure that is text, a moment or a playlist's key; empty when
// the set has none such.
func (s Set) Text(k Key) string {
	if kind := k.kind(); kind == numberValue || kind == countValue || kind == linesValue || kind == hashesValue || kind == serverValue {
		return ""
	}
	return s.m[k].text
}

// Server is a figure that names a server; no server when the set has
// none such.
func (s Set) Server(k Key) node.Node {
	f, ok := s.m[k]
	if !ok || k.kind() != serverValue {
		return node.Node{}
	}
	n, _ := node.Parse(f.text)
	return n
}

// Lines is a figure that is a list of lines.
func (s Set) Lines(k Key) []string { return s.m[k].lines }

// MarshalJSON writes the set as an object, the names of the figures in order.
func (s Set) MarshalJSON() ([]byte, error) {
	names := make([]string, 0, len(s.m))
	by := make(map[string]Field, len(s.m))
	for k, f := range s.m {
		names = append(names, k.String())
		by[k.String()] = f
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(n)
		val, err := by[n].MarshalJSON()
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads a set. A figure this program does not know, or one
// whose value is not what the figure has, is refused: figures come from
// another copy of this program, and one that tells what this one cannot
// read is of another version.
func (s *Set) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.m = nil
	for name, v := range raw {
		k, err := ParseKey(name)
		if err != nil {
			return err
		}
		var f Field
		if err := f.unmarshal(k, v); err != nil {
			return fmt.Errorf("detail %s: %w", name, err)
		}
		if s.m == nil {
			s.m = map[Key]Field{}
		}
		s.m[k] = f
	}
	return nil
}

// Stored reads a set from where this program kept it. What an older
// version wrote and this one has no figure for is left out: the record of
// an incident outlives the version that made it.
func Stored(text string) Set {
	var s Set
	var raw map[string]json.RawMessage
	if text == "" || json.Unmarshal([]byte(text), &raw) != nil {
		return s
	}
	for name, v := range raw {
		k, err := ParseKey(name)
		if err != nil {
			continue
		}
		var f Field
		if f.unmarshal(k, v) != nil {
			continue
		}
		if s.m == nil {
			s.m = map[Key]Field{}
		}
		s.m[k] = f
	}
	return s
}
