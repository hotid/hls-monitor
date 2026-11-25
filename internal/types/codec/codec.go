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
type Codec struct{ name string }

func H264() Codec       { return Codec{"h264"} }
func HEVC() Codec       { return Codec{"hevc"} }
func MPEG2Video() Codec { return Codec{"mpeg2video"} }
func MPEG1Video() Codec { return Codec{"mpeg1video"} }
func MPEG4() Codec      { return Codec{"mpeg4"} }
func AAC() Codec        { return Codec{"aac"} }

// AACLATM is AAC in the LATM transport rather than in ADTS.
func AACLATM() Codec { return Codec{"aac-latm"} }
func MP3() Codec     { return Codec{"mp3"} }
func AC3() Codec     { return Codec{"ac3"} }
func EAC3() Codec    { return Codec{"eac3"} }
func DTS() Codec     { return Codec{"dts"} }

// Named is a coding known only by what the container calls it: the type
// of a sample entry of MP4, or a stream type of MPEG-TS the monitor has no
// name for.
func Named(name string) Codec {
	if name == "" {
		panic("codec: a coding with no name")
	}
	return Codec{name}
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
	if c.name == "" {
		return codecfamily.Family{}
	}
	return codecfamily.OfTrack(c.name)
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
	c.name = string(b)
	return nil
}
