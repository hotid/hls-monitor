// Package sourcekind is what a server answered for: a playlist or a
// segment of it.
package sourcekind

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("source kind")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Kind is one source kind.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	playlist = def("playlist")
	segment  = def("segment")
)

func Playlist() Kind { return playlist }

func Segment() Kind { return segment }

// Parse finds the source kind a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}

// Values lists every source kind.
func Values() []Kind {
	vs := enum.All[domain]()
	out := make([]Kind, len(vs))
	for i, v := range vs {
		out[i] = Kind{v}
	}
	return out
}
