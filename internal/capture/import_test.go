package capture

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	desc, _ := json.Marshal(Capture{ID: "20260101-1", StreamID: 7, Variant: "v", Kinds: []string{"pts_discontinuity"}, State: StateKept, At: time.Now(), Done: true,
		Files: []File{{Role: "segment", Name: "segments/1.ts", Bytes: 5}}})
	good := map[string][]byte{"20260101-1/capture.json": desc, "20260101-1/segments/1.ts": []byte("12345")}
	a := archiveOf(t, good)
	id, err := s.Import(2, a, a.Size())
	if err != nil || id != "20260101-1-a2" {
		t.Fatalf("imported as %q: %v", id, err)
	}
	list := s.List(7)
	if len(list) != 1 || list[0].AgentID != 2 || !list[0].Done || list[0].Bytes != 5 {
		t.Fatalf("listed %+v", list)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, id, "segments", "1.ts")); string(b) != "12345" {
		t.Errorf("segment written as %q", b)
	}
	// Sent again, after an answer that was lost: the same capture.
	a = archiveOf(t, good)
	if again, err := s.Import(2, a, a.Size()); err != nil || again != id || len(s.List(7)) != 1 {
		t.Errorf("a second delivery: %q, %v, %d captures", again, err, len(s.List(7)))
	}
	// It survives a restart.
	s2, _ := Open(Options{Dir: dir, MaxBytes: 10 << 20})
	if l := s2.List(7); len(l) != 1 || l[0].AgentID != 2 {
		t.Errorf("after a restart: %+v", l)
	}

	for name, files := range map[string]map[string][]byte{
		"a way out":           {"20260101-1/capture.json": desc, "20260101-1/../../x": []byte("x")},
		"two captures":        {"20260101-1/capture.json": desc, "other/x": []byte("x")},
		"no description":      {"20260101-1/segments/1.ts": []byte("x")},
		"another description": {"20260101-2/capture.json": desc},
	} {
		a := archiveOf(t, files)
		if id, err := s.Import(3, a, a.Size()); err == nil {
			t.Errorf("%s: imported as %q", name, id)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x")); err == nil {
		t.Error("a file was written outside the store")
	}
}
