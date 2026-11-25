// Package cookiesecure is when the session cookie is marked Secure.
package cookiesecure

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("cookie security")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Mode is one cookie security.
type Mode struct{ value }

func def(name string) Mode {
	_ = set // the set is there before the first value is
	return Mode{enum.Def[domain](name)}
}

var (
	auto   = def("auto")
	always = def("true")
	never  = def("false")
)

// Auto follows the scheme of the request.
func Auto() Mode { return auto }

func Always() Mode { return always }

func Never() Mode { return never }

// Parse finds the cookie security a name stands for.
func Parse(name string) (Mode, error) {
	v, err := enum.Parse[domain](name)
	return Mode{v}, err
}

// Values lists every cookie security.
func Values() []Mode {
	vs := enum.All[domain]()
	out := make([]Mode, len(vs))
	for i, v := range vs {
		out[i] = Mode{v}
	}
	return out
}
