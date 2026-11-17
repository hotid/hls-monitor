//go:build linux

package httpx

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

func tcpInfo(conn *net.TCPConn) (TCPInfo, bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return TCPInfo{}, false
	}
	var info *unix.TCPInfo
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		info, gerr = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	}); err != nil || gerr != nil || info == nil {
		return TCPInfo{}, false
	}
	// The smoothed time includes how long the other side put off its
	// acknowledgement, tens of milliseconds on a connection that waits for
	// an answer; the smallest of the last minutes does not.
	rtt := info.Rtt
	if info.Min_rtt > 0 && info.Min_rtt < rtt {
		rtt = info.Min_rtt
	}
	return TCPInfo{
		RTT:         time.Duration(rtt) * time.Microsecond,
		RTTVar:      time.Duration(info.Rttvar) * time.Microsecond,
		Retransmits: info.Total_retrans,
		OutOfOrder:  info.Rcv_ooopack,
	}, true
}
