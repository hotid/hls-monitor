// Package container is what a media segment is packed as.
package container

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("container")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Kind is one container.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	ts      = def("ts")
	fmP4    = def("fmp4")
	aac     = def("aac")
	vtt     = def("vtt")
	unknown = def("unknown")
)

// TS is MPEG-TS.
func TS() Kind { return ts }

// FMP4 is fragmented MP4.
func FMP4() Kind { return fmP4 }

// AAC is sound alone, in ADTS.
func AAC() Kind { return aac }

// VTT is WebVTT subtitles.
func VTT() Kind { return vtt }

// Unknown is what the monitor does not read.
func Unknown() Kind { return unknown }

// Parse finds the container a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}

// Values lists every container.
func Values() []Kind {
	vs := enum.All[domain]()
	out := make([]Kind, len(vs))
	for i, v := range vs {
		out[i] = Kind{v}
	}
	return out
}
