// Package status is what a check cycle came to, for a stream or for one
// of its playlists. The statuses are defined from the best to the worst.
package status

import (
	"github.com/hotid/hls-monitor/internal/types/enum"
	"github.com/hotid/hls-monitor/internal/types/severity"
)

type domain struct{}

var set = enum.NewSet("status")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Status is one status.
type Status struct{ value }

func def(name string) Status {
	_ = set // the set is there before the first value is
	return Status{enum.Def[domain](name)}
}

var (
	ok   = def("ok")
	warn = def("warn")
	fail = def("fail")
)

func OK() Status { return ok }

func Warn() Status { return warn }

func Fail() Status { return fail }

// Parse finds the status a name stands for.
func Parse(name string) (Status, error) {
	v, err := enum.Parse[domain](name)
	return Status{v}, err
}

// Values lists every status.
func Values() []Status {
	vs := enum.All[domain]()
	out := make([]Status, len(vs))
	for i, v := range vs {
		out[i] = Status{v}
	}
	return out
}

// Of is the status a problem of the given severity makes: a critical one
// fails the check, a warning warns, and something told for information
// leaves it as it was.
func Of(sev severity.Severity) Status {
	switch sev {
	case severity.Crit():
		return fail
	case severity.Warn():
		return warn
	case severity.Info():
		return ok
	}
	panic("status: of a severity that was never set")
}

// Worse tells whether s is worse than o.
func (s Status) Worse(o Status) bool { return s.Rank() > o.Rank() }

// Rank is the status as the number the series of statuses hold: 0 for
// good, 1 for a warning, 2 for a failure. The numbers are what is stored
// and what queries compare with: they do not follow from the order the
// statuses are defined in, and do not change with it.
func (s Status) Rank() float64 {
	switch s {
	case ok:
		return 0
	case warn:
		return 1
	case fail:
		return 2
	}
	panic("status: the rank of a status that was never set")
}

// OfRank reads a status back from the number a series holds it as; false
// for a number that is none of them.
func OfRank(v float64) (Status, bool) {
	switch v {
	case 0:
		return ok, true
	case 1:
		return warn, true
	case 2:
		return fail, true
	}
	return Status{}, false
}
