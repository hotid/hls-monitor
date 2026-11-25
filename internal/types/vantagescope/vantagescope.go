// Package vantagescope sums up which vantage points saw a problem.
package vantagescope

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("vantage scope")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Scope is one vantage scope.
type Scope struct{ value }

func def(name string) Scope {
	_ = set // the set is there before the first value is
	return Scope{enum.Def[domain](name)}
}

var (
	all_     = def("all")
	some     = def("some")
	onlyThis = def("only_this")
	single   = def("single")
)

// All of them saw it.
func All() Scope { return all_ }

func Some() Scope { return some }

// OnlyThis: the one that reported it, and no other.
func OnlyThis() Scope { return onlyThis }

// Single: there is nothing to compare with.
func Single() Scope { return single }

// Parse finds the vantage scope a name stands for.
func Parse(name string) (Scope, error) {
	v, err := enum.Parse[domain](name)
	return Scope{v}, err
}

// Values lists every vantage scope.
func Values() []Scope {
	vs := enum.All[domain]()
	out := make([]Scope, len(vs))
	for i, v := range vs {
		out[i] = Scope{v}
	}
	return out
}
