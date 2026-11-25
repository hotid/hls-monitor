// Package errcode is what the API says went wrong, for a program to act
// on and for the interface to put into the reader's language; the words
// that go with it are for a person.
package errcode

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("error code")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Code is one error code.
type Code struct{ value }

func def(name string) Code {
	_ = set // the set is there before the first value is
	return Code{enum.Def[domain](name)}
}

var (
	agentDisabled      = def("agent_disabled")
	agentUnavailable   = def("agent_unavailable")
	badCredentials     = def("bad_credentials")
	badRequest         = def("bad_request")
	badToken           = def("bad_token")
	capturesOff        = def("captures_off")
	conflict           = def("conflict")
	csrf               = def("csrf")
	diagnoseBusy       = def("diagnose_busy")
	forbidden          = def("forbidden")
	internal           = def("internal")
	methodNotAllowed   = def("method_not_allowed")
	metricsUnavailable = def("metrics_unavailable")
	noClip             = def("no_clip")
	noPicture          = def("no_picture")
	noRoom             = def("no_room")
	notFound           = def("not_found")
	notLocal           = def("not_local")
	tooLarge           = def("too_large")
	tooManyAttempts    = def("too_many_attempts")
	unauthorized       = def("unauthorized")
	upstream           = def("upstream")
)

func AgentDisabled() Code { return agentDisabled }

func AgentUnavailable() Code { return agentUnavailable }

func BadCredentials() Code { return badCredentials }

func BadRequest() Code { return badRequest }

func BadToken() Code { return badToken }

func CapturesOff() Code { return capturesOff }

func Conflict() Code { return conflict }

func CSRF() Code { return csrf }

func DiagnoseBusy() Code { return diagnoseBusy }

func Forbidden() Code { return forbidden }

func Internal() Code { return internal }

func MethodNotAllowed() Code { return methodNotAllowed }

func MetricsUnavailable() Code { return metricsUnavailable }

func NoClip() Code { return noClip }

func NoPicture() Code { return noPicture }

func NoRoom() Code { return noRoom }

func NotFound() Code { return notFound }

func NotLocal() Code { return notLocal }

func TooLarge() Code { return tooLarge }

func TooManyAttempts() Code { return tooManyAttempts }

func Unauthorized() Code { return unauthorized }

func Upstream() Code { return upstream }

// Parse finds the error code a name stands for.
func Parse(name string) (Code, error) {
	v, err := enum.Parse[domain](name)
	return Code{v}, err
}

// Values lists every error code.
func Values() []Code {
	vs := enum.All[domain]()
	out := make([]Code, len(vs))
	for i, v := range vs {
		out[i] = Code{v}
	}
	return out
}
