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
	return TCPInfo{
		RTT:         time.Duration(info.Rtt) * time.Microsecond,
		RTTVar:      time.Duration(info.Rttvar) * time.Microsecond,
		Retransmits: info.Total_retrans,
		OutOfOrder:  info.Rcv_ooopack,
		SegsOut:     info.Data_segs_out,
	}, true
}
