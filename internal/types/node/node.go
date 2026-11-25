// Package node names a server that answered a request.
package node

import (
	"errors"
	"net/netip"
	"strings"
)

// Node is a server as the monitor tells one from another: the host that
// answered and the address the connection went to. Behind a balancer or an
// anycast address one host is many servers, and one address many hosts.
//
// The zero Node is no server: nothing answered, or it is not known what did.
type Node struct {
	host string
	ip   netip.Addr
}

// Of names the server of a host at an address; either may be unknown.
func Of(host string, ip netip.Addr) Node { return Node{host: host, ip: ip.Unmap()} }

// Host is the host that answered, with the port when it is not the default.
func (n Node) Host() string { return n.host }

// IP is the address the connection went to; not valid when it is not known.
func (n Node) IP() netip.Addr { return n.ip }

// IsZero tells that there is no server to name.
func (n Node) IsZero() bool { return n.host == "" && !n.ip.IsValid() }

// String names the server for a reader and for the record of an incident:
// the host and the address with a space between. No server has no name.
func (n Node) String() string {
	if n.IsZero() {
		return ""
	}
	if !n.ip.IsValid() {
		return n.host
	}
	return n.host + " " + n.ip.String()
}

// Parse reads a server from the record of an incident.
func Parse(text string) (Node, error) {
	host := strings.TrimSpace(text)
	if host == "" {
		return Node{}, errors.New("empty server")
	}
	var ip netip.Addr
	if i := strings.LastIndex(host, " "); i > 0 {
		// What follows the host is the address; when it is none, the
		// record is of a server whose address was not known.
		if a, err := netip.ParseAddr(host[i+1:]); err == nil {
			host, ip = host[:i], a
		}
	}
	return Node{host: host, ip: ip}, nil
}

func (n Node) MarshalText() ([]byte, error) { return []byte(n.String()), nil }

func (n *Node) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*n = Node{}
		return nil
	}
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*n = p
	return nil
}
