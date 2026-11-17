package httpx

import (
	"crypto/tls"
	"net"
	"sync"
	"time"
)

// TCPInfo is what the kernel knows about a connection. The counters run
// for the life of the connection, which may serve many requests.
type TCPInfo struct {
	// RTT is the smoothed round trip time, RTTVar how much it varies.
	RTT    time.Duration
	RTTVar time.Duration
	// Retransmits counts the segments this side had to send again: the
	// loss on the way from here, seen by the one who sends.
	Retransmits uint32
	// OutOfOrder counts the segments that arrived out of order: on the
	// receiving side the only sign of loss on the way here, and not a
	// certain one — the network may also just reorder.
	OutOfOrder uint32
	// NewRetransmits and NewOutOfOrder are how much the two counters grew
	// since the previous request this client made on the same connection;
	// on a new connection, all of it. Connections are shared by whatever
	// is requested from one server, so the growth belongs to the server
	// more than to the one request that happened to read it.
	NewRetransmits uint32
	NewOutOfOrder  uint32
}

// connCounters remembers, per connection, the counters last read.
type connCounters struct {
	mu   sync.Mutex
	seen map[string][2]uint32
}

// grown fills the New fields of info for the connection.
func (c *connCounters) grown(conn net.Conn, info *TCPInfo) {
	key := conn.LocalAddr().String() + ">" + conn.RemoteAddr().String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil || len(c.seen) > 4096 {
		c.seen = map[string][2]uint32{} // connections come and go
	}
	before, known := c.seen[key]
	if !known || info.Retransmits < before[0] || info.OutOfOrder < before[1] {
		before = [2]uint32{}
	}
	info.NewRetransmits, info.NewOutOfOrder = info.Retransmits-before[0], info.OutOfOrder-before[1]
	c.seen[key] = [2]uint32{info.Retransmits, info.OutOfOrder}
}

// ConnInfo reads the kernel's figures of a connection, through TLS if it
// is wrapped in it. It reports false where that is not possible: on
// systems other than Linux, and for connections that are not TCP.
func ConnInfo(conn net.Conn) (TCPInfo, bool) {
	if t, ok := conn.(*tls.Conn); ok {
		conn = t.NetConn()
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return TCPInfo{}, false
	}
	return tcpInfo(tcp)
}
