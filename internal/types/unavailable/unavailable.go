// Package unavailable is why a stream cannot be watched from a vantage
// point just now.
package unavailable

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("reason a vantage point cannot be used")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Why is one reason a vantage point cannot be used.
type Why struct{ value }

func def(name string) Why {
	_ = set // the set is there before the first value is
	return Why{enum.Def[domain](name)}
}

var (
	offline = def("offline")
	tooOld  = def("too_old")
)

func Offline() Why { return offline }

// TooOld: an agent that does not take such requests.
func TooOld() Why { return tooOld }

// Parse finds the reason a vantage point cannot be used a name stands for.
func Parse(name string) (Why, error) {
	v, err := enum.Parse[domain](name)
	return Why{v}, err
}

// Values lists every reason a vantage point cannot be used.
func Values() []Why {
	vs := enum.All[domain]()
	out := make([]Why, len(vs))
	for i, v := range vs {
		out[i] = Why{v}
	}
	return out
}
