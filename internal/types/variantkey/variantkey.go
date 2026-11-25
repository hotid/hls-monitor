// Package variantkey is what a playlist of a stream is known by.
package variantkey

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// Key names one playlist of a stream. The playlists a master playlist
// lists are keyed by their URI in it; the master playlist itself and a
// stream that is a media playlist have keys of their own.
//
// The zero Key names no playlist. A figure about a stream as a whole has
// it, and says so with omitzero.
type Key struct{ s string }

// Master is the key of the multivariant playlist itself.
func Master() Key { return Key{"master"} }

// Media is the key of a stream whose URL is a media playlist.
func Media() Key { return Key{"media"} }

// OfURI is the key of a playlist listed in a master playlist under uri.
func OfURI(uri string) Key {
	if uri == "" {
		panic("variantkey: a playlist with no URI has no key")
	}
	return Key{uri}
}

// Parse reads a key from where it was written.
func Parse(s string) (Key, error) {
	if s == "" {
		return Key{}, errors.New("empty variant key")
	}
	return Key{s}, nil
}

// IsZero tells that the key names no playlist.
func (k Key) IsZero() bool { return k.s == "" }

// String is the key as it is written outside the program.
func (k Key) String() string {
	if k.s == "" {
		panic("variantkey: a key that was never set is used")
	}
	return k.s
}

// Compare orders keys as their text is ordered.
func (k Key) Compare(o Key) int { return strings.Compare(k.s, o.s) }

func (k Key) MarshalText() ([]byte, error) {
	if k.s == "" {
		return nil, errors.New("variant key that was never set")
	}
	return []byte(k.s), nil
}

func (k *Key) UnmarshalText(b []byte) error {
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*k = p
	return nil
}

func (k Key) Value() (driver.Value, error) {
	b, err := k.MarshalText()
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (k *Key) Scan(src any) error {
	switch x := src.(type) {
	case string:
		return k.UnmarshalText([]byte(x))
	case []byte:
		return k.UnmarshalText(x)
	}
	return fmt.Errorf("variant key cannot be read from %T", src)
}
