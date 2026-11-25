// Package role is what a user of the monitor may do.
package role

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("role")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Role is one role.
type Role struct{ value }

func def(name string) Role {
	_ = set // the set is there before the first value is
	return Role{enum.Def[domain](name)}
}

var (
	admin  = def("admin")
	viewer = def("viewer")
)

// Admin changes what is monitored and who may see it.
func Admin() Role { return admin }

// Viewer looks.
func Viewer() Role { return viewer }

// Parse finds the role a name stands for.
func Parse(name string) (Role, error) {
	v, err := enum.Parse[domain](name)
	return Role{v}, err
}

// Values lists every role.
func Values() []Role {
	vs := enum.All[domain]()
	out := make([]Role, len(vs))
	for i, v := range vs {
		out[i] = Role{v}
	}
	return out
}
