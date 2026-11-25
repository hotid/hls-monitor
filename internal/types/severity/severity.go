// Package severity is how bad a problem is.
//
// Info marks something worth recording that is not wrong in itself, such
// as the stream being served by a different server; it does not affect the
// status. The severities are defined from the mildest to the worst.
package severity

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("severity")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Severity is one severity.
type Severity struct{ value }

func def(name string) Severity {
	_ = set // the set is there before the first value is
	return Severity{enum.Def[domain](name)}
}

var (
	info = def("info")
	warn = def("warn")
	crit = def("crit")
)

func Info() Severity { return info }

func Warn() Severity { return warn }

func Crit() Severity { return crit }

// Parse finds the severity a name stands for.
func Parse(name string) (Severity, error) {
	v, err := enum.Parse[domain](name)
	return Severity{v}, err
}

// Values lists every severity.
func Values() []Severity {
	vs := enum.All[domain]()
	out := make([]Severity, len(vs))
	for i, v := range vs {
		out[i] = Severity{v}
	}
	return out
}

// Worse tells whether s is worse than o.
func (s Severity) Worse(o Severity) bool { return s.weight() > o.weight() }

// weight orders severities by how bad they are. It is said here and does
// not follow from the order they are defined in.
func (s Severity) weight() int {
	switch s {
	case info:
		return 1
	case warn:
		return 2
	case crit:
		return 3
	}
	panic("severity: the weight of a severity that was never set")
}
