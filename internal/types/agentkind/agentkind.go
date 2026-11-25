// Package agentkind is where a vantage point runs.
package agentkind

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("agent kind")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Kind is one agent kind.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	local  = def("local")
	remote = def("remote")
)

// Local is the server itself.
func Local() Kind { return local }

// Remote is an agent that enrolled.
func Remote() Kind { return remote }

// Parse finds the agent kind a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}

// Values lists every agent kind.
func Values() []Kind {
	vs := enum.All[domain]()
	out := make([]Kind, len(vs))
	for i, v := range vs {
		out[i] = Kind{v}
	}
	return out
}
