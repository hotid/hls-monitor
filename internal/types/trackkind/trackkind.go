// Package trackkind is what an elementary stream of a segment carries.
package trackkind

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("track kind")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Kind is one track kind.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	video = def("video")
	audio = def("audio")
	other = def("other")
)

func Video() Kind { return video }

func Audio() Kind { return audio }

func Other() Kind { return other }

// Parse finds the track kind a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}

// Values lists every track kind.
func Values() []Kind {
	vs := enum.All[domain]()
	out := make([]Kind, len(vs))
	for i, v := range vs {
		out[i] = Kind{v}
	}
	return out
}
