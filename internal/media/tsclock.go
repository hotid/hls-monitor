package media

// TSClock is what the program clock of an MPEG-TS segment says: how often
// the clock itself and the tables come, and how far ahead of the clock the
// pictures and the sound are sent. A set-top box plays by this clock; a
// player in a browser does not look at it.
type TSClock struct {
	// PCRs is how many clock references were read, Span the time between
	// the first and the last of them.
	PCRs int     `json:"pcrs"`
	Span float64 `json:"span"`
	// MaxPCRGap is the longest time between two clock references in a row,
	// by the clock. TR 101 290 allows 40 ms, and 100 ms at most.
	MaxPCRGap float64 `json:"max_pcr_gap"`
	// PCRJumps counts references that went back or more than a second
	// forward with no discontinuity announced.
	PCRJumps int `json:"pcr_jumps,omitempty"`
	// PATs and PMTs count the tables, MaxPATGap and MaxPMTGap are the
	// longest time between two of them by the clock, counting the tail
	// after the last one. TR 101 290 allows half a second.
	PATs      int     `json:"pats"`
	PMTs      int     `json:"pmts"`
	MaxPATGap float64 `json:"max_pat_gap"`
	MaxPMTGap float64 `json:"max_pmt_gap"`
	// MinLead is the least time by which a frame came before the clock
	// reached the moment it is to be decoded at; negative when it came
	// late. HasLead tells that it was measured.
	MinLead float64 `json:"min_lead"`
	HasLead bool    `json:"has_lead,omitempty"`
}

// scanTSClock reads the program clock of the packets in data, which begins
// at a packet. The first PID that carries a clock is the clock: a segment
// of HLS holds one program.
func scanTSClock(data []byte) *TSClock {
	c := &TSClock{}
	const wrap = float64(1<<33) / tsClock
	var (
		pcrPID           uint16
		havePID, have    bool
		clock            float64
		lastPAT          float64
		lastPMT          float64
		havePAT, havePMT bool
		pmt              = map[uint16]bool{}
	)
	gap := func(since float64, max *float64) {
		if d := clock - since; d > *max {
			*max = d
		}
	}
	for off := 0; off+tsPacketSize <= len(data); off += tsPacketSize {
		p := data[off : off+tsPacketSize]
		if p[0] != 0x47 {
			continue
		}
		pid := uint16(p[1]&0x1f)<<8 | uint16(p[2])
		start := p[1]&0x40 != 0
		afc := (p[3] >> 4) & 3
		body := 4
		if afc&2 != 0 {
			body = 5 + int(p[4])
			if p[4] >= 7 && p[5]&0x10 != 0 && (!havePID || pid == pcrPID) {
				base := uint64(p[6])<<25 | uint64(p[7])<<17 | uint64(p[8])<<9 | uint64(p[9])<<1 | uint64(p[10])>>7
				ext := uint64(p[10]&1)<<8 | uint64(p[11])
				t := float64(base)/tsClock + float64(ext)/27e6
				if have {
					d := t - clock
					if d < -wrap/2 {
						d += wrap
					}
					switch {
					case p[5]&0x80 != 0:
						// Announced: a new clock, and nothing is measured across it.
						havePAT, havePMT = false, false
					case d < 0 || d > 1:
						c.PCRJumps++
						havePAT, havePMT = false, false
					default:
						c.Span += d
						if d > c.MaxPCRGap {
							c.MaxPCRGap = d
						}
					}
				}
				if !have {
					// Tables that came before the first reference are of its time.
					lastPAT, havePAT = t, c.PATs > 0
					lastPMT, havePMT = t, c.PMTs > 0
				}
				pcrPID, havePID, have, clock = pid, true, true, t
				c.PCRs++
			}
		}
		if afc&1 == 0 || body >= tsPacketSize || !start {
			continue
		}
		switch {
		case pid == 0:
			c.PATs++
			if have {
				if havePAT {
					gap(lastPAT, &c.MaxPATGap)
				}
				lastPAT, havePAT = clock, true
			}
			// The programs of the table say where their maps are.
			s := body + 1 + int(p[body])
			if s+8 <= tsPacketSize && p[s] == 0 {
				end := min(s+3+(int(p[s+1]&0x0f)<<8|int(p[s+2]))-4, tsPacketSize)
				for i := s + 8; i+4 <= end; i += 4 {
					if p[i] != 0 || p[i+1] != 0 {
						pmt[uint16(p[i+2]&0x1f)<<8|uint16(p[i+3])] = true
					}
				}
			}
		case pmt[pid]:
			c.PMTs++
			if have {
				if havePMT {
					gap(lastPMT, &c.MaxPMTGap)
				}
				lastPMT, havePMT = clock, true
			}
		case have && body+14 <= tsPacketSize && p[body] == 0 && p[body+1] == 0 && p[body+2] == 1 && (p[body+3]>>5 == 6 || p[body+3]>>4 == 0xe || p[body+3] == 0xbd):
			// A frame begins: when it is to be decoded against the clock now.
			flags := p[body+7] >> 6
			at := body + 9
			if flags == 3 && body+19 <= tsPacketSize {
				at = body + 14
			} else if flags != 2 {
				continue
			}
			ts := uint64(p[at]>>1&7)<<30 | uint64(p[at+1])<<22 | uint64(p[at+2]>>1)<<15 | uint64(p[at+3])<<7 | uint64(p[at+4])>>1
			lead := float64(ts)/tsClock - clock
			if lead < -wrap/2 {
				lead += wrap
			} else if lead > wrap/2 {
				lead -= wrap
			}
			if !c.HasLead || lead < c.MinLead {
				c.MinLead, c.HasLead = lead, true
			}
		}
	}
	// The tail after the last table is time without one as well.
	if havePAT {
		gap(lastPAT, &c.MaxPATGap)
	}
	if havePMT {
		gap(lastPMT, &c.MaxPMTGap)
	}
	return c
}
