// Package aggregation is how the values of a series within a step become
// one point of a chart.
package aggregation

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("aggregation")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// How is one aggregation.
type How struct{ value }

func def(name string) How {
	_ = set // the set is there before the first value is
	return How{enum.Def[domain](name)}
}

var (
	avg  = def("avg")
	max_ = def("max")
	min_ = def("min")
	last = def("last")
	sum  = def("sum")
)

func Avg() How { return avg }

func Max() How { return max_ }

func Min() How { return min_ }

func Last() How { return last }

func Sum() How { return sum }

// Parse finds the aggregation a name stands for.
func Parse(name string) (How, error) {
	v, err := enum.Parse[domain](name)
	return How{v}, err
}

// Values lists every aggregation.
func Values() []How {
	vs := enum.All[domain]()
	out := make([]How, len(vs))
	for i, v := range vs {
		out[i] = How{v}
	}
	return out
}
