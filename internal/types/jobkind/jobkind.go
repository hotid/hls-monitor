// Package jobkind is what an agent is asked to do when it asks for work.
package jobkind

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("kind of work")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Kind is one kind of work.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	fetchV    = def("fetch")
	diagnoseV = def("diagnose")
	captureV  = def("capture")
)

// Fetch: an address is fetched for the server.
func Fetch() Kind { return fetchV }

// Diagnose: a stream is checked every second for a while.
func Diagnose() Kind { return diagnoseV }

// Capture: a capture of a stream is made at its next check.
func Capture() Kind { return captureV }

// Parse finds the kind of work a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}
