package agent

import (
	"testing"
	"time"
)

// The round trip time reported is the smallest of the last half a minute.
func TestLinkRTTIsTheSmallestOfRecent(t *testing.T) {
	var l linkWatch
	t0 := time.Now()
	if got := l.noteRTT(t0, 40*time.Millisecond); got != 40*time.Millisecond {
		t.Errorf("first reading: %v", got)
	}
	if got := l.noteRTT(t0.Add(time.Second), 2*time.Millisecond); got != 2*time.Millisecond {
		t.Errorf("a smaller one: %v", got)
	}
	if got := l.noteRTT(t0.Add(2*time.Second), 45*time.Millisecond); got != 2*time.Millisecond {
		t.Errorf("delayed acknowledgement must not show: %v", got)
	}
	// The way got slower: seen once the old readings are out of the window.
	if got := l.noteRTT(t0.Add(40*time.Second), 20*time.Millisecond); got != 20*time.Millisecond {
		t.Errorf("after the window: %v", got)
	}
}
