// Package diagnosestep is what a line of the stream of a live diagnosis
// tells: that it began, what one check found, that it is over.
package diagnosestep

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("diagnosis step")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Step is one diagnosis step.
type Step struct{ value }

func def(name string) Step {
	_ = set // the set is there before the first value is
	return Step{enum.Def[domain](name)}
}

var (
	start = def("start")
	cycle = def("cycle")
	end   = def("end")
)

func Start() Step { return start }

func Cycle() Step { return cycle }

func End() Step { return end }

// Parse finds the diagnosis step a name stands for.
func Parse(name string) (Step, error) {
	v, err := enum.Parse[domain](name)
	return Step{v}, err
}

// Values lists every diagnosis step.
func Values() []Step {
	vs := enum.All[domain]()
	out := make([]Step, len(vs))
	for i, v := range vs {
		out[i] = Step{v}
	}
	return out
}
