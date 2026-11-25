// Package answerkind is what an agent's answer to a piece of work is.
package answerkind

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("kind of answer")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Kind is one kind of answer.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	fetchedV       = def("fetched")
	diagnoseCycleV = def("diagnose_cycle")
	diagnoseEndV   = def("diagnose_end")
)

// Fetched: what came of fetching an address, or of asking for a capture.
func Fetched() Kind { return fetchedV }

// DiagnoseCycle: what one check of a diagnosis found.
func DiagnoseCycle() Kind { return diagnoseCycleV }

// DiagnoseEnd: the time of a diagnosis is up.
func DiagnoseEnd() Kind { return diagnoseEndV }

// Parse finds the kind of answer a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}
