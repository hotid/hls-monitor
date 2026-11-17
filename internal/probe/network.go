package probe

import (
	"fmt"
	"sync/atomic"

	"github.com/hotid/hls-monitor/internal/httpx"
)

// Reach is what a request made for a stream may connect to under the
// policy of the process. entry marks the request for the stream's own
// address; inside tells that this address turned out not to be public.
func Reach(entry, inside bool) httpx.Reach {
	switch PrivateNetworks(privateNetworks.Load()) {
	case PrivateAllow:
		return httpx.ReachAny
	case PrivateDeny:
		return httpx.ReachPublic
	}
	switch {
	case entry:
		return httpx.ReachFirstAny
	case inside:
		return httpx.ReachAny
	}
	return httpx.ReachPublic
}

// PrivateNetworks says whether checks may connect to addresses that are
// not public: loopback, private and link-local ranges, where the admin
// pages and metadata services of the monitor's own network live.
type PrivateNetworks int32

const (
	// PrivateSame: a stream whose own address is inside may be followed
	// inside; a stream on a public address may not lead there, neither by
	// a redirect nor by what its playlists name. The default.
	PrivateSame PrivateNetworks = iota
	// PrivateAllow: no restriction.
	PrivateAllow
	// PrivateDeny: nothing inside is ever requested.
	PrivateDeny
)

var privateNetworks atomic.Int32

// SetPrivateNetworks sets the policy of the process from its name: "same",
// "allow" or "deny".
func SetPrivateNetworks(name string) error {
	switch name {
	case "", "same":
		privateNetworks.Store(int32(PrivateSame))
	case "allow":
		privateNetworks.Store(int32(PrivateAllow))
	case "deny":
		privateNetworks.Store(int32(PrivateDeny))
	default:
		return fmt.Errorf("private networks policy must be same, allow or deny, got %q", name)
	}
	return nil
}
