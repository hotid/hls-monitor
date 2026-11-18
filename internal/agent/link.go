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
	seen map[string][2]uint32
	// rtts are the round trip times read in the last rttWindow. Most of
	// the agent's requests wait long for their answer, and on those the
	// kernel's figure includes the time the server put off acknowledging
	// the request; the smallest of the recent ones does not.
	rtts        []rttSample
	rtt         time.Duration
	retransmits int64
	// segments counts the segments with data sent, the same way.
	segments    int64
	lastRequest time.Duration
	requests    int64
	failures    int64
	measured    bool
}

type rttSample struct {
	at  time.Time
	rtt time.Duration
}

const rttWindow = 30 * time.Second

// noteRTT adds a reading and returns the smallest of the recent ones.
func (l *linkWatch) noteRTT(now time.Time, rtt time.Duration) time.Duration {
	keep := l.rtts[:0]
	for _, s := range l.rtts {
		if now.Sub(s.at) <= rttWindow {
			keep = append(keep, s)
		}
	}
	l.rtts = append(keep, rttSample{now, rtt})
	least := rtt
	for _, s := range l.rtts {
		least = min(least, s.rtt)
	}
	return least
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
		l.measured, l.rtt = true, l.noteRTT(time.Now(), info.RTT)
		if l.seen == nil {
			l.seen = map[string][2]uint32{}
		}
		key := conn.LocalAddr().String()
		now := [2]uint32{info.Retransmits, info.SegsOut}
		if before, known := l.seen[key]; known && now[0] >= before[0] && now[1] >= before[1] {
			l.retransmits += int64(now[0] - before[0])
			l.segments += int64(now[1] - before[1])
		} else {
			l.retransmits += int64(now[0]) // a new connection
			l.segments += int64(now[1])
		}
		l.seen[key] = now
		// Connections come and go; the map must not grow for ever.
		if len(l.seen) > 64 {
			l.seen = map[string][2]uint32{key: now}
		}
	}
}

func (l *linkWatch) report() *agentproto.Link {
	l.mu.Lock()
	defer l.mu.Unlock()
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	return &agentproto.Link{RTTMs: ms(l.rtt), Retransmits: l.retransmits, RequestMs: ms(l.lastRequest),
		Requests: l.requests, Failures: l.failures, Measured: l.measured, Segments: l.segments}
}
