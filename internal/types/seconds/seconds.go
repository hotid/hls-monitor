// Package seconds is a length of time that is written, where it leaves
// the program, as a number of seconds: in a request, in the answer to
// one, in a row of the database. Inside it is a time.Duration and is
// used as one.
package seconds

import (
	"database/sql/driver"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Span is a length of time; zero where none was set.
type Span time.Duration

// Of is the span of a number of seconds.
func Of(sec float64) Span { return Span(math.Round(sec * float64(time.Second))) }

// Duration is the span as the library has it.
func (s Span) Duration() time.Duration { return time.Duration(s) }

// Seconds is the number the span is written as.
func (s Span) Seconds() float64 { return time.Duration(s).Seconds() }

func (s Span) String() string { return time.Duration(s).String() }

// MarshalJSON writes the number of seconds.
func (s Span) MarshalJSON() ([]byte, error) {
	return strconv.AppendFloat(nil, s.Seconds(), 'f', -1, 64), nil
}

// UnmarshalJSON reads a number of seconds.
func (s *Span) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("%s is not a number of seconds", b)
	}
	*s = Of(v)
	return nil
}

// Value stores the number of seconds.
func (s Span) Value() (driver.Value, error) { return s.Seconds(), nil }

// Scan reads a stored number of seconds.
func (s *Span) Scan(src any) error {
	switch x := src.(type) {
	case nil:
		*s = 0
	case float64:
		*s = Of(x)
	case int64:
		*s = Span(time.Duration(x) * time.Second)
	default:
		return fmt.Errorf("%T is not a number of seconds", src)
	}
	return nil
}
