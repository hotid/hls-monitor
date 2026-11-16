// Command hls-fake-origin serves an endless fake live HLS stream for
// development and manual testing of the monitor. It loops three short
// sample segments and can be told to misbehave.
//
// Stream URLs: /master.m3u8 (two variants) and /v0/index.m3u8. The same
// paths under /lb/ answer with a redirect to the backend (this server by
// default), the way a CDN entry point sends clients to one of its servers.
//
// Fault injection, all GET for convenience:
//
//	/ctl/down?status=502   playlists answer with this status (0 restores)
//	/ctl/garbage?on=1      playlists return an HTML page
//	/ctl/freeze?on=1       stop publishing new segments
//	/ctl/skip?n=10         publish n segments at once
//	/ctl/rewind?n=5        move the media sequence back
//	/ctl/fail?status=404   the next segment answers with this status
//	/ctl/slow?ms=3000      the next segment is delayed
//	/ctl/variants?n=1      number of variants in the master playlist
//	/ctl/backend?url=http://other:8089
//	                       where /lb/... redirects to (empty: this server);
//	                       /lb/v0/index.m3u8 then behaves like a CDN entry
//	                       point that sends clients to another server
//	/ctl/backend?url=http://other:8089   where /lb/ redirects to (empty: this server)
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
)

func main() {
	listen := flag.String("listen", ":8089", "address to listen on")
	fmp4 := flag.Bool("fmp4", false, "serve fragmented MP4 instead of MPEG-TS")
	window := flag.Int("window", 6, "segments in the playlist")
	flag.String("backend", "", "base URL that /lb/ redirects to; empty means this server")
	flag.Parse()

	o := hlstest.New(hlstest.Options{FMP4: *fmp4, Window: *window, Start: time.Now().Add(-time.Duration(*window) * 2 * time.Second)})
	o.Publish(*window)
	backend := flag.Lookup("backend").Value.String()
	o.SetBackend(backend)

	var frozen atomic.Bool
	go func() {
		for range time.Tick(time.Duration(hlstest.SegmentDur * float64(time.Second))) {
			if !frozen.Load() {
				o.Publish(1)
			}
		}
	}()

	num := func(r *http.Request, name string) int {
		n, _ := strconv.Atoi(r.URL.Query().Get(name))
		return n
	}
	mux := http.NewServeMux()
	mux.Handle("/", o.Handler())
	ctl := func(path string, f func(r *http.Request) string) {
		mux.HandleFunc("/ctl/"+path, func(w http.ResponseWriter, r *http.Request) {
			msg := f(r)
			log.Print(msg)
			fmt.Fprintln(w, msg)
		})
	}
	ctl("down", func(r *http.Request) string {
		o.SetDown(num(r, "status"))
		return fmt.Sprintf("playlist status override: %d", num(r, "status"))
	})
	ctl("garbage", func(r *http.Request) string {
		o.SetGarbage(num(r, "on") != 0)
		return fmt.Sprintf("garbage playlists: %v", num(r, "on") != 0)
	})
	ctl("freeze", func(r *http.Request) string {
		frozen.Store(num(r, "on") != 0)
		return fmt.Sprintf("frozen: %v", frozen.Load())
	})
	ctl("skip", func(r *http.Request) string {
		o.Publish(num(r, "n"))
		return fmt.Sprintf("published %d segments at once", num(r, "n"))
	})
	ctl("rewind", func(r *http.Request) string {
		o.Rewind(uint64(num(r, "n")))
		return fmt.Sprintf("rewound by %d", num(r, "n"))
	})
	ctl("fail", func(r *http.Request) string {
		seq := o.Last() + 1
		o.SetFault(seq, hlstest.Fault{Status: num(r, "status")})
		return fmt.Sprintf("segment %d will answer %d", seq, num(r, "status"))
	})
	ctl("slow", func(r *http.Request) string {
		seq := o.Last() + 1
		o.SetFault(seq, hlstest.Fault{Delay: time.Duration(num(r, "ms")) * time.Millisecond})
		return fmt.Sprintf("segment %d will be delayed by %d ms", seq, num(r, "ms"))
	})
	ctl("backend", func(r *http.Request) string {
		u := r.URL.Query().Get("url")
		o.SetBackend(u)
		if u == "" {
			return "/lb/ redirects to this server"
		}
		return "/lb/ redirects to " + u
	})

	log.Printf("fake HLS origin on %s: /master.m3u8, /v0/index.m3u8", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
