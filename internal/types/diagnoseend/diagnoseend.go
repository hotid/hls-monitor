// Package diagnoseend is why a live diagnosis ended. One that ended
// because the reader went away has no reason: there is nobody to tell.
package diagnoseend

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("reason a diagnosis ended")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Reason is one reason a diagnosis ended.
type Reason struct{ value }

func def(name string) Reason {
	_ = set // the set is there before the first value is
	return Reason{enum.Def[domain](name)}
}

var (
	timeV       = def("time")
	notTakenV   = def("agent_not_taking")
	refusedV    = def("agent_refused")
	tooOldV     = def("agent_too_old")
	unreadableV = def("agent_unreadable")
	silentV     = def("agent_silent")
)

// Time: the time asked for has passed.
func Time() Reason { return timeV }

// NotTaken: the agent did not take the work: it is out of contact or busy.
func NotTaken() Reason { return notTakenV }

// Refused: the agent took the work and said it would not do it.
func Refused() Reason { return refusedV }

// TooOld: the agent answered with something else: it does not know of diagnoses.
func TooOld() Reason { return tooOldV }

// Unreadable: what the agent sent cannot be read.
func Unreadable() Reason { return unreadableV }

// Silent: the agent stopped answering.
func Silent() Reason { return silentV }

// Parse finds the reason a diagnosis ended a name stands for.
func Parse(name string) (Reason, error) {
	v, err := enum.Parse[domain](name)
	return Reason{v}, err
}
