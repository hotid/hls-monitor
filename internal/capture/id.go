package capture

import (
	"errors"
	"fmt"
	"time"

	"github.com/hotid/hls-monitor/internal/types/id"
)

// ID names a capture. It is also the name of the directory the capture is
// kept in and a part of the address it is asked for at, and for that it
// is made of letters, digits and hyphens and nothing else: an ID cannot
// be a path, whoever wrote it. The zero ID is that of no capture.
type ID struct{ name string }

// newID names a capture by when it was asked for, of which stream and at
// which segment.
func newID(at time.Time, stream id.Stream, seq uint64) ID {
	return ID{fmt.Sprintf("%s-s%d-%d", at.UTC().Format("20060102T150405"), stream, seq)}
}

// From is the name a capture has on the server when it came from an
// agent: two agents may capture the same stream at the same moment.
func (i ID) From(agent id.Agent) ID { return ID{i.name + "-a" + agent.String()} }

// ParseID reads an ID written as text: in an address, in the description
// of a capture, as the name of a directory.
func ParseID(text string) (ID, error) {
	if text == "" || len(text) > 128 {
		return ID{}, errors.New("not the ID of a capture")
	}
	for _, r := range text {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
			return ID{}, errors.New("not the ID of a capture")
		}
	}
	return ID{text}, nil
}

// IsZero tells that it names no capture.
func (i ID) IsZero() bool { return i.name == "" }

func (i ID) String() string { return i.name }

func (i ID) MarshalText() ([]byte, error) {
	if i.name == "" {
		return nil, errors.New("the ID of a capture that was never set")
	}
	return []byte(i.name), nil
}

func (i *ID) UnmarshalText(b []byte) error {
	v, err := ParseID(string(b))
	if err != nil {
		return err
	}
	*i = v
	return nil
}
