// Package codec is what a track of a segment is coded with.
package codec

import (
	"errors"

	"github.com/hotid/hls-monitor/internal/types/codecfamily"
)

// Codec names the coding of a track the way its container does. MPEG-TS
// tells it by a stream type, and those the monitor knows have functions
// here; fragmented MP4 tells it by the type of a box, which is taken as it
// is written (see Named).
//
// The zero Codec is that of no track.
type Codec struct {
	name string
	// family is found once, when the coding is named: it is asked for
	// with every segment read.
	family codecfamily.Family
}

func of(name string) Codec { return Codec{name: name, family: codecfamily.OfTrack(name)} }

func H264() Codec       { return of("h264") }
func HEVC() Codec       { return of("hevc") }
func MPEG2Video() Codec { return of("mpeg2video") }
func MPEG1Video() Codec { return of("mpeg1video") }
func MPEG4() Codec      { return of("mpeg4") }
func AAC() Codec        { return of("aac") }

// AACLATM is AAC in the LATM transport rather than in ADTS.
func AACLATM() Codec { return of("aac-latm") }
func MP3() Codec     { return of("mp3") }
func AC3() Codec     { return of("ac3") }
func EAC3() Codec    { return of("eac3") }
func DTS() Codec     { return of("dts") }

// Named is a coding known only by what the container calls it: the type
// of a sample entry of MP4, or a stream type of MPEG-TS the monitor has no
// name for.
func Named(name string) Codec {
	if name == "" {
		panic("codec: a coding with no name")
	}
	return of(name)
}

// IsZero tells that no coding was set.
func (c Codec) IsZero() bool { return c.name == "" }

// String is the name of the coding, for a reader.
func (c Codec) String() string {
	if c.name == "" {
		panic("codec: a coding that was never set is used")
	}
	return c.name
}

// Family is the coding by the name that is the same in a playlist and in a
// container; not set for one the monitor does not compare.
func (c Codec) Family() codecfamily.Family {
	return c.family
}

func (c Codec) MarshalText() ([]byte, error) {
	if c.name == "" {
		return nil, errors.New("codec that was never set")
	}
	return []byte(c.name), nil
}

func (c *Codec) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		return errors.New("empty codec")
	}
	*c = of(string(b))
	return nil
}
