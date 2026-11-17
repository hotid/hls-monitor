package httpx

import (
	"crypto/tls"
	"net"
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
