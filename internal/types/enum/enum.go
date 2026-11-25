// Package enum is what the closed sets of values of the monitor are made
// of: the kinds of problems, the severities, the levels of a check and the
// like. A value of such a set is not a string and cannot be made of one by
// conversion: it comes from the function that names it or from Parse, which
// refuses what the set does not have. The name of a value appears where the
// value leaves the program — in JSON, in the database, on a page — and
// nowhere inside it.
//
// The zero value belongs to no set. It says that a field was never filled,
// which is a mistake of the code that made the value, so String panics on
// it and MarshalText refuses it. A field that may be absent says so with
// omitzero, and the code that reads it asks IsZero.
package enum

import (
	"database/sql/driver"
	"fmt"
)

// Domain tells one set from another. It is implemented by an empty struct
// of the package that owns the set, and that is what keeps the values of
// two sets from being compared with or assigned to each other.
type Domain interface {
	Set() *Set
}

// Set is the names of the values of one domain, in the order they were
// defined.
type Set struct {
	what  string
	names []string // names[0] is not a value
	index map[string]uint8
}

// NewSet starts a set; what names it in messages.
func NewSet(what string) *Set {
	return &Set{what: what, names: []string{""}, index: map[string]uint8{}}
}

// Of is a value of the domain D.
type Of[D Domain] struct{ i uint8 }

func set[D Domain]() *Set {
	var d D
	return d.Set()
}

// Def adds a value to the set of D. It is for the variables of the package
// that owns the set, and nothing else: a set does not grow while the
// program runs.
func Def[D Domain](name string) Of[D] {
	s := set[D]()
	if _, dup := s.index[name]; dup {
		panic(fmt.Sprintf("enum: %s %q defined twice", s.what, name))
	}
	if len(s.names) > 255 {
		panic(fmt.Sprintf("enum: %s has too many values", s.what))
	}
	i := uint8(len(s.names))
	s.names = append(s.names, name)
	s.index[name] = i
	return Of[D]{i}
}

// Parse finds the value a name stands for.
func Parse[D Domain](name string) (Of[D], error) {
	s := set[D]()
	i, ok := s.index[name]
	if !ok {
		return Of[D]{}, fmt.Errorf("unknown %s %q", s.what, name)
	}
	return Of[D]{i}, nil
}

// All lists the values of D in the order they were defined.
func All[D Domain]() []Of[D] {
	s := set[D]()
	out := make([]Of[D], 0, len(s.names)-1)
	for i := 1; i < len(s.names); i++ {
		out = append(out, Of[D]{uint8(i)})
	}
	return out
}

// IsZero tells that the value was never set.
func (v Of[D]) IsZero() bool { return v.i == 0 }

// Ord is the place of the value in its set, from one, in the order of
// definition. A set whose values have an order defines them in it.
func (v Of[D]) Ord() int {
	if v.i == 0 {
		panic(fmt.Sprintf("enum: %s that was never set is used", set[D]().what))
	}
	return int(v.i)
}

// String is the name of the value, for what leaves the program.
func (v Of[D]) String() string {
	s := set[D]()
	if v.i == 0 {
		panic(fmt.Sprintf("enum: %s that was never set is used", s.what))
	}
	return s.names[v.i]
}

// MarshalText makes the value its name in JSON, as a value and as the key
// of an object.
func (v Of[D]) MarshalText() ([]byte, error) {
	s := set[D]()
	if v.i == 0 {
		return nil, fmt.Errorf("%s that was never set", s.what)
	}
	return []byte(s.names[v.i]), nil
}

// UnmarshalText reads a name.
func (v *Of[D]) UnmarshalText(b []byte) error {
	p, err := Parse[D](string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Value stores the value in a database as its name.
func (v Of[D]) Value() (driver.Value, error) {
	b, err := v.MarshalText()
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan reads the value from a database.
func (v *Of[D]) Scan(src any) error {
	switch x := src.(type) {
	case string:
		return v.UnmarshalText([]byte(x))
	case []byte:
		return v.UnmarshalText(x)
	}
	return fmt.Errorf("%s cannot be read from %T", set[D]().what, src)
}
