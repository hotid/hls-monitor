package picture

import (
	"log/slog"
	"runtime"
	"time"
)

// Setup makes the decoder a program is configured to look at streams
// with, or nil when it is not to look: every is zero, or the program to
// decode with is not there, which is said in the log.
func Setup(every time.Duration, ffmpeg string, log *slog.Logger) *Decoder {
	if every <= 0 {
		return nil
	}
	d := New(Options{FFmpeg: ffmpeg, Parallel: max(1, runtime.GOMAXPROCS(0)/2)})
	if err := d.Available(); err != nil {
		log.Warn("the picture of the streams will not be checked", "error", err)
		return nil
	}
	log.Info("the picture and the sound of the streams are checked", "every", every.String(), "decoder", d.path)
	return d
}
