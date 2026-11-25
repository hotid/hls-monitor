// Package id is the identifiers the monitor knows its things by: streams,
// agents, the playlists of a stream, incidents and the rest. Each kind of
// thing has a type of its own, so that the identifier of an agent cannot
// be given where that of a stream is wanted: they are the same numbers,
// and nothing else would tell.
//
// An identifier is a number outside the program: in JSON, in the
// database, in an address. The zero identifier is that of nothing; where
// one is asked for and none is meant — every stream, the stream as a whole
// — it is the zero one that says so.
package id

import (
	"database/sql/driver"
	"fmt"
	"strconv"
)

// ID is the identifier of a thing of the kind K.
type ID[K any] struct{ n int64 }

type (
	streamKind         struct{}
	agentKind          struct{}
	variantKind        struct{}
	incidentKind       struct{}
	serverIncidentKind struct{}
	userKind           struct{}
	tokenKind          struct{}
	slateKind          struct{}
	frameKind          struct{}
	snapshotKind       struct{}
	recordKind         struct{}
)

type (
	// Stream identifies a monitored stream.
	Stream = ID[streamKind]
	// Agent identifies a vantage point: the server itself or a remote agent.
	Agent = ID[agentKind]
	// Variant identifies one playlist of a stream.
	Variant = ID[variantKind]
	// Incident identifies an incident of a stream.
	Incident = ID[incidentKind]
	// ServerIncident identifies an incident of a server.
	ServerIncident = ID[serverIncidentKind]
	// User identifies an account.
	User = ID[userKind]
	// Token identifies an API token.
	Token = ID[tokenKind]
	// Slate identifies a slate, or a clip a channel plays itself.
	Slate = ID[slateKind]
	// Frame identifies one picture of a slate.
	Frame = ID[frameKind]
	// Snapshot identifies a stored playlist.
	Snapshot = ID[snapshotKind]
	// Record identifies a row of the segment journal.
	Record = ID[recordKind]
)

func StreamOf(n int64) Stream                 { return Stream{n} }
func AgentOf(n int64) Agent                   { return Agent{n} }
func VariantOf(n int64) Variant               { return Variant{n} }
func IncidentOf(n int64) Incident             { return Incident{n} }
func ServerIncidentOf(n int64) ServerIncident { return ServerIncident{n} }
func UserOf(n int64) User                     { return User{n} }
func TokenOf(n int64) Token                   { return Token{n} }
func SlateOf(n int64) Slate                   { return Slate{n} }
func FrameOf(n int64) Frame                   { return Frame{n} }
func SnapshotOf(n int64) Snapshot             { return Snapshot{n} }
func RecordOf(n int64) Record                 { return Record{n} }

// Int64 is the identifier as the number it is outside the program.
func (i ID[K]) Int64() int64 { return i.n }

// IsZero tells that it identifies nothing.
func (i ID[K]) IsZero() bool { return i.n == 0 }

// Compare orders identifiers as their numbers are ordered.
func (i ID[K]) Compare(o ID[K]) int {
	switch {
	case i.n < o.n:
		return -1
	case i.n > o.n:
		return 1
	}
	return 0
}

// String is the identifier in decimal.
func (i ID[K]) String() string { return strconv.FormatInt(i.n, 10) }

// Format writes the identifier as its number whatever it is asked to be
// written as: in a message, in an address, in a line of a log.
func (i ID[K]) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		fmt.Fprintf(f, "%q", i.String())
		return
	}
	fmt.Fprint(f, i.String())
}

// Parse reads an identifier written in decimal, as in an address.
func (i *ID[K]) Parse(text string) error {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("identifier %q is not a number", text)
	}
	i.n = n
	return nil
}

// MarshalJSON writes the identifier as a number.
func (i ID[K]) MarshalJSON() ([]byte, error) { return strconv.AppendInt(nil, i.n, 10), nil }

// UnmarshalJSON reads a number; null is the identifier of nothing. The
// number may come in quotes: that is how the key of an object is written.
func (i *ID[K]) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		i.n = 0
		return nil
	}
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		b = b[1 : len(b)-1]
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("identifier %s is not a whole number", b)
	}
	i.n = n
	return nil
}

// MarshalText writes the identifier where text is wanted: as the key of a
// JSON object.
func (i ID[K]) MarshalText() ([]byte, error) { return strconv.AppendInt(nil, i.n, 10), nil }

func (i *ID[K]) UnmarshalText(b []byte) error { return i.Parse(string(b)) }

// Value stores the identifier in a database.
func (i ID[K]) Value() (driver.Value, error) { return i.n, nil }

// Scan reads the identifier from a database; NULL is that of nothing.
func (i *ID[K]) Scan(src any) error {
	switch x := src.(type) {
	case nil:
		i.n = 0
	case int64:
		i.n = x
	default:
		return fmt.Errorf("identifier cannot be read from %T", src)
	}
	return nil
}
