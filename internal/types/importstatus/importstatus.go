// Package importstatus is what became, or would become, of one channel
// of a list given to the monitor to add.
package importstatus

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("import status")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Status is one import status.
type Status struct{ value }

func def(name string) Status {
	_ = set // the set is there before the first value is
	return Status{enum.Def[domain](name)}
}

var (
	new_      = def("new")
	created   = def("created")
	exists    = def("exists")
	duplicate = def("duplicate")
	invalid   = def("invalid")
	failed    = def("failed")
)

// New: a stream that would be added.
func New() Status { return new_ }

// Created: one that was.
func Created() Status { return created }

// Exists: the address is checked already.
func Exists() Status { return exists }

// Duplicate: the list has it twice.
func Duplicate() Status { return duplicate }

// Invalid: not an address the monitor can check.
func Invalid() Status { return invalid }

// Failed: adding it went wrong.
func Failed() Status { return failed }

// Parse finds the import status a name stands for.
func Parse(name string) (Status, error) {
	v, err := enum.Parse[domain](name)
	return Status{v}, err
}

// Values lists every import status.
func Values() []Status {
	vs := enum.All[domain]()
	out := make([]Status, len(vs))
	for i, v := range vs {
		out[i] = Status{v}
	}
	return out
}
