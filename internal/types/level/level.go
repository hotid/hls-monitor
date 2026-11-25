// Package level is how deep a stream is checked.
package level

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("check level")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Level is one check level.
type Level struct{ value }

func def(name string) Level {
	_ = set // the set is there before the first value is
	return Level{enum.Def[domain](name)}
}

var (
	playlist  = def("playlist")
	header    = def("header")
	segments  = def("segments")
	container = def("container")
)

// Playlist: playlists only.
func Playlist() Level { return playlist }

// Header requests only the beginning of every new segment: it proves the
// segment exists, learns its size and which server has it, and reads
// what the first packets tell (tracks, parameters, the first timestamp,
// whether it starts with a keyframe) at a small fraction of the traffic.
func Header() Level { return header }

// Segments: plus segment downloads.
func Segments() Level { return segments }

// Container: plus container analysis.
func Container() Level { return container }

// Parse finds the check level a name stands for.
func Parse(name string) (Level, error) {
	v, err := enum.Parse[domain](name)
	return Level{v}, err
}

// Values lists every check level.
func Values() []Level {
	vs := enum.All[domain]()
	out := make([]Level, len(vs))
	for i, v := range vs {
		out[i] = Level{v}
	}
	return out
}
