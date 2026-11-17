package probe

import (
	"context"
	"sync"
)

// A segment is read into memory whole before it is looked at, and nothing
// bounds how many streams download segments at the same moment. The budget
// does: a download waits until the bodies already held leave room for it.
// Waiting delays a check instead of failing it, and happens before the
// request is sent, so it is not counted as the server being slow.

// budget bounds the bytes of segment bodies held in memory at once.
type budget struct {
	mu    sync.Mutex
	limit int64
	used  int64
	// waiting is how many downloads wait for room.
	waiting int
	// freed is closed and replaced every time room is given back.
	freed chan struct{}
}

func newBudget(limit int64) *budget {
	return &budget{limit: limit, freed: make(chan struct{})}
}

// acquire waits for room for n bytes. A request larger than the whole
// budget is let through when nothing else is held, so that it cannot wait
// forever. It returns how much was reserved, zero when ctx ended first.
func (b *budget) acquire(ctx context.Context, n int64) int64 {
	for {
		b.mu.Lock()
		if b.used+n <= b.limit || b.used == 0 {
			b.used += n
			b.mu.Unlock()
			return n
		}
		freed := b.freed
		b.waiting++
		b.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-freed:
		}
		b.mu.Lock()
		b.waiting--
		b.mu.Unlock()
		if ctx.Err() != nil {
			return 0
		}
	}
}

// grow accounts for n more bytes without waiting: a body turned out larger
// than was reserved for it, and it is in memory already.
func (b *budget) grow(n int64) {
	b.mu.Lock()
	b.used += n
	b.mu.Unlock()
}

func (b *budget) release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	close(b.freed)
	b.freed = make(chan struct{})
	b.mu.Unlock()
}

// DefaultBodyMemory is the default budget for segment bodies.
const DefaultBodyMemory = 1 << 30

// bodies is shared by every checker of the process.
var bodies = newBudget(DefaultBodyMemory)

// SetBodyMemory sets how many bytes of segment bodies all checkers of the
// process together may hold in memory. Call it before checks start.
func SetBodyMemory(bytes int64) {
	if bytes > 0 {
		bodies = newBudget(bytes)
	}
}

// BodyMemory reports how many bytes of segment bodies are held, how many
// may be, and how many downloads wait for room.
func BodyMemory() (used, limit int64, waiting int) {
	b := bodies
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.limit, b.waiting
}

// expectedBody estimates the size of a segment before it is requested:
// the byte range when the playlist gives one, otherwise the declared
// bitrate over the segment duration with a margin.
func expectedBody(v Variant, s plSegment) int64 {
	const floor, unknown = 1 << 20, 8 << 20
	n := int64(unknown)
	switch {
	case s.seg.Limit > 0:
		n = s.seg.Limit
	case v.Bandwidth > 0 && s.seg.Duration > 0:
		n = int64(float64(v.Bandwidth) / 8 * s.seg.Duration * 1.5)
	}
	return min(max(n, floor), segmentMaxBody)
}
