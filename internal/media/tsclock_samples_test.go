package media

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Prints what the program clock of real segments says, when a directory of
// them is given in HLSMON_SAMPLES.
func TestClockOfRealSegments(t *testing.T) {
	dir := os.Getenv("HLSMON_SAMPLES")
	if dir == "" {
		t.Skip("no HLSMON_SAMPLES")
	}
	var files []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && filepath.Ext(p) == ".ts" {
			files = append(files, p)
		}
		return nil
	})
	var pcr, pat, pmt, lead []float64
	jumps := 0
	for _, f := range files {
		data, _ := os.ReadFile(f)
		info := Analyze(data, nil)
		c := info.Clock
		if c == nil || c.PCRs < 2 {
			continue
		}
		pcr, pat, pmt = append(pcr, c.MaxPCRGap), append(pat, c.MaxPATGap), append(pmt, c.MaxPMTGap)
		if c.HasLead {
			lead = append(lead, c.MinLead)
		}
		jumps += c.PCRJumps
		if c.MaxPCRGap > 0.1 || c.MinLead < 0 || c.PCRJumps > 0 || len(pcr)%40 == 1 {
			fmt.Printf("%-50s pcr %.3f pat %.2f/%d pmt %.2f/%d lead %.3f jumps %d span %.1f\n", filepath.Base(f), c.MaxPCRGap, c.MaxPATGap, c.PATs, c.MaxPMTGap, c.PMTs, c.MinLead, c.PCRJumps, c.Span)
		}
	}
	q := func(name string, v []float64) {
		sort.Float64s(v)
		if n := len(v); n > 0 {
			fmt.Printf("%-5s n %d  min %.3f  p10 %.3f  p50 %.3f  p90 %.3f  max %.3f\n", name, n, v[0], v[n/10], v[n/2], v[n*9/10], v[n-1])
		}
	}
	q("pcr", pcr)
	q("pat", pat)
	q("pmt", pmt)
	q("lead", lead)
	fmt.Println("jumps", jumps, "files", len(files))
}
