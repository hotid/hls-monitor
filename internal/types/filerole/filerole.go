// Package filerole is what a file of a capture is.
package filerole

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("file role")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Role is one file role.
type Role struct{ value }

func def(name string) Role {
	_ = set // the set is there before the first value is
	return Role{enum.Def[domain](name)}
}

var (
	evidence = def("evidence")
	segment  = def("segment")
	init_    = def("init")
	playlist = def("playlist")
	master   = def("master")
)

// Evidence is the very bytes the check read.
func Evidence() Role { return evidence }

func Segment() Role { return segment }

// Init is the initialization section of fragmented MP4.
func Init() Role { return init_ }

func Playlist() Role { return playlist }

func Master() Role { return master }

// Parse finds the file role a name stands for.
func Parse(name string) (Role, error) {
	v, err := enum.Parse[domain](name)
	return Role{v}, err
}

// Values lists every file role.
func Values() []Role {
	vs := enum.All[domain]()
	out := make([]Role, len(vs))
	for i, v := range vs {
		out[i] = Role{v}
	}
	return out
}
