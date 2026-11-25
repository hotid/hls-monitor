// Package readiness is whether the server can do its job.
package readiness

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("readiness")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Status is one readiness.
type Status struct{ value }

func def(name string) Status {
	_ = set // the set is there before the first value is
	return Status{enum.Def[domain](name)}
}

var (
	ok          = def("ok")
	degraded    = def("degraded")
	unavailable = def("unavailable")
)

func OK() Status { return ok }

// Degraded: the server works but something needs attention.
func Degraded() Status { return degraded }

// Unavailable: it cannot do its job.
func Unavailable() Status { return unavailable }

// Parse finds the readiness a name stands for.
func Parse(name string) (Status, error) {
	v, err := enum.Parse[domain](name)
	return Status{v}, err
}

// Values lists every readiness.
func Values() []Status {
	vs := enum.All[domain]()
	out := make([]Status, len(vs))
	for i, v := range vs {
		out[i] = Status{v}
	}
	return out
}
