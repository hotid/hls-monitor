package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/asticode/go-astits"
)

const (
	tsPacketSize = 188
	ptsWrap      = int64(1) << 33
	tsClock      = 90000.0
)

// findTSSync returns the offset of the first plausible sync byte at or after from.
func findTSSync(data []byte, from int) int {
	for i := from; i < len(data); i++ {
		if data[i] == 0x47 && (i+tsPacketSize >= len(data) || data[i+tsPacketSize] == 0x47) {
			return i
		}
	}
	return -1
}

func analyzeTS(data []byte, info *Info) {
	info.TimeWrap = float64(ptsWrap) / tsClock
	start := scanTSPackets(data, info)
	if start < 0 {
		info.Error = "no MPEG-TS sync found"
		return
	}
	demuxTS(data[start:], info)
}

// scanTSPackets walks raw packets checking sync bytes, continuity counters
// and error flags. It returns the offset of the first valid packet.
func scanTSPackets(data []byte, info *Info) int {
	type pidState struct {
		cc  uint8
		pkt []byte // the last packet with a payload
		// repeated is set once that packet has been sent again.
		repeated bool
	}
	pids := map[uint16]*pidState{}
	info.FirstCC, info.LastCC = map[uint16]uint8{}, map[uint16]uint8{}
	first := -1
	off := 0
	for off+tsPacketSize <= len(data) {
		if data[off] != 0x47 {
			info.SyncErrors++
			n := findTSSync(data, off+1)
			if n < 0 {
				return first
			}
			off = n
			continue
		}
		if first < 0 {
			first = off
		}
		info.Packets++
		p := data[off : off+tsPacketSize]
		off += tsPacketSize

		if p[1]&0x80 != 0 {
			info.TransportErrors++
		}
		pid := uint16(p[1]&0x1f)<<8 | uint16(p[2])
		if pid == 0x1fff {
			continue
		}
		if p[3]>>6 != 0 {
			info.Scrambled = true
		}
		afc := (p[3] >> 4) & 3
		if afc&1 == 0 {
			continue // no payload: the counter does not advance
		}
		cc := p[3] & 0x0f
		discontinuity := afc&2 != 0 && p[4] > 0 && p[5]&0x80 != 0
		st := pids[pid]
		if st == nil {
			pids[pid] = &pidState{cc: cc, pkt: p}
			info.FirstCC[pid] = cc
			info.LastCC[pid] = cc
			continue
		}
		switch {
		case discontinuity:
			st.repeated = false
		case cc == (st.cc+1)&0x0f:
			st.repeated = false
		case cc == st.cc && !st.repeated && sameTSPacket(st.pkt, p):
			// A packet may be sent twice, unchanged, and no more than twice.
			st.repeated = true
		default:
			info.CCErrors++
			st.repeated = false
		}
		st.cc, st.pkt = cc, p
		info.LastCC[pid] = cc
	}
	if off < len(data) {
		// Trailing bytes that do not form a whole packet.
		info.SyncErrors++
	}
	return first
}

// sameTSPacket reports whether b repeats a: every byte equal except the
// program clock reference, which a repeated packet may carry updated.
func sameTSPacket(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	hasPCR := func(p []byte) bool { return (p[3]>>4)&2 != 0 && p[4] >= 7 && p[5]&0x10 != 0 }
	if !hasPCR(a) || !hasPCR(b) {
		return false
	}
	return bytes.Equal(a[:6], b[:6]) && bytes.Equal(a[12:], b[12:])
}

type tsTrack struct {
	Track
	streamType astits.StreamType

	havePTS  bool
	firstPTS int64 // raw 33-bit value used as unwrap reference
	minPTS   int64
	maxPTS   int64

	pesCount int
	// pictures counts coded pictures; a PES packet usually carries one,
	// but nothing in MPEG-TS requires that. lastPictures is how many the
	// last PES packet carried.
	pictures      int
	lastPictures  int
	keyPTS        []int64
	spsDone       bool
	adtsFrames    int
	lastPESPTS    int64
	lastPESFrames int
}

func (t *tsTrack) unwrap(pts int64) int64 {
	rel := (pts - t.firstPTS) & (ptsWrap - 1)
	if rel >= ptsWrap/2 {
		rel -= ptsWrap
	}
	return t.firstPTS + rel
}

func demuxTS(data []byte, info *Info) {
	dmx := astits.NewDemuxer(context.Background(), bytes.NewReader(data))
	tracks := map[uint16]*tsTrack{}
	var order []uint16

	for {
		d, err := dmx.NextData()
		if err != nil {
			if !errors.Is(err, astits.ErrNoMorePackets) {
				info.Error = fmt.Sprintf("ts demux: %v", err)
			}
			break
		}
		switch {
		case d.PAT != nil:
			info.HasPAT = true
		case d.PMT != nil:
			info.HasPMT = true
			for _, es := range d.PMT.ElementaryStreams {
				if _, ok := tracks[es.ElementaryPID]; ok {
					continue
				}
				t := &tsTrack{streamType: es.StreamType}
				t.ID = uint32(es.ElementaryPID)
				t.Kind, t.Codec = tsStreamKind(es.StreamType)
				tracks[es.ElementaryPID] = t
				order = append(order, es.ElementaryPID)
			}
		case d.PES != nil:
			t, ok := tracks[d.PID]
			if !ok || t.Kind == KindOther {
				continue
			}
			handlePES(t, d)
		}
	}

	for _, pid := range order {
		t := tracks[pid]
		if t.Kind == KindOther {
			continue
		}
		finalizeTSTrack(t)
		info.Tracks = append(info.Tracks, t.Track)
	}
}

func tsStreamKind(st astits.StreamType) (kind, codec string) {
	switch st {
	case astits.StreamTypeH264Video:
		return KindVideo, "h264"
	case astits.StreamTypeH265Video:
		return KindVideo, "hevc"
	case astits.StreamTypeMPEG2Video:
		return KindVideo, "mpeg2video"
	case astits.StreamTypeMPEG1Video:
		return KindVideo, "mpeg1video"
	case astits.StreamTypeMPEG4Video:
		return KindVideo, "mpeg4"
	case astits.StreamTypeAACAudio:
		return KindAudio, "aac"
	case astits.StreamTypeAACLATMAudio:
		return KindAudio, "aac-latm"
	case astits.StreamTypeMPEG1Audio, astits.StreamTypeMPEG2Audio:
		return KindAudio, "mp3"
	case astits.StreamTypeAC3Audio:
		return KindAudio, "ac3"
	case astits.StreamTypeEAC3Audio:
		return KindAudio, "eac3"
	case astits.StreamTypeDTSAudio:
		return KindAudio, "dts"
	}
	return KindOther, fmt.Sprintf("0x%02x", uint8(st))
}

func handlePES(t *tsTrack, d *astits.DemuxerData) {
	t.pesCount++
	t.Bytes += int64(len(d.PES.Data))

	var pts int64
	hasPTS := false
	if h := d.PES.Header; h != nil && h.OptionalHeader != nil && h.OptionalHeader.PTS != nil {
		pts = h.OptionalHeader.PTS.Base
		hasPTS = true
	}
	if hasPTS {
		if !t.havePTS {
			t.havePTS = true
			t.firstPTS = pts
			t.minPTS, t.maxPTS = pts, pts
		} else {
			pts = t.unwrap(pts)
			t.minPTS = min(t.minPTS, pts)
			t.maxPTS = max(t.maxPTS, pts)
		}
	}

	switch t.Kind {
	case KindVideo:
		key := false
		if d.FirstPacket != nil && d.FirstPacket.AdaptationField != nil {
			key = d.FirstPacket.AdaptationField.RandomAccessIndicator
		}
		switch t.Codec {
		case "h264":
			sps, vcl, recovery := scanNALs(d.PES.Data, false, !t.spsDone)
			// Broadcast encoders often never send an IDR picture: decoding
			// starts at a picture announced by a recovery point message.
			key = key || vcl == 5 || recovery
			if sps != nil && !t.spsDone {
				t.spsDone = true
				applyH264SPS(&t.Track, sps)
			}
		case "hevc":
			sps, vcl, _ := scanNALs(d.PES.Data, true, !t.spsDone)
			key = key || (vcl >= 16 && vcl <= 23)
			if sps != nil && !t.spsDone {
				t.spsDone = true
				applyHEVCSPS(&t.Track, sps)
			}
		}
		t.lastPictures = 1
		if t.Codec == "h264" || t.Codec == "hevc" {
			t.lastPictures = countPictures(d.PES.Data, t.Codec == "hevc")
		}
		t.pictures += t.lastPictures
		if t.pesCount == 1 {
			t.FirstKeyframe = key
		}
		if key {
			t.Keyframes++
			if hasPTS {
				t.keyPTS = append(t.keyPTS, pts)
			}
		}
	case KindAudio:
		if t.Codec == "aac" {
			n, sr, ch := countADTS(d.PES.Data)
			if sr > 0 && t.SampleRate == 0 {
				t.SampleRate, t.Channels = sr, ch
			}
			t.adtsFrames += n
			if hasPTS {
				t.lastPESPTS, t.lastPESFrames = pts, n
			}
		}
	}
}

func finalizeTSTrack(t *tsTrack) {
	t.Frames = t.pesCount
	if !t.havePTS {
		return
	}
	t.HasTiming = true
	span := t.maxPTS - t.minPTS
	end := t.maxPTS

	switch {
	case t.Kind == KindAudio && t.Codec == "aac" && t.SampleRate > 0 && t.adtsFrames > 0:
		t.Frames = t.adtsFrames
		end = t.lastPESPTS + int64(float64(t.lastPESFrames)*1024*tsClock/float64(t.SampleRate))
		end = max(end, t.maxPTS)
	case t.Kind == KindVideo && t.pictures > t.lastPictures && span > 0:
		// Timestamps come one per PES packet, pictures may come several:
		// the span covers every picture but those of the last packet.
		// For field-coded interlaced video this is the field rate.
		t.Frames = t.pictures
		frameDur := float64(span) / float64(t.pictures-t.lastPictures)
		end = t.maxPTS + int64(frameDur*float64(t.lastPictures))
		t.FPS = roundTo(tsClock/frameDur, 3)
	case t.pesCount > 1 && span > 0:
		frameDur := float64(span) / float64(t.pesCount-1)
		end = t.maxPTS + int64(frameDur)
	}
	t.StartPTS = float64(t.minPTS) / tsClock
	t.EndPTS = float64(end) / tsClock

	if t.Kind == KindVideo {
		if len(t.keyPTS) == 0 {
			t.MaxGOP = t.Duration()
		} else {
			var gop int64
			for i := 1; i < len(t.keyPTS); i++ {
				gop = max(gop, t.keyPTS[i]-t.keyPTS[i-1])
			}
			gop = max(gop, end-t.keyPTS[len(t.keyPTS)-1])
			t.MaxGOP = roundTo(float64(gop)/tsClock, 3)
		}
	}
}

// scanNALs walks an Annex B byte stream up to the first VCL NAL unit.
// It returns the SPS payload (when wantSPS and present before the first VCL
// unit), the type of that first VCL unit, or -1 when there is none, and
// whether an H.264 recovery point message precedes it.
func scanNALs(data []byte, isHEVC, wantSPS bool) (sps []byte, vclType int, recovery bool) {
	startCode := []byte{0, 0, 1}
	pos := 0
	for {
		i := bytes.Index(data[pos:], startCode)
		if i < 0 {
			return sps, -1, recovery
		}
		nal := pos + i + 3
		if nal >= len(data) {
			return sps, -1, recovery
		}
		var typ int
		if isHEVC {
			typ = int(data[nal]>>1) & 0x3f
		} else {
			typ = int(data[nal]) & 0x1f
		}
		isVCL := (isHEVC && typ <= 31) || (!isHEVC && typ >= 1 && typ <= 5)
		if isVCL {
			return sps, typ, recovery
		}
		if !isHEVC && typ == 6 && !recovery {
			end := len(data)
			if j := bytes.Index(data[nal:], startCode); j >= 0 {
				end = nal + j
			}
			recovery = hasRecoveryPoint(data[nal+1 : end])
		}
		isSPS := (isHEVC && typ == 33) || (!isHEVC && typ == 7)
		if isSPS && wantSPS && sps == nil {
			end := len(data)
			if j := bytes.Index(data[nal:], startCode); j >= 0 {
				end = nal + j
			}
			sps = bytes.TrimRight(data[nal:end], "\x00")
		}
		pos = nal
	}
}

// countPictures counts the coded pictures in the payload of one PES
// packet: access unit delimiters when the stream has them, otherwise
// slices that begin a picture. It never returns less than one.
func countPictures(data []byte, isHEVC bool) int {
	startCode := []byte{0, 0, 1}
	delimiters, firstSlices := 0, 0
	pos := 0
	for {
		i := bytes.Index(data[pos:], startCode)
		if i < 0 {
			break
		}
		nal := pos + i + 3
		pos = nal
		if isHEVC {
			if nal+2 >= len(data) {
				break
			}
			switch typ := int(data[nal]>>1) & 0x3f; {
			case typ == 35:
				delimiters++
			case typ <= 31 && data[nal+2]&0x80 != 0: // first_slice_segment_in_pic_flag
				firstSlices++
			}
			continue
		}
		if nal+1 >= len(data) {
			break
		}
		switch typ := int(data[nal]) & 0x1f; {
		case typ == 9:
			delimiters++
		case typ >= 1 && typ <= 5 && data[nal+1]&0x80 != 0: // first_mb_in_slice is 0
			firstSlices++
		}
	}
	return max(delimiters, firstSlices, 1)
}

func applyH264SPS(t *Track, nalu []byte) {
	sps, err := avc.ParseSPSNALUnit(nalu, true)
	if err != nil || sps == nil {
		return
	}
	t.Width, t.Height = int(sps.Width), int(sps.Height)
	t.Profile = h264Profile(sps.Profile)
	t.Level = fmt.Sprintf("%d.%d", sps.Level/10, sps.Level%10)
}

func applyHEVCSPS(t *Track, nalu []byte) {
	sps, err := hevc.ParseSPSNALUnit(nalu)
	if err != nil || sps == nil {
		return
	}
	w, h := sps.ImageSize()
	t.Width, t.Height = int(w), int(h)
	t.Profile = hevcProfile(uint32(sps.ProfileTierLevel.GeneralProfileIDC))
	lvl := uint32(sps.ProfileTierLevel.GeneralLevelIDC)
	t.Level = fmt.Sprintf("%d.%d", lvl/30, (lvl%30)/3)
}

var adtsSampleRates = [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// countADTS counts ADTS frames in data and returns the audio parameters of
// the first one.
func countADTS(data []byte) (frames, sampleRate, channels int) {
	pos := 0
	for pos+7 <= len(data) {
		if data[pos] != 0xff || data[pos+1]&0xf6 != 0xf0 {
			break
		}
		if frames == 0 {
			if idx := int(data[pos+2]>>2) & 0x0f; idx < len(adtsSampleRates) {
				sampleRate = adtsSampleRates[idx]
			}
			channels = int(data[pos+2]&1)<<2 | int(data[pos+3]>>6)
			if channels == 7 {
				channels = 8
			}
		}
		size := int(data[pos+3]&3)<<11 | int(data[pos+4])<<3 | int(data[pos+5]>>5)
		if size < 7 {
			break
		}
		frames++
		pos += size
	}
	return frames, sampleRate, channels
}

// analyzePackedAAC handles HLS "packed audio": an optional ID3 tag carrying
// the transport timestamp followed by raw ADTS frames.
func analyzePackedAAC(data []byte, info *Info) {
	info.TimeWrap = float64(ptsWrap) / tsClock
	var start float64
	hasStart := false
	for len(data) >= 10 && bytes.HasPrefix(data, []byte("ID3")) {
		size := int(data[6]&0x7f)<<21 | int(data[7]&0x7f)<<14 | int(data[8]&0x7f)<<7 | int(data[9]&0x7f)
		end := min(10+size, len(data))
		const owner = "com.apple.streaming.transportStreamTimestamp\x00"
		if i := bytes.Index(data[10:end], []byte(owner)); i >= 0 {
			v := data[10+i+len(owner) : end]
			if len(v) >= 8 {
				var ts int64
				for _, b := range v[:8] {
					ts = ts<<8 | int64(b)
				}
				start = float64(ts&(ptsWrap-1)) / tsClock
				hasStart = true
			}
		}
		data = data[end:]
	}
	frames, sr, ch := countADTS(data)
	if frames == 0 {
		info.Error = "no ADTS frames found"
		return
	}
	t := Track{Kind: KindAudio, Codec: "aac", SampleRate: sr, Channels: ch, Frames: frames, Bytes: int64(len(data))}
	if sr > 0 {
		t.HasTiming = hasStart
		t.StartPTS = start
		t.EndPTS = start + float64(frames)*1024/float64(sr)
	}
	info.Tracks = append(info.Tracks, t)
}

func roundTo(v float64, digits int) float64 {
	p := 1.0
	for range digits {
		p *= 10
	}
	if v >= 0 {
		return float64(int64(v*p+0.5)) / p
	}
	return float64(int64(v*p-0.5)) / p
}

// hasRecoveryPoint reports whether an H.264 SEI NAL unit payload carries a
// recovery point message (payload type 6).
func hasRecoveryPoint(sei []byte) bool {
	// Drop emulation prevention bytes: 00 00 03 stands for 00 00.
	if bytes.Contains(sei, []byte{0, 0, 3}) {
		clean := make([]byte, 0, len(sei))
		for i := 0; i < len(sei); i++ {
			if i >= 2 && sei[i] == 3 && sei[i-1] == 0 && sei[i-2] == 0 {
				continue
			}
			clean = append(clean, sei[i])
		}
		sei = clean
	}
	pos := 0
	// The last byte is the trailing bits, not a message.
	for pos+1 < len(sei) {
		typ, size := 0, 0
		for pos < len(sei) && sei[pos] == 0xff {
			typ += 255
			pos++
		}
		if pos >= len(sei) {
			return false
		}
		typ += int(sei[pos])
		pos++
		for pos < len(sei) && sei[pos] == 0xff {
			size += 255
			pos++
		}
		if pos >= len(sei) {
			return false
		}
		size += int(sei[pos])
		pos++
		if typ == 6 {
			return true
		}
		pos += size
	}
	return false
}
