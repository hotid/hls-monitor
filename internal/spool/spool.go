// Package spool keeps records on disk until whoever they are for has taken
// them: an agent's results until the server stored them, the server's
// samples until the time series storage did.
package spool

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A spool is a directory of files, each a batch: records of a four-byte
// big-endian length and their bytes. Records are appended to the newest
// file; the sender takes the oldest one, and deletes it once the receiver
// has it. A file is never larger than one request, unless a single record
// is. Neither a long outage of the receiver nor a restart of the sender
// loses what is in it.
//
// A file is synced when it is closed, not on every record: a power failure
// may lose the last seconds of records, a crash or a restart of the process
// loses nothing. A record cut short by a crash ends the file it is in.

const spoolExt = ".q"

type spoolFile struct {
	seq     uint64
	size    int64
	records int
	// created is when the file was begun: no result in it is older.
	created time.Time
}

// Spool is a queue of records on disk. It is safe for concurrent use.
type Spool struct {
	dir      string
	maxBytes int64
	maxFile  int64

	mu     sync.Mutex
	files  []spoolFile // oldest first; the last one may be open for writing
	active *os.File    // the file being written, nil when the last one is closed
	total  int64
	// dropped counts results deleted to keep the spool within maxBytes
	// since the last call of takeDropped.
	dropped int
	// droppedTotal counts them since the agent started.
	droppedTotal int64
}

// Open opens the spool in dir, creating it if needed. maxBytes bounds its
// size: beyond it the oldest records are dropped. maxFile bounds a batch.
func Open(dir string, maxBytes, maxFile int64) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Spool{dir: dir, maxBytes: maxBytes, maxFile: maxFile}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(dir, name)) // a rewrite the agent did not finish
			continue
		}
		var seq uint64
		if !strings.HasSuffix(name, spoolExt) {
			continue
		}
		if _, err := fmt.Sscanf(strings.TrimSuffix(name, spoolExt), "%d", &seq); err != nil {
			continue
		}
		records, err := readSpoolFile(filepath.Join(dir, name))
		if err != nil || len(records) == 0 {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		var size int64
		for _, r := range records {
			size += int64(len(r))
		}
		created := time.Now()
		if info, err := e.Info(); err == nil {
			created = info.ModTime() // not before its last result; close enough after a restart
		}
		s.files = append(s.files, spoolFile{seq: seq, size: size, records: len(records), created: created})
		s.total += size
	}
	sort.Slice(s.files, func(i, j int) bool { return s.files[i].seq < s.files[j].seq })
	return s, nil
}

func (s *Spool) path(seq uint64) string {
	return filepath.Join(s.dir, fmt.Sprintf("%016d%s", seq, spoolExt))
}

// readSpoolFile returns the whole records of a file; a record cut short
// ends it.
func readSpoolFile(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for len(data) >= 4 {
		n := int(binary.BigEndian.Uint32(data))
		if n <= 0 || n > len(data)-4 {
			break
		}
		out = append(out, data[4:4+n])
		data = data[4+n:]
	}
	return out, nil
}

// seal closes the file being written. The caller holds the lock.
func (s *Spool) seal() {
	if s.active != nil {
		_ = s.active.Sync()
		_ = s.active.Close()
		s.active = nil
	}
}

// Add appends one result.
func (s *Spool) Add(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.files[len(s.files)-1].size+int64(len(b)) > s.maxFile {
		s.seal()
	}
	if s.active == nil {
		seq := uint64(1)
		if n := len(s.files); n > 0 {
			seq = s.files[n-1].seq + 1
		}
		f, err := os.OpenFile(s.path(seq), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		s.active = f
		s.files = append(s.files, spoolFile{seq: seq, created: time.Now()})
	}
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(b)))
	if _, err := s.active.Write(append(head[:], b...)); err != nil {
		// What was written of the record is cut off when the file is read.
		s.seal()
		return err
	}
	last := &s.files[len(s.files)-1]
	last.size += int64(len(b))
	last.records++
	s.total += int64(len(b))

	// Over the limit the oldest results go first; the file being written stays.
	for s.total > s.maxBytes && len(s.files) > 1 {
		s.dropped += s.files[0].records
		s.droppedTotal += int64(s.files[0].records)
		s.remove(0)
	}
	return nil
}

// remove deletes the i-th file. The caller holds the lock.
func (s *Spool) remove(i int) {
	f := s.files[i]
	if i == len(s.files)-1 {
		s.seal()
	}
	_ = os.Remove(s.path(f.seq))
	s.total -= f.size
	s.files = append(s.files[:i], s.files[i+1:]...)
}

// Next returns the oldest batch and its number; nil when the spool is
// empty. A file that cannot be read is given up.
func (s *Spool) Next() (seq uint64, records [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.files) > 0 {
		if len(s.files) == 1 {
			s.seal() // nothing older to send: send what has been written so far
		}
		f := s.files[0]
		records, err := readSpoolFile(s.path(f.seq))
		if err == nil && len(records) > 0 {
			return f.seq, records
		}
		s.remove(0)
	}
	return 0, nil
}

// Done tells that the first n records of the batch are not to be sent
// again. The rest, if any, stays at the head of the spool.
func (s *Spool) Done(seq uint64, records [][]byte, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.files) == 0 || s.files[0].seq != seq {
		return nil // dropped for space while it was being sent
	}
	if n >= len(records) {
		s.remove(0)
		return nil
	}
	if n <= 0 {
		return nil
	}
	rest := records[n:]
	var buf []byte
	var size int64
	for _, r := range rest {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(r)))
		buf = append(buf, r...)
		size += int64(len(r))
	}
	tmp := s.path(seq) + ".tmp"
	if err := writeSynced(tmp, buf); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path(seq)); err != nil {
		return err
	}
	s.total -= s.files[0].size - size
	s.files[0].size, s.files[0].records = size, len(rest)
	return nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// Queued reports how many results wait and how many bytes they take.
func (s *Spool) Queued() (records int, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		records += f.records
	}
	return records, s.total
}

// Stats reports what waits, how long the oldest of it has been waiting and
// how many results were dropped for space since the start.
func (s *Spool) Stats() (records int, bytes int64, oldest time.Duration, dropped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		records += f.records
	}
	if len(s.files) > 0 && records > 0 {
		oldest = max(time.Since(s.files[0].created), 0)
	}
	return records, s.total, oldest, s.droppedTotal
}

func (s *Spool) TakeDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.dropped
	s.dropped = 0
	return n
}

func (s *Spool) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seal()
}
