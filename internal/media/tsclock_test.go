package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// clockPacket makes one packet of pid; pcr and ts are in 90 kHz ticks, -1 for none.
func clockPacket(pid uint16, start bool, pcr, ts int64, payload []byte) []byte {
	p := make([]byte, tsPacketSize)
	for i := range p {
		p[i] = 0xff
	}
	p[0] = 0x47
	p[1] = byte(pid >> 8 & 0x1f)
	if start {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	at := 4
	if pcr >= 0 {
		p[3] = 0x30
		p[4], p[5] = 7, 0x10
		p[6], p[7], p[8], p[9], p[10], p[11] = byte(pcr>>25), byte(pcr>>17), byte(pcr>>9), byte(pcr>>1), byte(pcr<<7)|0x7e, 0
		at = 12
	} else {
		p[3] = 0x10
	}
	if ts >= 0 {
		copy(p[at:], []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5,
			0x21 | byte(ts>>29&0x0e), byte(ts >> 22), byte(ts>>14) | 1, byte(ts >> 7), byte(ts<<1) | 1})
	} else {
		copy(p[at:], payload)
	}
	return p
}

func TestProgramClock(t *testing.T) {
	pat := []byte{0, 0, 0xb0, 13, 0, 1, 0xc1, 0, 0, 0, 1, 0xf0, 0x00} // program 1 is mapped on PID 0x1000
	var data []byte
	add := func(p []byte) { data = append(data, p...) }
	add(clockPacket(0, true, -1, -1, pat))
	add(clockPacket(0x1000, true, -1, -1, []byte{0, 2}))
	for i := int64(0); i < 10; i++ {
		// A reference every 40 ms, a frame to decode 300 ms after it.
		add(clockPacket(0x100, true, i*3600, i*3600+27000, nil))
	}
	c := scanTSClock(data)
	if c.PCRs != 10 || c.PATs != 1 || c.PMTs != 1 || c.PCRJumps != 0 {
		t.Fatalf("read %+v", c)
	}
	near := func(got, want float64) bool { return got > want-0.001 && got < want+0.001 }
	if !near(c.MaxPCRGap, 0.04) || !near(c.MinLead, 0.3) || !near(c.MaxPATGap, 0.36) || !near(c.MaxPMTGap, 0.36) {
		t.Errorf("gap %.3f, lead %.3f, tables %.3f and %.3f; want 0.040, 0.300, 0.360, 0.360", c.MaxPCRGap, c.MinLead, c.MaxPATGap, c.MaxPMTGap)
	}

	// A long silence of the clock, a frame that is late, and a jump back.
	add(clockPacket(0x100, true, 9*3600+18000, 9*3600+18000-900, nil))
	add(clockPacket(0x100, true, 1000, -1, nil))
	c = scanTSClock(data)
	if !near(c.MaxPCRGap, 0.2) || !near(c.MinLead, -0.01) || c.PCRJumps != 1 {
		t.Errorf("gap %.3f, lead %.3f, %d jumps; want 0.200, -0.010, 1", c.MaxPCRGap, c.MinLead, c.PCRJumps)
	}
}

// DVB carries AC-3 as private data and names it by a descriptor: it is
// sound all the same.
func TestSoundCarriedAsPrivateData(t *testing.T) {
	out := filepath.Join(t.TempDir(), "clip.ts")
	msg, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440",
		"-t", "2", "-c:v", "libx264", "-g", "25", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-mpegts_flags", "system_b", "-f", "mpegts", out).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot make the clip: %s", strings.TrimSpace(string(msg)))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	info := Analyze(data, nil)
	if a := info.Audio(); a == nil || a.Codec != "ac3" {
		t.Fatalf("tracks = %+v", info.Tracks)
	}
}
