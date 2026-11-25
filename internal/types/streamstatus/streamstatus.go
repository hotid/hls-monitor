// Package streamstatus is the state a stream is shown in. The last three
// come from the results of checks, and are defined from the best to the
// worst; the first two say why there is no result to show.
package streamstatus

import (
	"github.com/hotid/hls-monitor/internal/types/enum"
	"github.com/hotid/hls-monitor/internal/types/status"
)

type domain struct{}

var set = enum.NewSet("stream status")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Status is one stream status.
type Status struct{ value }

func def(name string) Status {
	_ = set // the set is there before the first value is
	return Status{enum.Def[domain](name)}
}

var (
	noData   = def("nodata")
	disabled = def("disabled")
	ok       = def("ok")
	warn     = def("warn")
	fail     = def("fail")
)

// NoData: no vantage point has a fresh result.
func NoData() Status { return noData }

// Disabled: the stream, or the agent, is not to be checked.
func Disabled() Status { return disabled }

func OK() Status { return ok }

func Warn() Status { return warn }

func Fail() Status { return fail }

// Parse finds the stream status a name stands for.
func Parse(name string) (Status, error) {
	v, err := enum.Parse[domain](name)
	return Status{v}, err
}

// Values lists every stream status.
func Values() []Status {
	vs := enum.All[domain]()
	out := make([]Status, len(vs))
	for i, v := range vs {
		out[i] = Status{v}
	}
	return out
}

// Of is the state a result of a check shows a stream in.
func Of(st status.Status) Status {
	switch st {
	case status.OK():
		return ok
	case status.Warn():
		return warn
	case status.Fail():
		return fail
	}
	panic("streamstatus: of a status that was never set")
}

// Checked tells that the state comes from the result of a check.
func (s Status) Checked() bool { return s == ok || s == warn || s == fail }

// Worse tells whether s, the result of a check, is worse than o. A state
// that is no result of a check is worse than none and better than any.
func (s Status) Worse(o Status) bool {
	return s.Checked() && (!o.Checked() || s.weight() > o.weight())
}

// weight orders the results of a check by how bad they are; it does not
// follow from the order the states are defined in.
func (s Status) weight() int {
	switch s {
	case ok:
		return 1
	case warn:
		return 2
	case fail:
		return 3
	}
	panic("streamstatus: the weight of a state that is no result of a check")
}
