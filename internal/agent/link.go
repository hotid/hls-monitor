package agent

import (
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/httpx"
)

// linkWatch keeps an eye on the way from the agent to the server, on the
// requests the agent makes anyway. Everything the agent has to tell goes
// this way, and so does a stream watched through it.
type linkWatch struct {
	mu sync.Mutex
	// seen is, per connection, the retransmissions already counted: the
	// kernel counts them for the life of a connection, and one connection
	// serves many requests.
	seen        map[string]uint32
	rtt         time.Duration
	retransmits int64
	lastRequest time.Duration
	requests    int64
	failures    int64
	measured    bool
}

// watch prepares a request to the server to be measured. The request it
// returns is the one to send; done is to be called when the answer has
// been read, or has failed to come. timed tells whether how long the
// request takes says something about the way.
func (l *linkWatch) watch(req *http.Request, timed bool) (*http.Request, func(ok bool)) {
	var conn net.Conn
	start := time.Now()
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { conn = i.Conn }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	return req, func(ok bool) {
		took := time.Since(start)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.requests++
		if !ok {
			// A request the agent itself gave up on (it is stopping) is not
			// a failure of the way.
			if req.Context().Err() == nil {
				l.failures++
			}
			return
		}
		if timed {
			l.lastRequest = took
		}
		if conn == nil {
			return
		}
		info, have := httpx.ConnInfo(conn)
		if !have {
			return
		}
		l.measured, l.rtt = true, info.RTT
		if l.seen == nil {
			l.seen = map[string]uint32{}
		}
		key := conn.LocalAddr().String()
		if before, known := l.seen[key]; known && info.Retransmits >= before {
			l.retransmits += int64(info.Retransmits - before)
		} else {
			l.retransmits += int64(info.Retransmits) // a new connection
		}
		l.seen[key] = info.Retransmits
		// Connections come and go; the map must not grow for ever.
		if len(l.seen) > 64 {
			l.seen = map[string]uint32{key: info.Retransmits}
		}
	}
}

func (l *linkWatch) report() *agentproto.Link {
	l.mu.Lock()
	defer l.mu.Unlock()
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	return &agentproto.Link{RTTMs: ms(l.rtt), Retransmits: l.retransmits, RequestMs: ms(l.lastRequest),
		Requests: l.requests, Failures: l.failures, Measured: l.measured}
}
