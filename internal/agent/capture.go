package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/capture"
	"github.com/hotid/hls-monitor/internal/probe"
)

// An agent captures the segments around what it finds the way the server
// does, from where it stands, on its own disk; the captures worth keeping
// are sent to the server, which keeps them with its own, and are removed
// here once it has them. Whether to capture at all is the server's word.

// gated hands captures to the store only while the server asks for them.
type gated struct {
	store *capture.Store
	on    atomic.Bool
}

func (g *gated) Capture(rq probe.CaptureRequest) {
	if g.on.Load() {
		g.store.Capture(rq)
	}
}

func (g *gated) Confirm(streamID int64, kinds []string) { g.store.Confirm(streamID, kinds) }

// openCaptures opens the store of captures, with ffprobe and a decoder
// to describe the segments where they are.
func (a *Agent) openCaptures() error {
	dir := a.opt.CaptureDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(a.opt.KeyFile), "captures")
	}
	o := capture.Options{Dir: dir, MaxBytes: a.opt.CaptureBytes, Keep: 24 * time.Hour, Log: a.log}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 1 << 30
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		o.FFprobe = p
	}
	ff := a.opt.FFmpeg
	if ff == "" {
		ff = "ffmpeg"
	}
	if p, err := exec.LookPath(ff); err == nil {
		o.FFmpeg = p
	}
	s, err := capture.Open(o)
	if err != nil {
		return err
	}
	a.captures = &gated{store: s}
	return nil
}

// captureSendEvery is how often finished captures are looked for to be
// sent, in nanoseconds. Tests make it shorter while agents of other tests
// may still be running.
var captureSendEvery atomic.Int64

func init() { captureSendEvery.Store(int64(15 * time.Second)) }

// captureLoop sends the captures worth keeping, one at a time.
func (a *Agent) captureLoop(ctx context.Context) {
	for sleepCtx(ctx, time.Duration(captureSendEvery.Load())) {
		for _, c := range a.captures.store.List(0) {
			if ctx.Err() != nil {
				return
			}
			if !c.Done || (c.State != capture.StateKept && c.State != capture.StateFound) {
				continue
			}
			switch status, err := a.sendCapture(ctx, c.ID); {
			case err != nil || status >= 500 || status == 0 || status == http.StatusTooManyRequests || status == http.StatusUnauthorized:
				// Out of contact, or the server cannot take it now, or
				// does not know the agent now: the key may be given back.
				a.log.Debug("a capture is not sent yet", "capture", c.ID, "status", status, "error", err)
			case status == http.StatusOK:
				a.log.Info("a capture was sent", "capture", c.ID, "stream", c.StreamID)
				a.captures.store.Delete(c.ID)
			default:
				// The server does not take it and will not: no room, or
				// captures turned off. Kept here it would only be sent
				// again and again.
				a.log.Warn("the server did not take a capture; it is dropped", "capture", c.ID, "status", status)
				a.captures.store.Delete(c.ID)
			}
		}
	}
}

// sendCapture sends one capture as an archive.
func (a *Agent) sendCapture(ctx context.Context, id string) (int, error) {
	f, err := os.CreateTemp("", "hls-monitor-capture-*.zip")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := a.captures.store.WriteZip(f, id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return http.StatusGone, nil
		}
		return 0, err
	}
	size, err := f.Seek(0, 1)
	if err != nil {
		return 0, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return 0, err
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, a.base+"/api/agent/v1/captures", f)
	if err != nil {
		return 0, err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set(agentproto.HeaderVersion, a.opt.Version)
	req.Header.Set(agentproto.HeaderTime, strconv.FormatInt(time.Now().UnixMilli(), 10))
	// Not the client of the short requests: an archive of a hundred
	// megabytes over a slow link takes longer than those may, and sctx
	// bounds it.
	resp, err := (&http.Client{Transport: a.hc.Transport}).Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// captureNow takes a capture asked for through the server.
func (a *Agent) captureNow(ctx context.Context, job agentproto.FetchRequest) {
	res := agentproto.FetchResult{Status: http.StatusConflict}
	if a.captures != nil && a.captures.on.Load() && a.run.Capture(job.Capture.StreamID) {
		res.Status = http.StatusAccepted
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.answer(fctx, job.ID, res, nil)
}
