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
	agent int64
	req   agentproto.FetchRequest
	done  chan fetchDone
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
	queues  map[int64]chan *fetchJob
	pending map[string]*fetchJob
}

func (r *relays) queue(agent int64) chan *fetchJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queues == nil {
		r.queues, r.pending = map[int64]chan *fetchJob{}, map[string]*fetchJob{}
	}
	q := r.queues[agent]
	if q == nil {
		q = make(chan *fetchJob)
		r.queues[agent] = q
	}
	return q
}

// fetchThroughAgent has an agent fetch what job names.
func (s *Server) fetchThroughAgent(ctx context.Context, agent int64, req agentproto.FetchRequest) (agentproto.FetchResult, []byte, *relayTiming) {
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
		fail(c, http.StatusForbidden, "agent_disabled", "the agent is disabled")
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
		fail(c, http.StatusNotFound, "not_found", "nobody waits for this any more")
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
	select {
	case job.done <- fetchDone{res: res, body: body, upload: time.Since(began)}:
	default:
	}
	c.Status(http.StatusNoContent)
}
