package media

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/hotid/hls-monitor/internal/types/codec"
	"github.com/hotid/hls-monitor/internal/types/trackkind"
)

// Init holds what is needed from an fMP4 initialization section (EXT-X-MAP)
// to interpret media segments.
type Init struct {
	tracks map[uint32]*initTrack
	order  []uint32
}

type initTrack struct {
	Track
	timescale uint32
	trex      *mp4.TrexBox
}

// ParseInit parses an fMP4 initialization section.
func ParseInit(data []byte) (in *Init, err error) {
	defer func() {
		if r := recover(); r != nil {
			in, err = nil, fmt.Errorf("parser panic: %v", r)
		}
	}()
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if f.Init == nil || f.Init.Moov == nil {
		return nil, fmt.Errorf("no moov box in initialization section")
	}
	return initFromMoov(f.Init.Moov), nil
}

func initFromMoov(moov *mp4.MoovBox) *Init {
	in := &Init{tracks: map[uint32]*initTrack{}}
	for _, trak := range moov.Traks {
		if trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Mdhd == nil {
			continue
		}
		t := &initTrack{timescale: trak.Mdia.Mdhd.Timescale}
		t.ID = trak.Tkhd.TrackID
		t.Kind = trackkind.Other()
		if h := trak.Mdia.Hdlr; h != nil {
			switch h.HandlerType {
			case "vide":
				t.Kind = trackkind.Video()
			case "soun":
				t.Kind = trackkind.Audio()
			}
		}
		if moov.Mvex != nil {
			if trex, ok := moov.Mvex.GetTrex(t.ID); ok {
				t.trex = trex
			}
		}
		if trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil && trak.Mdia.Minf.Stbl.Stsd != nil {
			describeSampleEntry(&t.Track, trak.Mdia.Minf.Stbl.Stsd)
		}
		in.tracks[t.ID] = t
		in.order = append(in.order, t.ID)
	}
	return in
}

func describeSampleEntry(t *Track, stsd *mp4.StsdBox) {
	visual := func(v *mp4.VisualSampleEntryBox) {
		t.Codec = codec.Named(v.Type())
		t.Width, t.Height = int(v.Width), int(v.Height)
	}
	audio := func(a *mp4.AudioSampleEntryBox) {
		t.Codec = codec.Named(a.Type())
		t.SampleRate, t.Channels = int(a.SampleRate), int(a.ChannelCount)
	}
	switch {
	case stsd.AvcX != nil:
		visual(stsd.AvcX)
		if c := stsd.AvcX.AvcC; c != nil {
			t.Profile = h264Profile(uint32(c.AVCProfileIndication))
			t.Level = fmt.Sprintf("%d.%d", c.AVCLevelIndication/10, c.AVCLevelIndication%10)
			if len(c.SPSnalus) > 0 {
				if sps, err := avc.ParseSPSNALUnit(c.SPSnalus[0], true); err == nil && sps != nil {
					t.Width, t.Height = int(sps.Width), int(sps.Height)
					if sps.VUI != nil {
						t.Range = videoRange(sps.VUI.TransferCharacteristics)
					}
				}
			}
		}
	case stsd.HvcX != nil:
		visual(stsd.HvcX)
		if c := stsd.HvcX.HvcC; c != nil {
			t.Profile = hevcProfile(uint32(c.GeneralProfileIDC))
			t.Level = fmt.Sprintf("%d.%d", c.GeneralLevelIDC/30, (c.GeneralLevelIDC%30)/3)
			if nalus := c.GetNalusForType(hevc.NALU_SPS); len(nalus) > 0 {
				if sps, err := hevc.ParseSPSNALUnit(nalus[0]); err == nil && sps != nil && sps.VUI != nil {
					t.Range = videoRange(uint(sps.VUI.TransferCharacteristics))
				}
			}
		}
	case stsd.Av01 != nil:
		visual(stsd.Av01)
	case stsd.VpXX != nil:
		visual(stsd.VpXX)
	case stsd.Encv != nil:
		visual(stsd.Encv)
	case stsd.Mp4a != nil:
		audio(stsd.Mp4a)
	case stsd.AC3 != nil:
		audio(stsd.AC3)
	case stsd.EC3 != nil:
		audio(stsd.EC3)
	case stsd.Opus != nil:
		audio(stsd.Opus)
	default:
		for _, c := range stsd.Children {
			t.Codec = codec.Named(c.Type())
			break
		}
	}
}

type fmp4Track struct {
	Track
	timescale uint32
	trex      *mp4.TrexBox

	have   bool
	minPTS int64
	maxEnd int64
	keyPTS []int64
	seen   int
}

func analyzeFMP4(data []byte, init *Init, info *Info) {
	f, err := mp4.DecodeFile(bytes.NewReader(data), mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		info.Error = fmt.Sprintf("mp4 decode: %v", err)
		if f == nil {
			return
		}
	}
	// A segment may carry its own moov (single-file or self-initializing).
	if f.Init != nil && f.Init.Moov != nil {
		init = initFromMoov(f.Init.Moov)
	}

	tracks := map[uint32]*fmp4Track{}
	var order []uint32
	get := func(id uint32) *fmp4Track {
		if t, ok := tracks[id]; ok {
			return t
		}
		t := &fmp4Track{}
		t.ID = id
		t.Kind = trackkind.Other()
		if init != nil {
			if it, ok := init.tracks[id]; ok {
				t.Track = it.Track
				t.timescale = it.timescale
				t.trex = it.trex
			}
		}
		tracks[id] = t
		order = append(order, id)
		return t
	}

	fragments := 0
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			if frag.Moof == nil {
				continue
			}
			fragments++
			for _, traf := range frag.Moof.Trafs {
				if traf.Tfhd == nil {
					continue
				}
				t := get(traf.Tfhd.TrackID)
				var dts int64
				if traf.Tfdt != nil {
					dts = int64(traf.Tfdt.BaseMediaDecodeTime())
				}
				trex := t.trex
				if trex == nil {
					trex = &mp4.TrexBox{}
				}
				// Where the samples lie: from the start of the fragment
				// unless the track says otherwise, one run after another.
				base := int64(frag.Moof.StartPos)
				if traf.Tfhd.HasBaseDataOffset() {
					base = int64(traf.Tfhd.BaseDataOffset)
				}
				at := int64(-1)
				for _, trun := range traf.Truns {
					trun.AddSampleDefaultValues(traf.Tfhd, trex)
					if trun.HasDataOffset() {
						at = base + int64(trun.DataOffset)
					}
					for i := range trun.Samples {
						s := &trun.Samples[i]
						var coded []byte
						if at >= 0 {
							if end := at + int64(s.Size); s.Size > 0 && end <= int64(len(data)) {
								coded = data[at:end]
							}
							at += int64(s.Size)
						}
						switch {
						case coded == nil:
						case t.Kind == trackkind.Video() && s.IsSync() && len(t.KeyHashes) < maxKeyHashes:
							t.KeyHashes = append(t.KeyHashes, hashOf(coded))
						case t.Kind == trackkind.Audio() && len(t.SoundHashes) < maxSoundHashes:
							t.SoundHashes = append(t.SoundHashes, hashOf(coded))
						}
						pts := dts + int64(s.CompositionTimeOffset)
						end := pts + int64(s.Dur)
						if !t.have {
							t.have = true
							t.minPTS, t.maxEnd = pts, end
						} else {
							t.minPTS = min(t.minPTS, pts)
							t.maxEnd = max(t.maxEnd, end)
						}
						if t.Kind == trackkind.Video() && s.IsSync() {
							t.Keyframes++
							t.keyPTS = append(t.keyPTS, pts)
							if t.seen == 0 {
								t.FirstKeyframe = true
							}
						}
						if t.Kind == trackkind.Video() && !s.IsSync() {
							t.PlainBytes += int64(s.Size)
							t.PlainFrames++
						}
						t.seen++
						t.Packets++
						t.Frames++
						t.Bytes += int64(s.Size)
						dts += int64(s.Dur)
					}
				}
			}
		}
	}
	if fragments == 0 && info.Error == "" {
		info.Error = "no movie fragments found"
	}

	for _, id := range order {
		t := tracks[id]
		if t.have && t.timescale > 0 {
			ts := float64(t.timescale)
			t.HasTiming = true
			t.StartPTS = float64(t.minPTS) / ts
			t.EndPTS = float64(t.maxEnd) / ts
			if t.Kind == trackkind.Video() {
				if d := t.Duration(); d > 0 {
					t.FPS = roundTo(float64(t.Frames)/d, 3)
				}
				if len(t.keyPTS) == 0 {
					t.MaxGOP = roundTo(t.Duration(), 3)
				} else {
					var gop int64
					for i := 1; i < len(t.keyPTS); i++ {
						gop = max(gop, t.keyPTS[i]-t.keyPTS[i-1])
					}
					gop = max(gop, t.maxEnd-t.keyPTS[len(t.keyPTS)-1])
					t.MaxGOP = roundTo(float64(gop)/ts, 3)
				}
			}
		}
		info.Tracks = append(info.Tracks, t.Track)
	}
}
