package capture

import (
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/types/id"
)

// The ID of a capture is the name of a directory: it cannot be a path.
func TestCaptureID(t *testing.T) {
	made := newID(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), id.StreamOf(7), 41).From(id.AgentOf(2))
	if made.String() != "20260102T030405-s7-41-a2" {
		t.Errorf("made as %q", made)
	}
	if back, err := ParseID(made.String()); err != nil || back != made {
		t.Errorf("read back as %v, %v", back, err)
	}
	for _, bad := range []string{"", "..", "a/b", `a\b`, "a.b", "a b", "../20260102T030405-s7-41"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("%q was taken as the ID of a capture", bad)
		}
	}
}
