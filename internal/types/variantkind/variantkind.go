// Package variantkind is what a playlist of a stream is.
package variantkind

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("variant kind")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Kind is one variant kind.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	master    = def("master")
	media     = def("media")
	video     = def("video")
	audio     = def("audio")
	subtitles = def("subtitles")
)

// Master is the multivariant playlist itself.
func Master() Kind { return master }

// Media is a stream whose URL is a media playlist.
func Media() Kind { return media }

func Video() Kind { return video }

func Audio() Kind { return audio }

func Subtitles() Kind { return subtitles }

// Parse finds the variant kind a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}

// Values lists every variant kind.
func Values() []Kind {
	vs := enum.All[domain]()
	out := make([]Kind, len(vs))
	for i, v := range vs {
		out[i] = Kind{v}
	}
	return out
}
