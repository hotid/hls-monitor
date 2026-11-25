// Package codecfamily is the coding a track is of, by the name that is the
// same whether a playlist declares it or a container carries it.
package codecfamily

import (
	"strings"

	"github.com/hotid/hls-monitor/internal/types/enum"
)

type domain struct{}

var set = enum.NewSet("codec family")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Family is one codec family.
type Family struct{ value }

func def(name string) Family {
	_ = set // the set is there before the first value is
	return Family{enum.Def[domain](name)}
}

var (
	h264  = def("h264")
	hevc  = def("hevc")
	mP3   = def("mp3")
	aac   = def("aac")
	aC3   = def("ac3")
	eaC3  = def("eac3")
	mpeG4 = def("mpeg4")
	mpeG2 = def("mpeg2")
)

func H264() Family { return h264 }

func HEVC() Family { return hevc }

func MP3() Family { return mP3 }

func AAC() Family { return aac }

func AC3() Family { return aC3 }

func EAC3() Family { return eaC3 }

func MPEG4() Family { return mpeG4 }

func MPEG2() Family { return mpeG2 }

// Parse finds the codec family a name stands for.
func Parse(name string) (Family, error) {
	v, err := enum.Parse[domain](name)
	return Family{v}, err
}

// Values lists every codec family.
func Values() []Family {
	vs := enum.All[domain]()
	out := make([]Family, len(vs))
	for i, v := range vs {
		out[i] = Family{v}
	}
	return out
}

// Audio tells that the coding is of sound.
func (f Family) Audio() bool { return f == AAC() || f == MP3() || f == AC3() || f == EAC3() }

// Declared is the coding an entry of the CODECS attribute of a playlist
// names, such as avc1.640028 or mp4a.40.2. An entry that names a coding
// the monitor does not compare is of no family: the value is not set.
func Declared(entry string) Family { return of(entry) }

// of reads a family out of a name: an entry of CODECS, or the name a
// container gives a track.
func of(name string) Family {
	s := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(s, "avc"), s == "h264":
		return H264()
	case strings.HasPrefix(s, "hvc"), strings.HasPrefix(s, "hev"), strings.HasPrefix(s, "dvh"), s == "hevc":
		return HEVC()
	case strings.HasPrefix(s, "mp4a.40.34"), strings.HasPrefix(s, "mp4a.6b"), strings.HasPrefix(s, "mp4a.69"), s == "mp3":
		return MP3()
	case strings.HasPrefix(s, "mp4a"), strings.HasPrefix(s, "aac"):
		return AAC()
	case strings.HasPrefix(s, "ac-3"), s == "ac3":
		return AC3()
	case strings.HasPrefix(s, "ec-3"), s == "eac3":
		return EAC3()
	case strings.HasPrefix(s, "mp4v"), s == "mpeg4":
		return MPEG4()
	case s == "mpeg2video":
		return MPEG2()
	}
	return Family{}
}

// OfTrack is the coding of a track by the name its container gives it.
func OfTrack(name string) Family { return of(name) }
