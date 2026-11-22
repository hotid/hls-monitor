// Package capture saves the segments around a finding: the one a check
// suspected, a few before it and a few after, with the playlists they came
// from, so that what went wrong can be taken apart after the stream has
// moved on. It is given the segment at the first suspicion and keeps the
// capture only if an incident is made of it; what is not confirmed is
// thrown away a few minutes later.
//
// Everything is bounded: how much is kept in all, how much one capture
// takes, how many are made at a time and how many from one server. A
// capture that does not fit is not made. Checks do not wait for captures.
package capture

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/hotid/hls-monitor/internal/probe"
)

const (
	// Parallel is how many captures are made at a time, and one of them
	// at the most from any one server: the server may be the one in trouble.
	Parallel = 2
	// before and after are how many segments are saved around the one
	// the finding is about.
	before = 4
	after  = 4
	// captureMax bounds one capture.
	captureMax = 96 << 20
	// pendingFor is for how long a capture waits to be confirmed.
	pendingFor = 10 * time.Minute
	// every is how soon the same stream is captured again; a capture
	// somebody asked for waits only askedEvery.
	every      = 30 * time.Minute
	askedEvery = time.Minute
	// onceIn is for how long a capture that was kept stands for the
	// problems it was made of: a stream that has the same thing wrong
	// with every segment is not captured over and over.
	onceIn = 24 * time.Hour
	// foundMax is how much is kept of captures that no incident came of
	// and whose segment a decoder found fault with all the same.
	foundMax = 200 << 20
	// fetchTimeout bounds one request, decodeTimeout one run of a decoder.
	fetchTimeout  = 30 * time.Second
	decodeTimeout = 30 * time.Second
)

// States of a capture.
const (
	// StatePending: made on suspicion, not confirmed yet.
	StatePending = "pending"
	// StateKept: an incident came of it, or somebody asked for it.
	StateKept = "kept"
	// StateFound: no incident came of it, and a decoder found fault with
	// its segment: kept for a while, within a small limit.
	StateFound = "found"
)

// File is one file of a capture.
type File struct {
	// Role is what it is: "evidence" (the bytes the check read),
	// "segment", "init", "playlist" or "master".
	Role string `json:"role"`
	// Name is where it is in the capture.
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	// Seq and Duration are those of a segment in its playlist; Position
	// is where it stands to the one the finding is about: 0, before (<0)
	// or after (>0).
	Seq      uint64  `json:"seq,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Position int     `json:"position"`
	Status   int     `json:"status,omitempty"`
	Server   string  `json:"server,omitempty"`
	Software string  `json:"software,omitempty"`
	Bytes    int64   `json:"bytes"`
	// Whole tells that all of the segment is there, not its beginning.
	Whole bool `json:"whole"`
	// AfterDetection tells that the file was fetched after the check
	// made its finding, by a request of its own: the server may have
	// answered differently that time. Evidence is never that.
	AfterDetection bool      `json:"after_detection"`
	FetchedAt      time.Time `json:"fetched_at"`
	SHA256         string    `json:"sha256,omitempty"`
	Error          string    `json:"error,omitempty"`
	// Probe names the file with what ffprobe says of it, DecodeErrors the
	// one with what a decoder complained of, when it did.
	Probe        string `json:"probe,omitempty"`
	DecodeErrors string `json:"decode_errors,omitempty"`
}

// Capture describes one capture: it is its capture.json.
type Capture struct {
	ID       string `json:"id"`
	StreamID int64  `json:"stream_id"`
	// AgentID is the agent that made it, for a capture an agent sent; 0
	// for one the server made itself.
	AgentID int64  `json:"agent_id,omitempty"`
	Variant string `json:"variant"`
	// Kinds are the problems it was made on suspicion of; Confirmed
	// those of them that were then reported. Manual tells that somebody
	// asked for it.
	Kinds     []string  `json:"kinds"`
	Confirmed []string  `json:"confirmed,omitempty"`
	Manual    bool      `json:"manual,omitempty"`
	State     string    `json:"state"`
	At        time.Time `json:"at"`
	// Done tells that nothing more is being added to it.
	Done  bool   `json:"done"`
	Seq   uint64 `json:"seq"`
	Bytes int64  `json:"bytes"`
	// Playable tells that index.m3u8 in it plays without the stream.
	Playable bool     `json:"playable"`
	Files    []File   `json:"files"`
	Notes    []string `json:"notes,omitempty"`
}

// Options configure a Store.
type Options struct {
	Dir string
	// MaxBytes bounds everything kept, captures not confirmed yet
	// included; Keep is for how long a capture is kept.
	MaxBytes int64
	Keep     time.Duration
	// FFprobe and FFmpeg are the programs that describe a segment; a
	// capture is made without what they say when they are not there.
	FFprobe, FFmpeg string
	Log             *slog.Logger
}

// Store makes and keeps captures. It is safe for concurrent use.
type Store struct {
	o   Options
	now func() time.Time

	mu       sync.Mutex
	captures map[string]*Capture
	// last is when a stream was last captured; busy the servers a
	// capture is being made from, and running how many are being made.
	last    map[int64]time.Time
	busy    map[string]bool
	running int
	wg      sync.WaitGroup

	made, refused int64
}

// Open reads the captures in a directory, making it if need be. Captures
// that were being made when the process stopped are dropped.
func Open(o Options) (*Store, error) {
	if o.MaxBytes <= 0 {
		o.MaxBytes = 2 << 30
	}
	if o.Keep <= 0 {
		o.Keep = 7 * 24 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if err := os.MkdirAll(o.Dir, 0o750); err != nil {
		return nil, err
	}
	s := &Store{o: o, now: time.Now, captures: map[string]*Capture{}, last: map[int64]time.Time{}, busy: map[string]bool{}}
	entries, err := os.ReadDir(o.Dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var c Capture
		b, err := os.ReadFile(filepath.Join(o.Dir, e.Name(), "capture.json"))
		if err != nil || json.Unmarshal(b, &c) != nil || c.ID != e.Name() || !c.Done {
			_ = os.RemoveAll(filepath.Join(o.Dir, e.Name()))
			continue
		}
		s.captures[c.ID] = &c
	}
	s.Sweep()
	return s, nil
}

// Run sweeps the store once a minute until the context is done.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// Wait returns when no capture is being made any more.
func (s *Store) Wait() { s.wg.Wait() }

// Usage tells how much is kept and how much may be.
type Usage struct {
	Captures int   `json:"captures"`
	Bytes    int64 `json:"bytes"`
	MaxBytes int64 `json:"max_bytes"`
	// Made counts the captures made since the start, Refused those that
	// were not: too many at a time, or too soon after the one before.
	Made    int64 `json:"made"`
	Refused int64 `json:"refused"`
}

// Usage returns the current figures.
func (s *Store) Usage() Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := Usage{Captures: len(s.captures), MaxBytes: s.o.MaxBytes, Made: s.made, Refused: s.refused}
	for _, c := range s.captures {
		u.Bytes += c.Bytes
	}
	return u
}

// List returns the captures of a stream, or all of them for 0, the
// latest first.
func (s *Store) List(streamID int64) []Capture {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Capture{}
	for _, c := range s.captures {
		if streamID == 0 || c.StreamID == streamID {
			out = append(out, clone(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func clone(c *Capture) Capture {
	d := *c
	d.Kinds, d.Confirmed = append([]string(nil), c.Kinds...), append([]string(nil), c.Confirmed...)
	d.Files, d.Notes = append([]File(nil), c.Files...), append([]string(nil), c.Notes...)
	return d
}

// Delete removes a capture that is not being made.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.captures[id]
	if !ok || !c.Done {
		return false
	}
	s.drop(c)
	return true
}

// drop removes a capture; the lock is held.
func (s *Store) drop(c *Capture) {
	delete(s.captures, c.ID)
	_ = os.RemoveAll(filepath.Join(s.o.Dir, c.ID))
}

// Confirm keeps what was captured of a stream on suspicion of problems
// that were then reported.
func (s *Store) Confirm(streamID int64, kinds []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.captures {
		if c.StreamID != streamID || c.State != StatePending {
			continue
		}
		changed := false
		for _, k := range kinds {
			if contains(c.Kinds, k) && !contains(c.Confirmed, k) {
				c.Confirmed = append(c.Confirmed, k)
				changed = true
			}
		}
		if changed {
			c.State = StateKept
			if c.Done {
				s.write(c)
			}
		}
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Sweep drops what is not to be kept: captures nobody confirmed, old
// ones, and the oldest when there is too much.
func (s *Store) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, c := range s.captures {
		if !c.Done {
			continue
		}
		switch {
		case c.State == StatePending && now.Sub(c.At) >= pendingFor:
			// No incident came of it. A segment a decoder found fault
			// with is worth a look all the same.
			if !faulty(c) {
				s.drop(c)
				continue
			}
			c.State = StateFound
			s.write(c)
		case now.Sub(c.At) >= s.o.Keep:
			s.drop(c)
		}
	}
	s.fit(0)
}

func faulty(c *Capture) bool {
	for _, f := range c.Files {
		if f.DecodeErrors != "" {
			return true
		}
	}
	return false
}

// have tells whether a capture kept within onceIn already stands for all
// the problems a new one of the stream would be made of. The lock is held.
func (s *Store) have(rq probe.CaptureRequest) bool {
	if len(rq.Kinds) == 0 {
		return false
	}
	covered := map[string]bool{}
	for _, c := range s.captures {
		if c.StreamID == rq.StreamID && c.State == StateKept && !c.Manual && s.now().Sub(c.At) < onceIn {
			for _, k := range c.Kinds {
				covered[k] = true
			}
		}
	}
	for _, k := range rq.Kinds {
		if !covered[k] {
			return false
		}
	}
	return true
}

// what names what a capture was made of, for telling the common from the rare.
func what(c *Capture) string {
	if c.Manual || len(c.Kinds) == 0 {
		return "asked for"
	}
	kinds := append([]string(nil), c.Kinds...)
	sort.Strings(kinds)
	return strings.Join(kinds, ",")
}

// fit makes room for more bytes within the limits by dropping finished
// captures: those not confirmed first, then those kept for what a decoder
// said, then, of those kept, the oldest of whatever there is most of — a
// problem half the streams have every hour does not push out the one
// capture of a rare one. It tells whether there is room. The lock is held.
func (s *Store) fit(more int64) bool {
	var total, found int64
	for _, c := range s.captures {
		total += c.Bytes
		if c.State == StateFound {
			found += c.Bytes
		}
	}
	for total+more > s.o.MaxBytes || found > foundMax {
		rank := map[string]int{StatePending: 0, StateFound: 1, StateKept: 2}
		many := map[string]int{}
		for _, c := range s.captures {
			if c.Done && c.State == StateKept {
				many[what(c)]++
			}
		}
		var victim *Capture
		worse := func(a, b *Capture) bool { // a goes before b
			if rank[a.State] != rank[b.State] {
				return rank[a.State] < rank[b.State]
			}
			if a.State == StateKept && many[what(a)] != many[what(b)] {
				return many[what(a)] > many[what(b)]
			}
			return a.At.Before(b.At)
		}
		for _, c := range s.captures {
			if !c.Done || (total+more <= s.o.MaxBytes && c.State != StateFound) {
				continue
			}
			if victim == nil || worse(c, victim) {
				victim = c
			}
		}
		if victim == nil {
			break
		}
		total -= victim.Bytes
		if victim.State == StateFound {
			found -= victim.Bytes
		}
		s.drop(victim)
	}
	return total+more <= s.o.MaxBytes
}

// write saves the description of a capture; the lock is held.
func (s *Store) write(c *Capture) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(s.o.Dir, c.ID, "capture.json"), b, 0o640)
	}
	if err != nil {
		s.o.Log.Warn("cannot write the description of a capture", "capture", c.ID, "error", err)
	}
}

// Capture starts a capture, if one may be made now, and returns at once.
func (s *Store) Capture(rq probe.CaptureRequest) {
	if rq.Get == nil || len(rq.Window) == 0 {
		return
	}
	s.mu.Lock()
	wait := every
	if rq.Manual {
		wait = askedEvery
	}
	server := rq.Server
	switch last, seen := s.last[rq.StreamID]; {
	case seen && s.now().Sub(last) < wait:
		// Captured a moment ago; not counted as refused.
		s.mu.Unlock()
		return
	case !rq.Manual && s.have(rq):
		s.mu.Unlock()
		return
	// What somebody asked for is not turned away for the captures the
	// checks are making; only for want of room.
	case (!rq.Manual && (s.running >= Parallel || (server != "" && s.busy[server]))) || !s.fit(captureMax):
		s.refused++
		s.mu.Unlock()
		return
	}
	c := &Capture{
		ID:       fmt.Sprintf("%s-s%d-%d", rq.At.UTC().Format("20060102T150405"), rq.StreamID, rq.Seq),
		StreamID: rq.StreamID, Variant: rq.Variant, Kinds: append([]string(nil), rq.Kinds...), Manual: rq.Manual,
		State: StatePending, At: rq.At, Seq: rq.Seq,
	}
	if rq.Manual {
		c.State = StateKept
	}
	if _, taken := s.captures[c.ID]; taken {
		s.mu.Unlock()
		return
	}
	if err := os.MkdirAll(filepath.Join(s.o.Dir, c.ID), 0o750); err != nil {
		s.mu.Unlock()
		s.o.Log.Warn("cannot make a capture", "error", err)
		return
	}
	s.captures[c.ID] = c
	s.last[rq.StreamID] = s.now()
	s.made++
	if rq.Manual {
		server = ""
	} else {
		s.running++
	}
	if server != "" {
		s.busy[server] = true
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.o.Log.Error("a capture failed", "capture", c.ID, "panic", r)
			}
			s.mu.Lock()
			if !rq.Manual {
				s.running--
			}
			if server != "" {
				delete(s.busy, server)
			}
			c.Done = true
			s.write(c)
			s.fit(0)
			s.mu.Unlock()
		}()
		s.make(c, rq)
	}()
}

// job is a capture being made.
type job struct {
	s   *Store
	c   *Capture
	rq  probe.CaptureRequest
	dir string
	// inits are the initialization sections fetched, by address.
	inits map[string][]byte
	// order is the segments saved, by sequence number, for the playlist.
	order map[uint64]saved
}

type saved struct {
	seg  probe.CaptureSegment
	name string
	init string
}

// make does the work of a capture.
func (s *Store) make(c *Capture, rq probe.CaptureRequest) {
	j := &job{s: s, c: c, rq: rq, dir: filepath.Join(s.o.Dir, c.ID), inits: map[string][]byte{}, order: map[uint64]saved{}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	j.put(File{Role: "playlist", Name: "source/playlist.m3u8", URL: rq.PlaylistURL, Whole: true, FetchedAt: rq.At}, rq.Playlist)
	if len(rq.Master) > 0 {
		j.put(File{Role: "master", Name: "source/master.m3u8", URL: rq.MasterURL, Whole: true, FetchedAt: rq.At}, rq.Master)
	}
	at := -1
	for i, w := range rq.Window {
		if w.Seq == rq.Seq {
			at = i
		}
	}
	if at < 0 {
		j.note("the segment is not in the playlist it was found in")
		return
	}
	trigger := rq.Window[at]
	// The bytes the check read, as they were: nothing fetched afterwards
	// stands in for them.
	j.put(File{Role: "evidence", Name: "evidence/" + fileName(trigger), URL: trigger.URL, Seq: trigger.Seq, Duration: trigger.Duration,
		Status: rq.Status, Server: rq.Server, Software: rq.Software, Whole: rq.EvidenceWhole, FetchedAt: rq.At}, rq.Evidence)

	// The segment itself first, while it is there; then those before it,
	// the latest first, for they leave the playlist soonest; then those
	// that follow.
	if rq.EvidenceWhole {
		j.segment(ctx, trigger, 0, rq.Evidence)
	} else {
		j.segment(ctx, trigger, 0, nil)
	}
	for i := at - 1; i >= 0 && at-i <= before; i-- {
		j.segment(ctx, rq.Window[i], i-at, nil)
	}
	n := 0
	lastSeq := trigger.Seq
	for i := at + 1; i < len(rq.Window) && n < after; i++ {
		n++
		j.segment(ctx, rq.Window[i], n, nil)
		lastSeq = rq.Window[i].Seq
	}
	j.follow(ctx, lastSeq, n)
	j.describe(ctx)
	j.playlist()
}

func (j *job) note(format string, args ...any) {
	j.s.mu.Lock()
	j.c.Notes = append(j.c.Notes, fmt.Sprintf(format, args...))
	j.s.mu.Unlock()
}

// room tells whether the capture may grow.
func (j *job) room() bool {
	j.s.mu.Lock()
	defer j.s.mu.Unlock()
	return j.c.Bytes < captureMax
}

// put writes a file of the capture and records it.
func (j *job) put(f File, data []byte) {
	if len(data) > 0 {
		full := filepath.Join(j.dir, filepath.FromSlash(f.Name))
		err := os.MkdirAll(filepath.Dir(full), 0o750)
		if err == nil {
			err = os.WriteFile(full, data, 0o640)
		}
		if err != nil {
			f.Error = err.Error()
			data = nil
		}
	}
	f.Bytes = int64(len(data))
	if len(data) > 0 {
		sum := sha256.Sum256(data)
		f.SHA256 = hex.EncodeToString(sum[:])
	}
	j.s.mu.Lock()
	j.c.Files = append(j.c.Files, f)
	j.c.Bytes += f.Bytes
	j.s.mu.Unlock()
}

// fileName is the name a segment is saved under: its sequence number and
// the extension its address has.
func fileName(seg probe.CaptureSegment) string {
	return fmt.Sprintf("%010d%s", seg.Seq, extension(seg.URL, ".bin"))
}

func extension(address, fallback string) string {
	u, err := url.Parse(address)
	if err != nil {
		return fallback
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if len(ext) < 2 || len(ext) > 6 {
		return fallback
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return fallback
		}
	}
	return ext
}

// segment saves one segment: the body it is given, or the one it fetches.
func (j *job) segment(ctx context.Context, seg probe.CaptureSegment, position int, body []byte) {
	f := File{Role: "segment", Name: "segments/" + fileName(seg), URL: seg.URL, Seq: seg.Seq, Duration: seg.Duration, Position: position}
	switch {
	case seg.Encrypted:
		f.Error = "encrypted: not captured"
		j.put(f, nil)
		return
	case !j.room():
		f.Error = "left out: the capture is as large as one may be"
		j.put(f, nil)
		return
	}
	if body != nil {
		f.Whole, f.FetchedAt, f.Status, f.Server, f.Software = true, j.rq.At, j.rq.Status, j.rq.Server, j.rq.Software
	} else {
		f.AfterDetection, f.FetchedAt = true, j.s.now()
		fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		resp, err := j.rq.Get(fctx, seg.URL, seg.Offset, seg.Limit)
		cancel()
		if resp != nil {
			f.Status = resp.Status
			if host, _, e := splitHost(resp.RemoteAddr); e == nil {
				f.Server = host
			}
			f.Software = resp.Header.Get("Server")
			// What came is kept even when it is not all of it.
			body = resp.Body
			f.Whole = resp.OK() && !resp.Truncated && !resp.LengthMismatch && err == nil
		}
		switch {
		case err != nil:
			f.Error = err.Error()
		case resp != nil && !resp.OK():
			f.Error = "HTTP " + strconv.Itoa(resp.Status)
			body = nil
		}
	}
	init := ""
	if seg.InitURL != "" && f.Whole {
		init = j.init(ctx, seg.InitURL)
	}
	j.put(f, body)
	if f.Whole && len(body) > 0 {
		j.order[seg.Seq] = saved{seg: seg, name: f.Name, init: init}
	}
}

func splitHost(addr string) (string, string, error) {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return "", "", errors.New("no port")
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}

// init fetches an initialization section once and returns the name it is
// saved under; empty when it could not be had.
func (j *job) init(ctx context.Context, address string) string {
	name := "segments/init-" + strconv.Itoa(len(j.inits)) + extension(address, ".mp4")
	for _, f := range j.c.Files {
		if f.Role == "init" && f.URL == address {
			return f.Name
		}
	}
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	resp, err := j.rq.Get(fctx, address, 0, 0)
	cancel()
	f := File{Role: "init", Name: name, URL: address, AfterDetection: true, FetchedAt: j.s.now()}
	if err != nil || resp == nil || !resp.OK() {
		f.Error = "could not be fetched"
		if err != nil {
			f.Error = err.Error()
		}
		j.put(f, nil)
		return ""
	}
	f.Status, f.Whole = resp.Status, true
	j.inits[name] = resp.Body
	j.put(f, resp.Body)
	return name
}

// follow waits for the segments that come after and saves them: how a
// stream comes out of a fault tells as much as how it went in.
func (j *job) follow(ctx context.Context, lastSeq uint64, have int) {
	if have >= after || len(j.rq.Playlist) == 0 {
		return
	}
	td := j.rq.TargetDuration
	if td <= 0 {
		td = 6
	}
	pause := max(time.Duration(td/2*float64(time.Second)), time.Second)
	deadline := time.Now().Add(time.Duration(float64(after)*td*float64(time.Second)) + 20*time.Second)
	for have < after && time.Now().Before(deadline) && j.room() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		resp, err := j.rq.Get(fctx, j.rq.PlaylistURL, 0, 0)
		cancel()
		if err != nil || resp == nil || !resp.OK() {
			continue
		}
		for _, seg := range window(j.rq.PlaylistURL, resp.Body) {
			if seg.Seq <= lastSeq || have >= after {
				continue
			}
			have++
			lastSeq = seg.Seq
			j.segment(ctx, seg, have, nil)
		}
	}
	if have < after {
		j.note("%d of %d segments after the one in question were saved", have, after)
	}
}

// window reads the segments of a media playlist.
func window(base string, body []byte) []probe.CaptureSegment {
	pl, kind, err := m3u8.DecodeFrom(bytes.NewReader(body), false)
	if err != nil || kind != m3u8.MEDIA {
		return nil
	}
	p := pl.(*m3u8.MediaPlaylist)
	b, err := url.Parse(base)
	if err != nil {
		return nil
	}
	var out []probe.CaptureSegment
	var initURL string
	encrypted := false
	seq := p.SeqNo
	for _, s := range p.GetAllSegments() {
		if s == nil {
			continue
		}
		if s.Map != nil && s.Map.URI != "" {
			if u, err := b.Parse(s.Map.URI); err == nil {
				initURL = u.String()
			}
		}
		for _, k := range s.Keys {
			encrypted = k.Method != "" && !strings.EqualFold(k.Method, "NONE")
		}
		if u, err := b.Parse(s.URI); err == nil && !s.Gap {
			out = append(out, probe.CaptureSegment{Seq: seq, URL: u.String(), Duration: s.Duration, Offset: s.Offset, Limit: s.Limit,
				InitURL: initURL, Discontinuity: s.Discontinuity, Encrypted: encrypted})
		}
		seq++
	}
	return out
}

// describe has ffprobe say what every whole segment is, and a decoder go
// through the one the finding is about and say what it could not decode.
func (j *job) describe(ctx context.Context) {
	for i := range j.c.Files {
		j.s.mu.Lock()
		f := j.c.Files[i]
		j.s.mu.Unlock()
		sv, ok := j.order[f.Seq]
		if f.Role != "segment" || !ok || sv.name != f.Name {
			continue
		}
		data, err := os.ReadFile(filepath.Join(j.dir, filepath.FromSlash(f.Name)))
		if err != nil {
			continue
		}
		if sv.init != "" {
			data = append(append([]byte(nil), j.inits[sv.init]...), data...)
		}
		base := strings.TrimSuffix(path.Base(f.Name), path.Ext(f.Name))
		probeName, errorsName := "", ""
		if j.s.o.FFprobe != "" {
			out, _ := run(ctx, j.s.o.FFprobe, data, "-v", "error", "-protocol_whitelist", "pipe", "-show_format", "-show_streams", "-of", "json", "-i", "pipe:0")
			if len(bytes.TrimSpace(out)) > 0 {
				probeName = "ffprobe/" + base + ".json"
				j.extra(probeName, out)
			}
		}
		if j.s.o.FFmpeg != "" && f.Position == 0 {
			_, complaints := run(ctx, j.s.o.FFmpeg, data, "-nostdin", "-v", "error", "-threads", "1", "-protocol_whitelist", "pipe", "-i", "pipe:0", "-f", "null", "-")
			if len(bytes.TrimSpace(complaints)) > 0 {
				errorsName = "ffprobe/" + base + ".decode-errors.txt"
				j.extra(errorsName, complaints)
			}
		}
		j.s.mu.Lock()
		j.c.Files[i].Probe, j.c.Files[i].DecodeErrors = probeName, errorsName
		j.s.mu.Unlock()
	}
}

// extra writes a file that is about the capture rather than of it.
func (j *job) extra(name string, data []byte) {
	full := filepath.Join(j.dir, filepath.FromSlash(name))
	if os.MkdirAll(filepath.Dir(full), 0o750) == nil && os.WriteFile(full, data, 0o640) == nil {
		j.s.mu.Lock()
		j.c.Bytes += int64(len(data))
		j.s.mu.Unlock()
	}
}

// run gives data to a program on its input and returns what it wrote, to
// its output and to its error output, a megabyte of each at the most. The
// program gets nothing else: no files, no network, no environment.
func run(ctx context.Context, program string, data []byte, args ...string) (out, complaints []byte) {
	ctx, cancel := context.WithTimeout(ctx, decodeTimeout)
	defer cancel()
	script := `ulimit -v 1048576 2>/dev/null; exec "$0" "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", script, program}, args...)...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.Stdin = bytes.NewReader(data)
	var o, e limited
	o.left, e.left = 1<<20, 1<<20
	cmd.Stdout, cmd.Stderr = &o, &e
	cmd.WaitDelay = time.Second
	_ = cmd.Run()
	return o.buf.Bytes(), e.buf.Bytes()
}

type limited struct {
	buf  bytes.Buffer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if n := min(len(p), l.left); n > 0 {
		l.buf.Write(p[:n])
		l.left -= n
	}
	return len(p), nil
}

// playlist writes index.m3u8: the whole segments that were saved, in
// order, by their names in the capture, so that it plays with no stream
// to ask.
func (j *job) playlist() {
	var seqs []uint64
	for seq := range j.order {
		seqs = append(seqs, seq)
	}
	if len(seqs) == 0 {
		j.note("no whole segment was saved: there is nothing to play")
		return
	}
	sort.Slice(seqs, func(a, b int) bool { return seqs[a] < seqs[b] })
	longest := 1.0
	for _, seq := range seqs {
		longest = max(longest, j.order[seq].seg.Duration)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-PLAYLIST-TYPE:VOD\n", int(longest+0.999), seqs[0])
	init := ""
	for i, seq := range seqs {
		sv := j.order[seq]
		// A segment that is missing between two that are there is a break.
		if sv.seg.Discontinuity || (i > 0 && seq != seqs[i-1]+1) {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if sv.init != "" && sv.init != init {
			init = sv.init
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", init)
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", sv.seg.Duration, sv.name)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	j.extra("index.m3u8", []byte(b.String()))
	j.s.mu.Lock()
	j.c.Playable = true
	j.s.mu.Unlock()
}

// WriteZip writes a capture as an archive.
func (s *Store) WriteZip(w io.Writer, id string) error {
	s.mu.Lock()
	c, ok := s.captures[id]
	done := ok && c.Done
	s.mu.Unlock()
	if !done {
		return os.ErrNotExist
	}
	root := filepath.Join(s.o.Dir, id)
	zw := zip.NewWriter(w)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		// Media does not compress; the rest is small.
		method := zip.Deflate
		if strings.HasPrefix(filepath.ToSlash(rel), "segments/") || strings.HasPrefix(filepath.ToSlash(rel), "evidence/") {
			method = zip.Store
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: id + "/" + filepath.ToSlash(rel), Method: method, Modified: s.now()})
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}
