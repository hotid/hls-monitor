package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/types/answerkind"
	"github.com/hotid/hls-monitor/internal/types/diagnoseend"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/id"
)

// Watching through an agent: the browser asks the server, the server asks
// the agent, the agent asks the origin. An agent accepts no connections,
// so it keeps a few requests to the server open and is handed work as
// their answers.

const (
	// fetchPoll is how long an agent's request for work is held.
	fetchPoll = 25 * time.Second
	// fetchHandOver is how long a fetch waits for the agent to take it:
	// an agent that is listening takes it at once.
	fetchHandOver = 5 * time.Second
)

// fetchJob is one request on its way through an agent.
type fetchJob struct {
	agent id.Agent
	req   agentproto.FetchRequest
	done  chan fetchDone
	// stream is set for work an agent answers many times over, a live
	// diagnosis: every answer goes there, in order.
	stream chan fetchDone
}

type fetchDone struct {
	res  agentproto.FetchResult
	body []byte
	// upload is how long the body took to arrive from the agent.
	upload time.Duration
}

// relayTiming is how the way through an agent went, in milliseconds.
type relayTiming struct {
	// WaitMs is how long the request waited for the agent to take it.
	WaitMs float64 `json:"wait_ms"`
	// UploadMs is how long what the agent fetched took to reach the
	// server, Bytes how much it was.
	UploadMs float64 `json:"upload_ms"`
	Bytes    int64   `json:"bytes"`
	// TotalMs is the whole of it, the agent's own fetch included.
	TotalMs float64 `json:"total_ms"`
}

// relays holds the work waiting for agents and the work they have taken.
type relays struct {
	mu      sync.Mutex
	queues  map[id.Agent]chan *fetchJob
	pending map[string]*fetchJob
}

func (r *relays) queue(agent id.Agent) chan *fetchJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queues == nil {
		r.queues, r.pending = map[id.Agent]chan *fetchJob{}, map[string]*fetchJob{}
	}
	q := r.queues[agent]
	if q == nil {
		q = make(chan *fetchJob)
		r.queues[agent] = q
	}
	return q
}

// fetchThroughAgent has an agent fetch what job names.
func (s *Server) fetchThroughAgent(ctx context.Context, agent id.Agent, req agentproto.FetchRequest) (agentproto.FetchResult, []byte, *relayTiming) {
	start := time.Now()
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	var id [12]byte
	_, _ = rand.Read(id[:])
	req.ID = hex.EncodeToString(id[:])
	job := &fetchJob{agent: agent, req: req, done: make(chan fetchDone, 1)}
	q := s.relays.queue(agent)
	s.relays.mu.Lock()
	s.relays.pending[req.ID] = job
	s.relays.mu.Unlock()
	defer func() {
		s.relays.mu.Lock()
		delete(s.relays.pending, req.ID)
		s.relays.mu.Unlock()
	}()

	hand := time.NewTimer(s.fetchHandOver)
	defer hand.Stop()
	select {
	case q <- job:
	case <-hand.C:
		return agentproto.FetchResult{Error: "the agent is not taking requests: it is out of contact, busy or too old for this"}, nil, nil
	case <-ctx.Done():
		return agentproto.FetchResult{Error: ctx.Err().Error()}, nil, nil
	}
	waited := time.Since(start)
	select {
	case d := <-job.done:
		return d.res, d.body, &relayTiming{WaitMs: ms(waited), UploadMs: ms(d.upload), Bytes: int64(len(d.body)), TotalMs: ms(time.Since(start))}
	case <-ctx.Done():
		return agentproto.FetchResult{Error: "the agent did not answer in time"}, nil, &relayTiming{WaitMs: ms(waited), TotalMs: ms(time.Since(start))}
	}
}

// agentFetchPoll godoc
//
//	@Summary		Something for the agent to fetch
//	@Description	Held for up to 25 seconds; answers 204 when there was nothing to do. An agent keeps several such requests open.
//	@Tags			agent
//	@Produce		json
//	@Param			Authorization	header		string	true	"Bearer agent key"
//	@Success		200				{object}	agentproto.FetchRequest
//	@Success		204
//	@Failure		401	{object}	apiError
//	@Router			/api/agent/v1/fetch [get]
func (s *Server) agentFetchPoll(c *gin.Context) {
	a := currentAgent(c)
	if !a.Enabled {
		fail(c, http.StatusForbidden, errcode.AgentDisabled(), "the agent is disabled")
		return
	}
	hold := time.NewTimer(s.fetchPoll)
	defer hold.Stop()
	select {
	case job := <-s.relays.queue(a.ID):
		c.JSON(http.StatusOK, job.req)
	case <-hold.C:
		c.Status(http.StatusNoContent)
	case <-c.Request.Context().Done():
		c.Abort()
	}
}

// agentFetchResult godoc
//
//	@Summary		What the agent fetched
//	@Description	The body is the body that was fetched; the X-Fetch-Result header is base64 of the JSON of a FetchResult.
//	@Tags			agent
//	@Param			Authorization	header	string	true	"Bearer agent key"
//	@Param			id				path	string	true	"ID of the fetch request"
//	@Success		204
//	@Failure		401	{object}	apiError
//	@Failure		404	{object}	apiError
//	@Router			/api/agent/v1/fetch/{id} [post]
func (s *Server) agentFetchResult(c *gin.Context) {
	a := currentAgent(c)
	s.relays.mu.Lock()
	job := s.relays.pending[c.Param("id")]
	s.relays.mu.Unlock()
	if job == nil || job.agent != a.ID {
		// The viewer went away or gave up waiting.
		fail(c, http.StatusNotFound, errcode.NotFound(), "nobody waits for this any more")
		return
	}
	var res agentproto.FetchResult
	meta, err := base64.StdEncoding.DecodeString(c.GetHeader(agentproto.HeaderFetchResult))
	if err != nil || json.Unmarshal(meta, &res) != nil {
		badRequest(c, "invalid "+agentproto.HeaderFetchResult+" header")
		return
	}
	began := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, playMaxBody+1))
	if err != nil {
		res.Error = "the agent's answer broke off: " + err.Error()
	}
	if job.stream != nil {
		// One answer of many: it waits its turn, but not for ever — a
		// reader that went away must not hold the agent.
		wait := time.NewTimer(5 * time.Second)
		defer wait.Stop()
		select {
		case job.stream <- fetchDone{res: res, body: body}:
			c.Status(http.StatusNoContent)
		case <-wait.C:
			fail(c, http.StatusNotFound, errcode.NotFound(), "nobody waits for this any more")
		}
		return
	}
	select {
	case job.done <- fetchDone{res: res, body: body, upload: time.Since(began)}:
	default:
	}
	c.Status(http.StatusNoContent)
}

// diagnoseThroughAgent has an agent check a stream every second and calls
// cycle with what every check found, until the time is up, the reader
// stops (cycle returns false) or ctx is done. It tells why it ended, with
// what the agent said where it refused; no reason when the reader stopped
// or ctx is done.
func (s *Server) diagnoseThroughAgent(ctx context.Context, agent id.Agent, d agentproto.DiagnoseJob, cycle func(agentproto.DiagnoseCycle) bool) (diagnoseend.Reason, string) {
	var none diagnoseend.Reason
	var id [12]byte
	_, _ = rand.Read(id[:])
	req := agentproto.Diagnose(d)
	req.ID = hex.EncodeToString(id[:])
	job := &fetchJob{agent: agent, req: req, stream: make(chan fetchDone, 2)}
	q := s.relays.queue(agent)
	s.relays.mu.Lock()
	s.relays.pending[job.req.ID] = job
	s.relays.mu.Unlock()
	// Once nobody is listed as waiting the agent is told so at its next
	// answer, and stops.
	defer func() {
		s.relays.mu.Lock()
		delete(s.relays.pending, job.req.ID)
		s.relays.mu.Unlock()
	}()
	hand := time.NewTimer(s.fetchHandOver)
	defer hand.Stop()
	select {
	case q <- job:
	case <-hand.C:
		return diagnoseend.NotTaken(), ""
	case <-ctx.Done():
		return none, ""
	}
	// A check takes its timeout at the most; an agent silent for much
	// longer than that is gone.
	quiet := 3*max(d.Target.Timeout.Duration(), 10*time.Second) + 15*time.Second
	for {
		silence := time.NewTimer(quiet)
		select {
		case got := <-job.stream:
			silence.Stop()
			switch answer := got.res.Answer(); {
			case got.res.Error != "":
				return diagnoseend.Refused(), got.res.Error
			case answer == answerkind.DiagnoseEnd():
				return diagnoseend.Time(), ""
			case answer != answerkind.DiagnoseCycle():
				return diagnoseend.TooOld(), ""
			}
			var c agentproto.DiagnoseCycle
			if json.Unmarshal(got.body, &c) != nil || c.Result == nil {
				return diagnoseend.Unreadable(), ""
			}
			if !cycle(c) {
				return none, ""
			}
		case <-silence.C:
			return diagnoseend.Silent(), ""
		case <-ctx.Done():
			silence.Stop()
			return none, ""
		}
	}
}
