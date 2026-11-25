package capture

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hotid/hls-monitor/internal/types/capturestate"
	"github.com/hotid/hls-monitor/internal/types/id"
)

// importMaxFiles bounds the files of an archive that is brought in.
const importMaxFiles = 200

// ErrNoRoom is returned when a capture does not fit within the limits.
var ErrNoRoom = errors.New("no room for the capture")

// ErrNotTheirs is returned for a capture of a stream the agent that sent
// it does not check.
var ErrNotTheirs = errors.New("a capture of a stream the agent does not check")

// Import brings in a capture an agent made and sends as the archive
// WriteZip makes: one directory named after the capture, its
// capture.json in it. It is kept as the agent's, under an ID of its own,
// and the ID is returned. A capture already brought in is not brought in
// twice: its ID is returned again. theirs, when given, tells whether the
// agent may send a capture of the stream: ErrNotTheirs when not.
func (s *Store) Import(agentID id.Agent, archive io.ReaderAt, size int64, theirs func(stream id.Stream) bool) (string, error) {
	zr, err := zip.NewReader(archive, size)
	if err != nil {
		return "", fmt.Errorf("not a zip archive: %w", err)
	}
	if len(zr.File) == 0 || len(zr.File) > importMaxFiles {
		return "", fmt.Errorf("an archive of %d files", len(zr.File))
	}
	// Every name is under one directory, the capture's, and stays there.
	root := ""
	var desc *zip.File
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		dir, rest, ok := strings.Cut(name, "/")
		if !ok || dir == "" || rest == "" || strings.HasSuffix(name, "/") || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
			return "", fmt.Errorf("a name that does not belong in a capture: %q", name)
		}
		for _, part := range strings.Split(rest, "/") {
			if part == ".." || part == "." {
				return "", fmt.Errorf("a name that does not belong in a capture: %q", name)
			}
		}
		if root == "" {
			root = dir
		} else if dir != root {
			return "", fmt.Errorf("more than one capture in the archive")
		}
		if rest == "capture.json" {
			desc = f
		}
		total += f.UncompressedSize64
	}
	if desc == nil {
		return "", fmt.Errorf("the archive has no capture.json")
	}
	if total > captureMax+(4<<20) {
		return "", fmt.Errorf("a capture of %d bytes, more than one may be", total)
	}
	var c Capture
	if err := readJSON(desc, &c, 4<<20); err != nil {
		return "", fmt.Errorf("capture.json: %w", err)
	}
	if c.ID != root || !c.Done || c.StreamID.IsZero() {
		return "", fmt.Errorf("capture.json does not describe the capture in the archive")
	}
	if theirs != nil && !theirs(c.StreamID) {
		return "", ErrNotTheirs
	}
	id := c.ID + "-a" + agentID.String()
	c.ID, c.AgentID = id, agentID
	if c.State != capturestate.Kept() && c.State != capturestate.Found() {
		c.State = capturestate.Kept()
	}

	s.mu.Lock()
	if _, ok := s.captures[id]; ok {
		s.mu.Unlock()
		return id, nil
	}
	if !s.fit(int64(total)) {
		s.mu.Unlock()
		return "", ErrNoRoom
	}
	// Held as being made while it is written, so that nothing counts it
	// whole or drops it half written.
	c.Done = false
	held := c
	s.captures[id] = &held
	s.mu.Unlock()

	dir := filepath.Join(s.o.Dir, id)
	fail := func(err error) (string, error) {
		s.mu.Lock()
		delete(s.captures, id)
		s.mu.Unlock()
		_ = os.RemoveAll(dir)
		return "", err
	}
	// A capture of nothing but its description has a directory as well:
	// without one it would be gone at the next start.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fail(err)
	}
	var written int64
	for _, f := range zr.File {
		rel := strings.TrimPrefix(f.Name, root+"/")
		if rel == "capture.json" {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if !strings.HasPrefix(dst, dir+string(filepath.Separator)) {
			return fail(fmt.Errorf("a name that does not belong in a capture: %q", f.Name))
		}
		n, err := extract(f, dst, int64(captureMax+(4<<20))-written)
		if err != nil {
			return fail(err)
		}
		written += n
	}
	s.mu.Lock()
	held.Bytes, held.Done = written, true
	s.write(&held)
	s.mu.Unlock()
	return id, nil
}

func readJSON(f *zip.File, out any, limit int64) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// extract writes one file of an archive, no more than left bytes of it.
func extract(f *zip.File, dst string, left int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, err
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, left+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > left {
		err = fmt.Errorf("the archive holds more than it says")
	}
	return n, err
}
