// Package picturemode is what the picture and the sound of a stream are
// checked for.
package picturemode

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("picture mode")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Mode is one picture mode.
type Mode struct{ value }

func def(name string) Mode {
	_ = set // the set is there before the first value is
	return Mode{enum.Def[domain](name)}
}

var (
	check  = def("")
	static = def("static")
	off    = def("off")
)

// Check: everything. It is what a stream has unless told otherwise, and
// its name outside the program is the empty one.
func Check() Mode { return check }

// Static: a picture that does not change is what the stream shows:
// radio, a test card.
func Static() Mode { return static }

// Off: not looked at.
func Off() Mode { return off }

// Parse finds the picture mode a name stands for.
func Parse(name string) (Mode, error) {
	v, err := enum.Parse[domain](name)
	return Mode{v}, err
}

// Values lists every picture mode.
func Values() []Mode {
	vs := enum.All[domain]()
	out := make([]Mode, len(vs))
	for i, v := range vs {
		out[i] = Mode{v}
	}
	return out
}
