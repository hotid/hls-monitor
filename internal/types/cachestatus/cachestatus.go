// Package cachestatus is what the headers of an answer say of the cache
// it came through. When they say nothing conclusive the value is not set.
package cachestatus

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("cache status")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Status is one cache status.
type Status struct{ value }

func def(name string) Status {
	_ = set // the set is there before the first value is
	return Status{enum.Def[domain](name)}
}

var (
	hit  = def("hit")
	miss = def("miss")
)

func Hit() Status { return hit }

func Miss() Status { return miss }

// Parse finds the cache status a name stands for.
func Parse(name string) (Status, error) {
	v, err := enum.Parse[domain](name)
	return Status{v}, err
}

// Values lists every cache status.
func Values() []Status {
	vs := enum.All[domain]()
	out := make([]Status, len(vs))
	for i, v := range vs {
		out[i] = Status{v}
	}
	return out
}
