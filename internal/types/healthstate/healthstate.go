// Package healthstate is how one of the things the server depends on is
// doing.
package healthstate

import (
	"github.com/hotid/hls-monitor/internal/types/enum"
	"github.com/hotid/hls-monitor/internal/types/overload"
)

type domain struct{}

var set = enum.NewSet("health state")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// State is one health state.
type State struct{ value }

func def(name string) State {
	_ = set // the set is there before the first value is
	return State{enum.Def[domain](name)}
}

var (
	ok        = def("ok")
	down      = def("down")
	disabled  = def("disabled")
	behind    = def("behind")
	processor = def("processor")
)

func OK() State { return ok }

func Down() State { return down }

// Disabled: not configured, and not needed.
func Disabled() State { return disabled }

// Behind: checks start later than they should.
func Behind() State { return behind }

// Processor: the process does not get the processor time it needs.
func Processor() State { return processor }

// Parse finds the health state a name stands for.
func Parse(name string) (State, error) {
	v, err := enum.Parse[domain](name)
	return State{v}, err
}

// Values lists every health state.
func Values() []State {
	vs := enum.All[domain]()
	out := make([]State, len(vs))
	for i, v := range vs {
		out[i] = State{v}
	}
	return out
}

// Short is the state of a process that is short of something.
func Short(of overload.Reason) State {
	switch of {
	case overload.Processor():
		return processor
	}
	panic("healthstate: short of what was never said")
}
