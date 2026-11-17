package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/relay"
)

// Somebody watching a stream through this agent: the server cannot reach
// the agent, so the agent asks the server for such work, fetches what it
// is told to from where it stands and posts back what it got.

// fetchers is how many requests for work the agent keeps open, and so how
// many fetches it makes at once: a player asks for a playlist, a segment
// and an audio segment at the same time.
const fetchers = 4

var (
	fetchClients sync.Once
	fetchSecure  *httpx.Client
	fetchLoose   *httpx.Client
)

func (a *Agent) fetchLoop(ctx context.Context) {
	fetchClients.Do(func() {
		fetchSecure = httpx.New(httpx.Options{UserAgent: "hls-monitor-agent/" + a.opt.Version})
		fetchLoose = httpx.New(httpx.Options{UserAgent: "hls-monitor-agent/" + a.opt.Version, InsecureSkipVerify: true})
	})
	for ctx.Err() == nil {
		var job agentproto.FetchRequest
		status, err := a.request(ctx, http.MethodGet, "/api/agent/v1/fetch", "", nil, false, &job)
		switch {
		case ctx.Err() != nil:
			return
		case err == nil && status == http.StatusOK && job.ID != "":
			a.fetch(ctx, job)
		case err == nil && status == http.StatusNoContent:
			// Nothing to do; ask again.
		case err == nil && status == http.StatusNotFound:
			// A server that does not know of this yet.
			sleepCtx(ctx, 5*time.Minute)
		default:
			// Out of contact, disabled or not enrolled: configLoop reports it.
			sleepCtx(ctx, 10*time.Second)
		}
	}
}

func (a *Agent) fetch(ctx context.Context, job agentproto.FetchRequest) {
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hc := fetchSecure
	if job.Insecure {
		hc = fetchLoose
	}
	res, body := relay.Fetch(fctx, hc, job)
	meta, _ := json.Marshal(res)
	req, err := http.NewRequestWithContext(fctx, http.MethodPost, a.base+"/api/agent/v1/fetch/"+job.ID, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(agentproto.HeaderVersion, a.opt.Version)
	req.Header.Set(agentproto.HeaderTime, strconv.FormatInt(time.Now().UnixMilli(), 10))
	req.Header.Set(agentproto.HeaderFetchResult, base64.StdEncoding.EncodeToString(meta))
	// Returning a segment is the largest thing the agent sends: the best
	// look at the way to the server there is.
	req, done := a.link.watch(req, false)
	resp, err := a.hc.Do(req)
	if err != nil {
		done(false)
		a.log.Debug("cannot return what was fetched for a viewer", "error", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	done(true)
}
