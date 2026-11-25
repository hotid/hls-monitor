// Package capturestate is what has become of a capture. The states are
// defined in the order a capture is given up in when room runs out: one
// not confirmed first, one kept last.
package capturestate

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("capture state")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// State is one capture state.
type State struct{ value }

func def(name string) State {
	_ = set // the set is there before the first value is
	return State{enum.Def[domain](name)}
}

var (
	pending = def("pending")
	found   = def("found")
	kept    = def("kept")
)

// Pending: made on suspicion, not confirmed yet.
func Pending() State { return pending }

// Found: no incident came of it, and a decoder found fault with its
// segment: kept for a while, within a small limit.
func Found() State { return found }

// Kept: an incident came of it, or somebody asked for it.
func Kept() State { return kept }

// Parse finds the capture state a name stands for.
func Parse(name string) (State, error) {
	v, err := enum.Parse[domain](name)
	return State{v}, err
}

// Values lists every capture state.
func Values() []State {
	vs := enum.All[domain]()
	out := make([]State, len(vs))
	for i, v := range vs {
		out[i] = State{v}
	}
	return out
}

// GoesBefore tells whether a capture in the state s is given up before
// one in the state o when room is needed: what is least sure goes first.
// The order is said here and does not follow from the order the states
// are defined in.
func (s State) GoesBefore(o State) bool { return s.weight() < o.weight() }

func (s State) weight() int {
	switch s {
	case pending:
		return 1
	case found:
		return 2
	case kept:
		return 3
	}
	panic("capturestate: the weight of a state that was never set")
}
