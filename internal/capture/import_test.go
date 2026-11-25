package capture

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/types/capturestate"
	"github.com/hotid/hls-monitor/internal/types/filerole"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

func archiveOf(t *testing.T, files map[string][]byte) *bytes.Reader {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b.Bytes())
}

func TestImportFromAnAgent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, MaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := json.Marshal(Capture{ID: ID{"20260101-1"}, StreamID: id.StreamOf(7), Variant: variantkey.OfURI("v"), Kinds: []problemkind.Kind{problemkind.PTSDiscontinuity()}, State: capturestate.Kept(), At: time.Now(), Done: true,
		Files: []File{{Role: filerole.Segment(), Name: "segments/1.ts", Bytes: 5}}})
	good := map[string][]byte{"20260101-1/capture.json": desc, "20260101-1/segments/1.ts": []byte("12345")}
	a := archiveOf(t, good)
	name, err := s.Import(id.AgentOf(2), a, a.Size(), nil)
	if err != nil || name.String() != "20260101-1-a2" {
		t.Fatalf("imported as %q: %v", name, err)
	}
	list := s.List(id.StreamOf(7))
	if len(list) != 1 || list[0].AgentID != id.AgentOf(2) || !list[0].Done || list[0].Bytes != 5 {
		t.Fatalf("listed %+v", list)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, name.String(), "segments", "1.ts")); string(b) != "12345" {
		t.Errorf("segment written as %q", b)
	}
	// Sent again, after an answer that was lost: the same capture.
	a = archiveOf(t, good)
	if again, err := s.Import(id.AgentOf(2), a, a.Size(), nil); err != nil || again != name || len(s.List(id.StreamOf(7))) != 1 {
		t.Errorf("a second delivery: %q, %v, %d captures", again, err, len(s.List(id.StreamOf(7))))
	}
	// It survives a restart.
	s2, _ := Open(Options{Dir: dir, MaxBytes: 10 << 20})
	if l := s2.List(id.StreamOf(7)); len(l) != 1 || l[0].AgentID != id.AgentOf(2) {
		t.Errorf("after a restart: %+v", l)
	}

	// A capture of a stream the agent does not check is not taken.
	a = archiveOf(t, map[string][]byte{"20260101-1/capture.json": desc, "20260101-1/segments/1.ts": []byte("12345")})
	if name, err := s.Import(id.AgentOf(5), a, a.Size(), func(stream id.Stream) bool { return stream != id.StreamOf(7) }); !errors.Is(err, ErrNotTheirs) || len(s.List(id.StreamOf(7))) != 1 {
		t.Errorf("a capture of another's stream: %q, %v", name, err)
	}
	// One of nothing but its description is kept as any other.
	bare, _ := json.Marshal(Capture{ID: ID{"20260101-3"}, StreamID: id.StreamOf(8), Variant: variantkey.OfURI("v"), State: capturestate.Kept(), At: time.Now(), Done: true})
	a = archiveOf(t, map[string][]byte{"20260101-3/capture.json": bare})
	if _, err := s.Import(id.AgentOf(2), a, a.Size(), nil); err != nil {
		t.Fatal(err)
	}
	if s3, _ := Open(Options{Dir: dir, MaxBytes: 10 << 20}); len(s3.List(id.StreamOf(8))) != 1 {
		t.Error("a capture without files is gone after a restart")
	}

	// The captures of a stream that is deleted go with it, and those an
	// agent made with the agent.
	if n := s.Forget(id.StreamOf(8), id.AgentOf(0)); n != 1 || len(s.List(id.StreamOf(8))) != 0 || len(s.List(id.StreamOf(7))) != 1 {
		t.Errorf("forgot %d captures of stream 8; left %d and %d", n, len(s.List(id.StreamOf(8))), len(s.List(id.StreamOf(7))))
	}
	if n := s.Forget(id.StreamOf(0), id.AgentOf(2)); n != 1 || len(s.List(id.StreamOf(0))) != 0 {
		t.Errorf("forgot %d captures of agent 2; left %d", n, len(s.List(id.StreamOf(0))))
	}
	if _, err := os.Stat(filepath.Join(dir, name.String())); err == nil {
		t.Error("the files of a forgotten capture are still there")
	}

	for what, files := range map[string]map[string][]byte{
		"a way out":           {"20260101-1/capture.json": desc, "20260101-1/../../x": []byte("x")},
		"two captures":        {"20260101-1/capture.json": desc, "other/x": []byte("x")},
		"no description":      {"20260101-1/segments/1.ts": []byte("x")},
		"another description": {"20260101-2/capture.json": desc},
	} {
		a := archiveOf(t, files)
		if name, err := s.Import(id.AgentOf(3), a, a.Size(), nil); err == nil {
			t.Errorf("%s: imported as %q", what, name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x")); err == nil {
		t.Error("a file was written outside the store")
	}
}
