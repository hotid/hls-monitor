package media

import (
	"bytes"

	"github.com/Eyevinn/mp4ff/avc"
)

// A PES packet usually carries one coded picture, but nothing in MPEG-TS
// requires that: it may carry several, or none (an access unit delimiter
// alone), and interlaced video may be coded as two field pictures per
// frame. Counting pictures, frames and keyframes therefore means reading
// the elementary stream: its start codes and the first bits of the picture
// headers. Nothing is decoded.

// pictureScan is what the payload of one PES packet holds.
type pictureScan struct {
	// known is false when the codec is not one the scanner reads.
	known bool
	// halves counts coded pictures in half frames: two for a frame
	// picture, one for a field.
	halves int
	// keys counts the pictures decoding can start at.
	keys     int
	firstKey bool // the first picture of the payload is one of them
	// sps is the sequence parameter set (H.264, HEVC), when asked for.
	sps []byte
	// width and height come from an MPEG-2 sequence header.
	width, height int
	// filler counts the bytes of filler data: what an encoder pads its
	// stream with to keep the rate, and no part of any picture.
	filler int
}

// videoScanner reads the pictures of one video track, PES packet by PES
// packet. It keeps what has to be remembered between packets.
type videoScanner struct {
	// From the H.264 sequence parameter set: what is needed to find
	// field_pic_flag in a slice header.
	fieldsPossible  bool
	frameNumBits    uint
	separatePlanes  bool
	fieldOpen       bool // the previous picture was the first field of a frame
	keyOpen         bool // ...and a keyframe, already counted
	recoveryPending bool // H.264: a recovery point message waits for its picture
}

var startCode = []byte{0, 0, 1}

func (v *videoScanner) setH264SPS(sps *avc.SPS) {
	v.fieldsPossible = !sps.FrameMbsOnlyFlag
	v.frameNumBits = sps.Log2MaxFrameNumMinus4 + 4
	v.separatePlanes = sps.SeparateColourPlaneFlag
}

// picture accounts for one coded picture.
func (v *videoScanner) picture(out *pictureScan, key, field bool) {
	second := field && v.fieldOpen
	v.fieldOpen = field && !second
	if out.halves == 0 {
		out.firstKey = key
	}
	if field {
		out.halves++
	} else {
		out.halves += 2
	}
	// The two fields of a keyframe are one keyframe.
	if key && !(second && v.keyOpen) {
		out.keys++
	}
	v.keyOpen = key && field && !second
}

func (v *videoScanner) scan(codec string, data []byte, wantSPS bool) pictureScan {
	out := pictureScan{known: true}
	switch codec {
	case "h264":
		v.scanH264(data, wantSPS, &out)
	case "hevc":
		v.scanHEVC(data, wantSPS, &out)
	case "mpeg2video", "mpeg1video":
		v.scanMPEG2(data, &out)
	default:
		out.known = false
	}
	return out
}

// nextUnit returns the offset of the byte after the next start code at or
// after pos, and -1 when there is none.
func nextUnit(data []byte, pos int) int {
	i := bytes.Index(data[pos:], startCode)
	if i < 0 || pos+i+3 >= len(data) {
		return -1
	}
	return pos + i + 3
}

// unitEnd returns where the unit starting at nal ends.
func unitEnd(data []byte, nal int) int {
	if j := bytes.Index(data[nal:], startCode); j >= 0 {
		return nal + j
	}
	return len(data)
}

func (v *videoScanner) scanH264(data []byte, wantSPS bool, out *pictureScan) {
	for nal := nextUnit(data, 0); nal >= 0; nal = nextUnit(data, nal) {
		switch typ := data[nal] & 0x1f; {
		case typ == 6:
			// Broadcast encoders often never send an IDR picture: decoding
			// starts at a picture announced by a recovery point message.
			if !v.recoveryPending {
				v.recoveryPending = hasRecoveryPoint(data[nal+1 : unitEnd(data, nal)])
			}
		case typ == 7:
			if wantSPS && out.sps == nil {
				out.sps = bytes.TrimRight(data[nal:unitEnd(data, nal)], "\x00")
			}
		case typ == 12:
			out.filler += unitEnd(data, nal) - nal
		case typ >= 1 && typ <= 5:
			// first_mb_in_slice is 0, coded as a single 1 bit, in the
			// slice that begins a picture.
			if nal+1 >= len(data) || data[nal+1]&0x80 == 0 {
				continue
			}
			key := typ == 5 || v.recoveryPending
			v.recoveryPending = false
			v.picture(out, key, v.h264Field(data[nal+1:]))
		}
	}
}

// h264Field reads field_pic_flag from the beginning of a slice header.
func (v *videoScanner) h264Field(slice []byte) bool {
	if !v.fieldsPossible {
		return false
	}
	r := bitReader{data: unescape(slice[:min(len(slice), 24)])}
	r.ue() // first_mb_in_slice
	r.ue() // slice_type
	r.ue() // pic_parameter_set_id
	if v.separatePlanes {
		r.bits(2)
	}
	r.bits(v.frameNumBits)
	return r.bits(1) == 1 && !r.failed
}

func (v *videoScanner) scanHEVC(data []byte, wantSPS bool, out *pictureScan) {
	for nal := nextUnit(data, 0); nal >= 0; nal = nextUnit(data, nal) {
		switch typ := (data[nal] >> 1) & 0x3f; {
		case typ == 33:
			if wantSPS && out.sps == nil {
				out.sps = bytes.TrimRight(data[nal:unitEnd(data, nal)], "\x00")
			}
		case typ == 38:
			out.filler += unitEnd(data, nal) - nal
		case typ <= 31:
			// first_slice_segment_in_pic_flag follows the two header bytes.
			if nal+2 < len(data) && data[nal+2]&0x80 != 0 {
				v.picture(out, typ >= 16 && typ <= 23, false)
			}
		}
	}
}

// scanMPEG2 reads MPEG-2 (and MPEG-1) video: a picture header says whether
// the picture is intra coded, the picture coding extension after it whether
// it is a frame or a field.
func (v *videoScanner) scanMPEG2(data []byte, out *pictureScan) {
	pending, key, field := false, false, false
	flush := func() {
		if pending {
			v.picture(out, key, field)
		}
		pending = false
	}
	for u := nextUnit(data, 0); u >= 0; u = nextUnit(data, u) {
		switch code := data[u]; {
		case code == 0x00 && u+2 < len(data):
			flush()
			pending, key, field = true, (data[u+2]>>3)&7 == 1, false
		case code == 0xb3 && u+3 < len(data) && out.width == 0:
			out.width = int(data[u+1])<<4 | int(data[u+2])>>4
			out.height = int(data[u+2]&0x0f)<<8 | int(data[u+3])
		case code == 0xb5 && pending && u+3 < len(data) && data[u+1]>>4 == 8:
			field = data[u+3]&3 != 3 // picture_structure: 3 is a frame
		}
	}
	flush()
}

// unescape drops emulation prevention bytes: 00 00 03 stands for 00 00.
func unescape(b []byte) []byte {
	if !bytes.Contains(b, []byte{0, 0, 3}) {
		return b
	}
	clean := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if i >= 2 && b[i] == 3 && b[i-1] == 0 && b[i-2] == 0 {
			continue
		}
		clean = append(clean, b[i])
	}
	return clean
}

// bitReader reads bits and Exp-Golomb codes; failed is set when the data
// ran out.
type bitReader struct {
	data   []byte
	pos    uint
	failed bool
}

func (r *bitReader) bits(n uint) uint {
	var out uint
	for ; n > 0; n-- {
		if r.pos/8 >= uint(len(r.data)) {
			r.failed = true
			return 0
		}
		out = out<<1 | uint(r.data[r.pos/8]>>(7-r.pos%8))&1
		r.pos++
	}
	return out
}

func (r *bitReader) ue() uint {
	zeros := uint(0)
	for r.bits(1) == 0 {
		if r.failed || zeros > 31 {
			r.failed = true
			return 0
		}
		zeros++
	}
	return 1<<zeros - 1 + r.bits(zeros)
}
