// Package relay carries out fetch requests: one address requested on
// behalf of somebody watching a stream. The server does it for itself, an
// agent for what the server asks of it.
package relay

import (
	"context"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/probe"
)

// Fetch requests what job names with the given client, under the policy
// on private networks of this process.
func Fetch(ctx context.Context, hc *httpx.Client, job agentproto.FetchRequest) (agentproto.FetchResult, []byte) {
	resp, err := hc.Get(ctx, httpx.Request{URL: job.URL, Headers: job.Headers, HeadersFor: job.HeadersFor, MaxBody: job.MaxBody,
		Reach: probe.Reach(job.Entry, job.Inside), RangeOffset: job.RangeOffset, RangeLength: job.RangeLength})
	var res agentproto.FetchResult
	if err != nil {
		res.Error = err.Error()
	}
	if resp == nil {
		return res, nil
	}
	res.Status, res.FinalURL, res.RemoteAddr, res.Inside = resp.Status, resp.FinalURL, resp.RemoteAddr, resp.Inside
	if resp.Header != nil {
		res.ContentType, res.ContentRange, res.AcceptRanges = resp.Header.Get("Content-Type"), resp.Header.Get("Content-Range"), resp.Header.Get("Accept-Ranges")
	}
	return res, resp.Body
}
