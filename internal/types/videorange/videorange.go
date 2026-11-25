// Package videorange is the dynamic range of a picture when it is a high
// one. A picture of the standard range has none: the value is not set.
package videorange

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("video range")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Range is one video range.
type Range struct{ value }

func def(name string) Range {
	_ = set // the set is there before the first value is
	return Range{enum.Def[domain](name)}
}

var (
	pq  = def("PQ")
	hlg = def("HLG")
)

// PQ is HDR10 and Dolby Vision.
func PQ() Range { return pq }

func HLG() Range { return hlg }

// Parse finds the video range a name stands for.
func Parse(name string) (Range, error) {
	v, err := enum.Parse[domain](name)
	return Range{v}, err
}

// Values lists every video range.
func Values() []Range {
	vs := enum.All[domain]()
	out := make([]Range, len(vs))
	for i, v := range vs {
		out[i] = Range{v}
	}
	return out
}
