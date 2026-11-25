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
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

type domain struct{}

var set = enum.NewSet("detail")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

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

// Field is one figure: its key and its value, as what the value is — a
// number, a text, a moment, a server. It becomes text only where a set is
// written out. The zero Field is no figure at all, and so is a text that
// is empty or a list with nothing in it: adding one to a Set adds nothing.
type Field struct {
	key     Key
	num     float64
	text    string
	lines   []string
	at      time.Time
	variant variantkey.Key
	hashes  []uint64
	server  node.Node
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
	// A figure tells a moment to the second.
	return Field{key: k, at: v.UTC().Truncate(time.Second)}
}

func variantField(k Key, v variantkey.Key) Field {
	if v.IsZero() {
		return Field{}
	}
	return Field{key: k, variant: v}
}

func serverField(k Key, v node.Node) Field {
	if v.IsZero() {
		return Field{}
	}
	return Field{key: k, server: v}
}

// SignatureMax is how many coded frames a clip is known by at the most.
const SignatureMax = 256

// hashesField is the coded frames a clip is known by: sorted, each once.
// Outside the program they are written in hexadecimal with commas between.
func hashesField(k Key, v []uint64) Field {
	if len(v) == 0 {
		return Field{}
	}
	v = slices.Clone(v)
	slices.Sort(v)
	return Field{key: k, hashes: slices.Compact(v)}
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
	return out, nil
}

// MarshalJSON writes the value the way it is told outside the program.
func (f Field) MarshalJSON() ([]byte, error) {
	switch f.key.kind() {
	case numberValue, countValue:
		return json.Marshal(f.num)
	case linesValue:
		return json.Marshal(f.lines)
	case timeValue:
		return json.Marshal(f.at.Format(time.RFC3339))
	case variantValue:
		return json.Marshal(f.variant.String())
	case serverValue:
		return json.Marshal(f.server.String())
	case hashesValue:
		parts := make([]string, len(f.hashes))
		for i, h := range f.hashes {
			parts[i] = strconv.FormatUint(h, 16)
		}
		return json.Marshal(strings.Join(parts, ","))
	}
	return json.Marshal(f.text)
}

// unmarshal reads the value of the figure k, into what it is. This is the
// one place where the text of a figure is taken apart.
func (f *Field) unmarshal(k Key, raw []byte) error {
	f.key = k
	switch k.kind() {
	case numberValue, countValue:
		return json.Unmarshal(raw, &f.num)
	case linesValue:
		return json.Unmarshal(raw, &f.lines)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	var err error
	switch k.kind() {
	case timeValue:
		f.at, err = time.Parse(time.RFC3339, text)
	case variantValue:
		f.variant, err = variantkey.Parse(text)
	case serverValue:
		f.server, err = node.Parse(text)
	case hashesValue:
		var hs []uint64
		if hs, err = parseHashes(text); err == nil {
			*f = hashesField(k, hs)
		}
	default:
		f.text = text
	}
	return err
}

// A key says, by its type, what the value of its figure is, so that a
// number is not asked for where there is a server: the compiler refuses.

// NumberKey names a figure that is a number.
type NumberKey struct{ Key }

// TextKey names a figure that is text.
type TextKey struct{ Key }

// LinesKey names a figure that is a list of lines.
type LinesKey struct{ Key }

// TimeKey names a figure that is a moment.
type TimeKey struct{ Key }

// VariantKey names a figure that is a playlist.
type VariantKey struct{ Key }

// HashesKey names a figure that is the coded frames of a clip.
type HashesKey struct{ Key }

// ServerKey names a figure that is a server.
type ServerKey struct{ Key }

// Named is any key, whatever its figure is.
type Named interface{ named() Key }

func (k Key) named() Key { return k }

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
func (s Set) Has(k Named) bool {
	_, ok := s.m[k.named()]
	return ok
}

// Number is a figure that is a number; false when the set has none such.
func (s Set) Number(k NumberKey) (float64, bool) {
	f, ok := s.m[k.Key]
	return f.num, ok
}

// Text is a figure that is text; empty when the set has none such.
func (s Set) Text(k TextKey) string { return s.m[k.Key].text }

// Lines is a figure that is a list of lines.
func (s Set) Lines(k LinesKey) []string { return s.m[k.Key].lines }

// Time is a figure that is a moment; the zero time when the set has none
// such.
func (s Set) Time(k TimeKey) time.Time { return s.m[k.Key].at }

// Variant is a figure that is a playlist; none when the set has none such.
func (s Set) Variant(k VariantKey) variantkey.Key { return s.m[k.Key].variant }

// Hashes is a figure that is the coded frames of a clip, sorted and
// without repeats; nil when the set has none such.
func (s Set) Hashes(k HashesKey) []uint64 { return s.m[k.Key].hashes }

// Server is a figure that names a server; no server when the set has
// none such.
func (s Set) Server(k ServerKey) node.Node { return s.m[k.Key].server }

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
