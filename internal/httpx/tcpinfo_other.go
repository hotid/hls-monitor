//go:build !linux

package httpx

import "net"

func tcpInfo(*net.TCPConn) (TCPInfo, bool) { return TCPInfo{}, false }
