package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
)

// A playlist names the addresses of its segments, and a redirect names
// where to go next. A stream on a public address can thus send the monitor
// to an address inside the network the monitor runs in — a neighbour's
// admin page, a cloud metadata service — and what comes back ends up in
// timings, error texts and playlist snapshots. Reach is what a request is
// allowed to connect to. It is applied to the address a connection is
// actually made to, after name resolution, so a name that resolves inside
// does not get around it.

// Reach says which addresses a request may connect to.
type Reach int

const (
	// ReachAny: no restriction.
	ReachAny Reach = iota
	// ReachPublic: only public addresses.
	ReachPublic
	// ReachFirstAny: the first connection may go anywhere; the following
	// ones (redirects) may go inside only if the first one did.
	ReachFirstAny
)

// ErrNotPublic is the reason a connection was refused for.
var ErrNotPublic = errors.New("the address is not a public one, and the stream's own address is: refused")

type reachKey struct{}

// reachState follows one request across its connections.
type reachState struct {
	mode   Reach
	cancel context.CancelCauseFunc
	// viaProxy is set while the request, or the redirect of it being
	// followed, goes through a proxy: see proxyOf.
	viaProxy atomic.Bool

	mu     sync.Mutex
	got    int  // connections the request has been given
	inside bool // the first of them was not to a public address
}

// allowed reports whether the request may use a connection to addr
// (ip:port). The caller holds no lock.
func (st *reachState) allowed(addr string) bool {
	if st.mode == ReachAny || st.viaProxy.Load() {
		return true
	}
	public := addrPublic(addr)
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case public:
		return true
	case st.mode == ReachFirstAny && (st.got == 0 || st.inside):
		return true
	}
	return false
}

// use records that the request was given a connection to addr, new or
// kept from before, and ends the request when it may not have it: false
// is returned then.
func (st *reachState) use(addr string) bool {
	if !st.allowed(addr) {
		st.cancel(fmt.Errorf("%s: %w", hostOf(addr), ErrNotPublic))
		return false
	}
	st.mu.Lock()
	if st.got == 0 {
		st.inside = !addrPublic(addr)
	}
	st.got++
	st.mu.Unlock()
	return true
}

// controlDial is the dialer's hook: it refuses a connection before
// anything is sent.
func controlDial(ctx context.Context, _, address string, _ syscall.RawConn) error {
	if st, ok := ctx.Value(reachKey{}).(*reachState); ok && !st.allowed(address) {
		return ErrNotPublic
	}
	return nil
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func addrPublic(addr string) bool {
	ip, err := netip.ParseAddr(hostOf(addr))
	if err != nil {
		return false
	}
	return Public(ip)
}

// notPublic are the ranges that are not loopback, private or link-local
// by the library's definition, and still do not belong to the Internet.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // shared address space of carriers
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking; used for container networks
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use address translation
	netip.MustParsePrefix("2001::/32"),      // Teredo: the address inside is hidden
	netip.MustParsePrefix("fec0::/10"),      // site-local, of old
}

// carried are the ranges of IPv6 whose addresses carry an address of IPv4
// that the packet ends up at, and where in them it sits.
var carried = []struct {
	prefix netip.Prefix
	at     int
}{
	{netip.MustParsePrefix("64:ff9b::/96"), 12}, // address translation
	{netip.MustParsePrefix("2002::/16"), 2},     // 6to4
}

// Public reports whether ip is an address of the Internet: not loopback,
// not private, not link-local, not otherwise reserved for local use.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, c := range carried {
		if c.prefix.Contains(ip) {
			b := ip.As16()
			ip = netip.AddrFrom4([4]byte(b[c.at : c.at+4]))
			break
		}
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
