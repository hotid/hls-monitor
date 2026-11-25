// Package overload is what a process that checks streams can be short
// of. A process that cannot keep up measures its own delay, not the
// stream's, and says so with what it found.
package overload

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("overload reason")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Reason is one overload reason.
type Reason struct{ value }

func def(name string) Reason {
	_ = set // the set is there before the first value is
	return Reason{enum.Def[domain](name)}
}

var (
	processor = def("processor")
)

// Processor: the process does not get the processor time it needs.
func Processor() Reason { return processor }

// Parse finds the overload reason a name stands for.
func Parse(name string) (Reason, error) {
	v, err := enum.Parse[domain](name)
	return Reason{v}, err
}

// Values lists every overload reason.
func Values() []Reason {
	vs := enum.All[domain]()
	out := make([]Reason, len(vs))
	for i, v := range vs {
		out[i] = Reason{v}
	}
	return out
}
